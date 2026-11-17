package service

import (
	"cadenceforge/internal/caption"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/timeline"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Request struct {
	SheetID        string `json:"sheet_id"`
	SheetRevision  int    `json:"sheet_revision"`
	RecipeID       string `json:"recipe_id"`
	RecipeRevision int    `json:"recipe_revision"`
	// CaptionGroup optionally selects a confirmed multilingual group. Without
	// it the render keeps the legacy single-VTT source-cue behaviour.
	CaptionGroup    string   `json:"caption_group,omitempty"`
	CaptionRevision int      `json:"caption_revision,omitempty"`
	Burn            []string `json:"burn_languages,omitempty"`
}

func (s *Service) Submit(key string, request Request) (domain.Render, error) {
	if strings.TrimSpace(key) == "" || len(key) > 128 {
		return domain.Render{}, domain.Invalid("Idempotency-Key requires 1..128 bytes")
	}
	if request.SheetRevision < 1 || request.RecipeRevision < 1 {
		return domain.Render{}, domain.Invalid("explicit positive revisions are required")
	}
	if request.CaptionGroup != "" && request.CaptionRevision < 1 {
		return domain.Render{}, domain.Invalid("caption_group requires an explicit positive caption_revision")
	}
	if request.CaptionGroup == "" && (request.CaptionRevision != 0 || len(request.Burn) != 0) {
		return domain.Render{}, domain.Invalid("caption_revision and burn_languages require caption_group")
	}
	if len(request.Burn) > 2 {
		return domain.Render{}, domain.Invalid("at most two burn languages keep the picture readable")
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	var out domain.Render
	err := s.Store.Update(func(st *domain.State) error {
		fingerprint := hash(request)
		for _, r := range st.Renders {
			if r.Key == key {
				if r.RequestHash != fingerprint {
					return domain.Conflict("idempotency key has different input")
				}
				out = r
				return nil
			}
		}
		pending := 0
		for _, r := range st.Renders {
			if r.State == "queued" || r.State == "running" {
				pending++
			}
		}
		if pending >= s.Config.QueueLimit {
			return domain.Capacity("render queue is full")
		}
		sheets := st.Sheets[request.SheetID]
		recipes := st.Recipes[request.RecipeID]
		if request.SheetRevision > len(sheets) {
			return domain.Missing("sheet revision", request.SheetID)
		}
		if request.RecipeRevision > len(recipes) {
			return domain.Missing("recipe revision", request.RecipeID)
		}
		sheet := sheets[request.SheetRevision-1]
		recipe := recipes[request.RecipeRevision-1]
		sources := map[string]domain.Source{}
		for _, p := range sheet.Spans {
			sources[p.SourceID] = st.Sources[p.SourceID]
		}
		for _, c := range sheet.Cues {
			sources[c.SourceID] = st.Sources[c.SourceID]
		}
		plan, err := timeline.Build(sheet, recipe, sources)
		if err != nil {
			return err
		}
		if request.CaptionGroup != "" {
			set, err := freezeCaptions(st, request, recipe, sheet, s.Config.BurnFont, s.Config.ExplicitBurnFont)
			if err != nil {
				return err
			}
			plan.Captions = &set
			plan.VTT = leadingVTT(set)
		} else {
			plan.VTT = caption.VTT(caption.Map(sheet, plan))
		}
		now := time.Now().UTC()
		id := ID()
		out = domain.Render{ID: id, Key: key, RequestHash: fingerprint, Sheet: sheet, Recipe: recipe, Sources: sources, Plan: plan, State: "queued", Attempts: []domain.Attempt{}, CreatedAt: now, UpdatedAt: now}
		st.Renders[id] = out
		st.Emit(id, "render.queued", "frozen source and recipe revisions")
		return nil
	})
	if err == nil {
		s.Notify()
	}
	return out, err
}

// freezeCaptions copies exact group and language revisions into the render.
// It refuses a group that points at another picture edit, and it refuses burn
// output for non-MP4 recipes instead of silently ignoring the request.
func freezeCaptions(st *domain.State, request Request, recipe domain.Recipe, sheet domain.Sheet, burnFont string, explicitFont bool) (domain.CaptionSet, error) {
	versions := st.CaptionGroups[request.CaptionGroup]
	if len(versions) == 0 {
		return domain.CaptionSet{}, domain.Missing("caption group", request.CaptionGroup)
	}
	if request.CaptionRevision < 1 || request.CaptionRevision > len(versions) {
		return domain.CaptionSet{}, domain.Missing("caption group revision", request.CaptionGroup)
	}
	group := versions[request.CaptionRevision-1]
	if len(group.Languages) == 0 {
		return domain.CaptionSet{}, domain.Conflict("caption group revision has no languages; publish a revision before delivery")
	}
	if group.SheetID != request.SheetID || group.SheetRevision != request.SheetRevision {
		return domain.CaptionSet{}, domain.Conflict("caption group pins another confirmed picture edit")
	}
	set := domain.CaptionSet{GroupID: group.ID, GroupRevision: group.Revision, SheetID: group.SheetID, SheetRevision: group.SheetRevision, Policy: CaptionPolicyFor(burnFont), Tracks: []domain.FrozenCaption{}, BurnLanguages: []string{}}
	for language, revision := range group.Languages {
		tracks := st.CaptionTracks[group.ID][language]
		if revision < 1 || revision > len(tracks) {
			return domain.CaptionSet{}, domain.Missing("caption track revision", group.ID+"/"+language)
		}
		track := tracks[revision-1]
		if track.SheetID != sheet.ID || track.SheetRevision != sheet.Revision {
			return domain.CaptionSet{}, domain.Conflict("caption language " + language + " targets another picture edit")
		}
		intervals, findings := caption.AnalyseTrack(track.Cues, sheet.Duration(), set.Policy)
		set.Tracks = append(set.Tracks, domain.FrozenCaption{Language: language, Revision: revision, SheetID: track.SheetID, SheetRevision: track.SheetRevision, VTT: caption.VTT(intervals), Findings: findings})
	}
	sort.Slice(set.Tracks, func(i, j int) bool { return set.Tracks[i].Language < set.Tracks[j].Language })
	burns := append([]string(nil), request.Burn...)
	sort.Strings(burns)
	for _, language := range burns {
		frozen, ok := set.Language(language)
		if !ok {
			return domain.CaptionSet{}, domain.Invalid("burn language %s is not pinned by the group revision", language)
		}
		if !explicitFont {
			intervals, err := caption.ParseVTT(frozen.VTT)
			if err != nil {
				return domain.CaptionSet{}, fmt.Errorf("frozen %s captions are unreadable: %w", language, err)
			}
			for _, interval := range intervals {
				if caption.CJK(interval.Text) {
					return domain.CaptionSet{}, domain.Conflict("burn language " + language + " needs a font with CJK glyph coverage; the built-in default only covers Latin scripts. Install a CJK font and set FORGE_BURN_FONT instead of burning missing glyphs")
				}
			}
		}
	}
	if len(burns) > 0 && recipe.Format != "mp4" {
		return domain.CaptionSet{}, domain.Invalid("burn_languages requires an MP4 recipe")
	}
	set.BurnLanguages = burns
	if err := domain.ValidateCaptionSet(set, sheet); err != nil {
		return domain.CaptionSet{}, fmt.Errorf("caption set invariant: %w", err)
	}
	return set, nil
}

func leadingVTT(set domain.CaptionSet) string {
	if len(set.Tracks) == 0 {
		return "WEBVTT\n\n"
	}
	return set.Tracks[0].VTT
}
