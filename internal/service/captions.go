package service

import (
	"cadenceforge/internal/caption"
	"cadenceforge/internal/domain"
	"sort"
	"strconv"
	"strings"
	"time"
)

type GroupInput struct {
	Name          string         `json:"name"`
	SheetID       string         `json:"sheet_id"`
	SheetRevision int            `json:"sheet_revision"`
	Languages     map[string]int `json:"languages"`
}

type TrackInput struct {
	SheetID       string              `json:"sheet_id"`
	SheetRevision int                 `json:"sheet_revision"`
	Cues          []domain.CaptionCue `json:"cues"`
}

// SaveCaptionGroup publishes a delivery manifest revision. It pins one sheet
// revision and one exact revision per language. Later single-language edits are
// allowed but do not change this revision.
func (s *Service) SaveCaptionGroup(id string, expected int, input GroupInput) (domain.CaptionGroup, error) {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	if id == "" {
		id = ID()
	}
	var out domain.CaptionGroup
	err := s.Store.Update(func(st *domain.State) error {
		versions := st.CaptionGroups[id]
		if len(versions) != expected {
			return domain.Conflict("caption group revision changed")
		}
		if expected == 0 && len(versions) > 0 {
			return domain.Conflict("caption group already exists")
		}
		if expected > 0 && len(versions) == 0 {
			return domain.Missing("caption group", id)
		}
		if len(input.Languages) > 8 {
			return domain.Invalid("caption group supports at most 8 language revisions")
		}
		sheet, err := pinnedSheet(st, input.SheetID, input.SheetRevision)
		if err != nil {
			return err
		}
		group := domain.CaptionGroup{ID: id, Revision: len(versions) + 1, Name: input.Name, SheetID: input.SheetID, SheetRevision: input.SheetRevision, Languages: map[string]int{}, CreatedAt: time.Now().UTC()}
		languages := []string{}
		for rawLanguage, revision := range input.Languages {
			language, err := domain.CanonicalLanguage(rawLanguage)
			if err != nil {
				return err
			}
			if _, exists := group.Languages[language]; exists {
				return domain.Invalid("language %s appears twice", language)
			}
			tracks := st.CaptionTracks[id][language]
			if revision < 1 || revision > len(tracks) {
				return domain.Missing("caption track revision", id+"/"+language)
			}
			if track := tracks[revision-1]; track.SheetID != sheet.ID || track.SheetRevision != sheet.Revision {
				return domain.Conflict("caption track " + language + " targets another picture edit")
			}
			group.Languages[language] = revision
			languages = append(languages, language)
		}
		sort.Strings(languages)
		if err := domain.ValidateCaptionGroup(group); err != nil {
			return err
		}
		out = group
		st.CaptionGroups[id] = append(versions, group)
		st.Emit(id, "caption.group_revised", "picture edit "+strconv.Itoa(input.SheetRevision)+" pinned with languages "+join(languages))
		return nil
	})
	return out, err
}

// SaveCaptionTrack appends one language revision. Sheet revisions are explicit:
// a translation may be edited while a render is running, but it must state
// which picture edit it was written against.
func (s *Service) SaveCaptionTrack(groupID, language string, expected int, input TrackInput) (domain.CaptionTrack, error) {
	canonical, err := domain.CanonicalLanguage(language)
	if err != nil {
		return domain.CaptionTrack{}, err
	}
	language = canonical
	s.mutation.Lock()
	defer s.mutation.Unlock()
	var out domain.CaptionTrack
	err = s.Store.Update(func(st *domain.State) error {
		if _, ok := st.CaptionGroups[groupID]; !ok {
			return domain.Missing("caption group", groupID)
		}
		versions := st.CaptionTracks[groupID][language]
		if len(versions) != expected {
			return domain.Conflict("caption language revision changed")
		}
		if expected == 0 && len(versions) > 0 {
			return domain.Conflict("caption language already exists")
		}
		if expected > 0 && len(versions) == 0 {
			return domain.Missing("caption language", groupID+"/"+language)
		}
		sheet, err := pinnedSheet(st, input.SheetID, input.SheetRevision)
		if err != nil {
			return err
		}
		track := domain.CaptionTrack{GroupID: groupID, Language: language, Revision: len(versions) + 1, SheetID: sheet.ID, SheetRevision: sheet.Revision, Cues: input.Cues, CreatedAt: time.Now().UTC()}
		if err := domain.ValidateCaptionTrack(track, sheet.Duration()); err != nil {
			return err
		}
		out = track
		st.PutTrack(track)
		st.Emit(groupID, "caption.track_revised", language+" revision "+strconv.Itoa(track.Revision)+" against picture edit "+strconv.Itoa(sheet.Revision))
		return nil
	})
	return out, err
}

type TrackView struct {
	Track    domain.CaptionTrack `json:"track"`
	VTT      string              `json:"vtt"`
	Findings []domain.Finding    `json:"findings"`
}

func (s *Service) CaptionTrackView(groupID, language string, revision int) (TrackView, bool, error) {
	canonical, err := domain.CanonicalLanguage(language)
	if err != nil {
		return TrackView{}, false, err
	}
	language = canonical
	st, err := s.Store.Snapshot()
	if err != nil {
		return TrackView{}, false, err
	}
	versions, err := st.Track(groupID, language)
	if err != nil {
		return TrackView{}, false, err
	}
	if revision == 0 {
		revision = len(versions)
	}
	if revision < 1 || revision > len(versions) {
		return TrackView{}, false, domain.Missing("caption track revision", groupID+"/"+language)
	}
	track := versions[revision-1]
	intervals, findings := caption.AnalyseTrack(track.Cues, st.Sheets[track.SheetID][track.SheetRevision-1].Duration(), CaptionPolicyFor(s.Config.BurnFont))
	current := false
	if group, err := st.GroupRevision(groupID, 0); err == nil && group.Languages[language] == revision {
		current = true
	}
	return TrackView{Track: track, VTT: caption.VTT(intervals), Findings: findings}, current, nil
}

type LanguageStatus struct {
	Language         string           `json:"language"`
	PinnedRevision   int              `json:"pinned_revision"`
	LatestRevision   int              `json:"latest_revision"`
	TargetsLatestCut bool             `json:"targets_latest_picture_edit"`
	Findings         []domain.Finding `json:"findings"`
}

type GroupView struct {
	Group       domain.CaptionGroup  `json:"group"`
	Sheet       domain.Sheet         `json:"sheet"`
	Track       map[string]TrackView `json:"tracks"`
	Languages   []LanguageStatus     `json:"languages"`
	Problems    []domain.Finding     `json:"problems"`
	Policy      domain.CaptionPolicy `json:"policy"`
	Explanation string               `json:"explanation"`
}

// CaptionGroupView explains a group revision without editing anything. It is
// the review surface for the "same confirmed picture, independently saved
// languages" contract.
func (s *Service) CaptionGroupView(id string, revision int) (GroupView, error) {
	st, err := s.Store.Snapshot()
	if err != nil {
		return GroupView{}, err
	}
	versions := st.CaptionGroups[id]
	if len(versions) == 0 {
		return GroupView{}, domain.Missing("caption group", id)
	}
	if revision == 0 {
		revision = len(versions)
	}
	if revision < 1 || revision > len(versions) {
		return GroupView{}, domain.Missing("caption group revision", id)
	}
	group := versions[revision-1]
	sheet := st.Sheets[group.SheetID][group.SheetRevision-1]
	policy := CaptionPolicyFor(s.Config.BurnFont)
	view := GroupView{Group: group, Sheet: sheet, Track: map[string]TrackView{}, Languages: []LanguageStatus{}, Problems: []domain.Finding{}, Policy: policy, Explanation: CaptionExplanation()}
	languages := make([]string, 0, len(group.Languages))
	for language := range group.Languages {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		trackRevision := group.Languages[language]
		track := st.CaptionTracks[id][language][trackRevision-1]
		intervals, findings := caption.AnalyseTrack(track.Cues, sheet.Duration(), policy)
		view.Track[language] = TrackView{Track: track, VTT: caption.VTT(intervals), Findings: findings}
		latest := len(st.CaptionTracks[id][language])
		view.Languages = append(view.Languages, LanguageStatus{Language: language, PinnedRevision: trackRevision, LatestRevision: latest, TargetsLatestCut: track.SheetRevision == len(st.Sheets[group.SheetID]), Findings: findings})
		if latest != trackRevision {
			view.Problems = append(view.Problems, domain.Finding{Code: "newer_language_revision", Detail: language + " has revision " + strconv.Itoa(latest) + "; this group revision pins " + strconv.Itoa(trackRevision) + " and delivery uses the pinned revision"})
		}
	}
	if group.SheetRevision != len(st.Sheets[group.SheetID]) {
		view.Problems = append(view.Problems, domain.Finding{Code: "newer_picture_edit", Detail: "group pins picture edit " + strconv.Itoa(group.SheetRevision) + "; sheet has revision " + strconv.Itoa(len(st.Sheets[group.SheetID]))})
	}
	return view, nil
}

// CaptionExplanation is copied into review responses and delivery bundles so a
// recipient can see the chosen trade-off and what was declared undisplayable.
func CaptionExplanation() string {
	return "Captions are authored on the confirmed composition timeline, so a complete sentence may cross one or more picture cuts without being split. " +
		"Each language is segmented independently for readability; a short shot that cannot carry the translation is reported as a finding instead of losing words, reflowing text or shrinking the whole language uniformly. " +
		"Burned output is optional, uses the same frozen cues as the sidecar, and is limited to two languages so they can be stacked rather than overlap."
}

func CaptionPolicyFor(font string) domain.CaptionPolicy {
	policy := DefaultCaptionPolicy()
	if font != "" {
		policy.BurnFont = font
	}
	return policy
}

func DefaultCaptionPolicy() domain.CaptionPolicy {
	return domain.CaptionPolicy{Version: 1, MaxColumnsPerSecond: 20, MaxColumnsPerLine: 42, MaxLines: 2, BurnFontSize: 16, BurnOutline: 1, BurnFont: "DejaVu Sans"}
}

func pinnedSheet(st *domain.State, id string, revision int) (domain.Sheet, error) {
	versions := st.Sheets[id]
	if len(versions) == 0 {
		return domain.Sheet{}, domain.Missing("sheet", id)
	}
	if revision < 1 || revision > len(versions) {
		return domain.Sheet{}, domain.Missing("sheet revision", id)
	}
	return versions[revision-1], nil
}

func join(values []string) string { return strings.Join(values, ",") }
