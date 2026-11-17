package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// CaptionCue is authored on the composition timeline. Unlike a source cue it is
// never split or re-timed by a picture cut: a complete sentence in one language
// may deliberately span one or more cuts.
type CaptionCue struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

// CaptionTrack is one language of a caption group. Every language owns an
// independent revision history; editing French never disturbs Chinese.
type CaptionTrack struct {
	GroupID       string       `json:"group_id"`
	Language      string       `json:"language"`
	Revision      int          `json:"revision"`
	SheetID       string       `json:"sheet_id"`
	SheetRevision int          `json:"sheet_revision"`
	Cues          []CaptionCue `json:"cues"`
	CreatedAt     time.Time    `json:"created_at"`
}

// CaptionGroupRevision is a delivery manifest. It pins one confirmed picture
// edit plus the exact revision of every language that a formal delivery must
// use. Later single-language edits do not change this revision.
type CaptionGroup struct {
	ID            string         `json:"id"`
	Revision      int            `json:"revision"`
	Name          string         `json:"name"`
	SheetID       string         `json:"sheet_id"`
	SheetRevision int            `json:"sheet_revision"`
	Languages     map[string]int `json:"languages"`
	CreatedAt     time.Time      `json:"created_at"`
}

// Finding records a caption property a reviewer must see. Findings never
// change, truncate or shrink the authored text.
type Finding struct {
	Cue    int    `json:"cue"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// CaptionPolicy is frozen into every render so old findings stay explainable
// even if the defaults change later.
type CaptionPolicy struct {
	Version             int     `json:"version"`
	MaxColumnsPerSecond float64 `json:"max_columns_per_second"`
	MaxColumnsPerLine   int     `json:"max_columns_per_line"`
	MaxLines            int     `json:"max_lines"`
	BurnFontSize        int     `json:"burn_font_size"`
	BurnOutline         int     `json:"burn_outline"`
	BurnFont            string  `json:"burn_font"`
}

// FrozenCaption is the submitted state of one language track. VTT and findings
// are recomputed at submit time from the frozen cues and the frozen policy.
type FrozenCaption struct {
	Language      string    `json:"language"`
	Revision      int       `json:"revision"`
	SheetID       string    `json:"sheet_id"`
	SheetRevision int       `json:"sheet_revision"`
	VTT           string    `json:"vtt"`
	Findings      []Finding `json:"findings"`
}

// CaptionSet binds a render to one group revision and all of its language
// revisions. Every track in the set targets the same sheet revision.
type CaptionSet struct {
	GroupID       string          `json:"group_id"`
	GroupRevision int             `json:"group_revision"`
	SheetID       string          `json:"sheet_id"`
	SheetRevision int             `json:"sheet_revision"`
	Policy        CaptionPolicy   `json:"policy"`
	Tracks        []FrozenCaption `json:"tracks"`
	BurnLanguages []string        `json:"burn_languages"`
}

func (c CaptionSet) Language(language string) (FrozenCaption, bool) {
	for _, track := range c.Tracks {
		if track.Language == language {
			return track, true
		}
	}
	return FrozenCaption{}, false
}

// CanonicalLanguage accepts a BCP-47-like tag case-insensitively and returns
// the conventional form used as the storage and download key: the primary
// subtag is lowercase and script subtags are title case ("zh-Hans", "pt-BR").
// Keeping one spelling avoids two tracks that differ only by case.
func CanonicalLanguage(tag string) (string, error) {
	parts := strings.Split(strings.TrimSpace(tag), "-")
	if len(parts) > 5 || len(strings.Join(parts, "")) > 35 {
		return "", Invalid("language must be a BCP-47-like tag of at most 35 characters")
	}
	for i, part := range parts {
		length := len(part)
		if i == 0 && (length < 2 || length > 3) {
			return "", Invalid("language primary subtag must contain 2..3 letters")
		}
		if i > 0 && (length < 2 || length > 8) {
			return "", Invalid("language subtags must contain 2..8 characters")
		}
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z':
			case r >= 'A' && r <= 'Z':
			case i > 0 && r >= '0' && r <= '9':
			default:
				return "", Invalid("language subtags must be alphanumeric")
			}
		}
	}
	canonical := []string{strings.ToLower(parts[0])}
	for _, part := range parts[1:] {
		switch len(part) {
		case 4:
			canonical = append(canonical, strings.ToUpper(part[:1])+strings.ToLower(part[1:]))
		case 2:
			canonical = append(canonical, strings.ToUpper(part))
		default:
			canonical = append(canonical, strings.ToLower(part))
		}
	}
	return strings.Join(canonical, "-"), nil
}

func ValidateLanguage(tag string) error {
	canonical, err := CanonicalLanguage(tag)
	if err != nil {
		return err
	}
	if canonical != tag {
		return Invalid("language %s is not in canonical lowercase form", tag)
	}
	return nil
}

func ValidateCueText(text string) error {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > 2000 || strings.Contains(text, "-->") || strings.ContainsAny(text, "\x00\r") || strings.Contains(text, "\n\n") {
		return Invalid("caption text is empty, too long or contains reserved markup")
	}
	return nil
}

// ValidateCaptionTrack checks composition-timed cues against one sheet
// duration. It rejects overlaps because two simultaneous cues in the same
// language cannot both be read.
func ValidateCaptionTrack(t CaptionTrack, durationMS int64) error {
	if err := ValidateLanguage(t.Language); err != nil {
		return err
	}
	if t.SheetRevision < 1 {
		return Invalid("caption track requires an explicit positive sheet revision")
	}
	if len(t.Cues) == 0 || len(t.Cues) > 500 {
		return Invalid("a caption track requires 1..500 cues")
	}
	var previousEnd int64 = -1
	for i, cue := range t.Cues {
		if cue.StartMS < 0 || cue.StartMS >= durationMS || cue.EndMS <= cue.StartMS {
			return Invalid("caption cue %d is outside the confirmed picture edit", i+1)
		}
		// An overrun is accepted and clipped for display; it is reported as a
		// finding so a too-short shot never silently drops the translation.
		if cue.StartMS < previousEnd {
			return Invalid("caption cue %d overlaps the previous cue in the same language", i+1)
		}
		if err := ValidateCueText(cue.Text); err != nil {
			return Invalid("caption cue %d: %s", i+1, err)
		}
		previousEnd = cue.EndMS
	}
	return nil
}

func ValidateCaptionGroup(g CaptionGroup) error {
	if g.ID == "" || g.Revision < 1 {
		return fmt.Errorf("invalid caption group identity")
	}
	if strings.TrimSpace(g.Name) == "" || len(g.Name) > 160 {
		return Invalid("caption group name must contain 1..160 bytes")
	}
	if g.SheetID == "" || g.SheetRevision < 1 {
		return Invalid("caption group requires a sheet id and positive revision")
	}
	if len(g.Languages) > 8 {
		return Invalid("a caption group supports at most 8 languages")
	}
	for language, revision := range g.Languages {
		if err := ValidateLanguage(language); err != nil {
			return err
		}
		if revision < 1 {
			return Invalid("language %s has invalid revision", language)
		}
	}
	return nil
}

func ValidateCaptionSet(c CaptionSet, sheet Sheet) error {
	if c.GroupID == "" || c.GroupRevision < 1 || c.SheetID != sheet.ID || c.SheetRevision != sheet.Revision {
		return fmt.Errorf("caption set is not bound to the frozen picture edit")
	}
	if len(c.Tracks) == 0 || len(c.Tracks) > 8 {
		return fmt.Errorf("caption set requires 1..8 frozen language tracks")
	}
	seen := map[string]bool{}
	for _, track := range c.Tracks {
		if seen[track.Language] {
			return fmt.Errorf("duplicate frozen language %s", track.Language)
		}
		seen[track.Language] = true
		if err := ValidateLanguage(track.Language); err != nil {
			return err
		}
		if track.Revision < 1 || track.SheetID != c.SheetID || track.SheetRevision != c.SheetRevision {
			return fmt.Errorf("frozen language %s targets another picture edit", track.Language)
		}
		if !strings.HasPrefix(track.VTT, "WEBVTT\n\n") {
			return fmt.Errorf("frozen language %s has no WebVTT", track.Language)
		}
	}
	burns := map[string]bool{}
	for _, language := range c.BurnLanguages {
		if !seen[language] {
			return fmt.Errorf("burn language %s is not in the caption set", language)
		}
		if burns[language] {
			return fmt.Errorf("duplicate burn language %s", language)
		}
		burns[language] = true
	}
	return nil
}
