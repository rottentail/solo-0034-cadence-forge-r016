package caption

import (
	"cadenceforge/internal/domain"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type Interval struct {
	StartMS, EndMS int64
	Text           string
	Order          int
}

// Map clips source cues against the source intervals used by the plan. A cue
// that crosses a cut is emitted once per output placement, which is the
// baseline behaviour for legacy source-cue sheets.
func Map(sheet domain.Sheet, plan domain.Plan) []Interval {
	out := []Interval{}
	for _, p := range plan.Placements {
		for i, c := range sheet.Cues {
			if c.SourceID != p.SourceID {
				continue
			}
			start := max(c.StartMS, p.SourceInMS)
			end := min(c.EndMS, p.SourceOutMS)
			if end <= start {
				continue
			}
			out = append(out, Interval{StartMS: p.OutputInMS + start - p.SourceInMS, EndMS: p.OutputInMS + end - p.SourceInMS, Text: c.Text, Order: i})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMS != out[j].StartMS {
			return out[i].StartMS < out[j].StartMS
		}
		return out[i].Order < out[j].Order
	})
	return out
}

// Display maps composition-timed cues. The complete sentence keeps its authored
// interval; it is not split at a picture cut. It is only clipped to the end of
// the composition, and that clip is reported separately.
func Display(cues []domain.CaptionCue, durationMS int64) ([]Interval, []domain.Finding) {
	out := make([]Interval, 0, len(cues))
	findings := []domain.Finding{}
	for i, cue := range cues {
		if cue.StartMS >= durationMS {
			findings = append(findings, domain.Finding{Cue: i + 1, Code: "cue_after_end", Detail: "cue starts after the confirmed composition ends"})
			continue
		}
		end := cue.EndMS
		if end > durationMS {
			findings = append(findings, domain.Finding{Cue: i + 1, Code: "cue_clipped", Detail: fmt.Sprintf("cue end %dms exceeds composition %dms; display clipped by %dms", cue.EndMS, durationMS, cue.EndMS-durationMS)})
			end = durationMS
		}
		out = append(out, Interval{StartMS: cue.StartMS, EndMS: end, Text: cue.Text, Order: i})
	}
	return out, findings
}

func stamp(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, ms%1000)
}

func VTT(intervals []Interval) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, c := range intervals {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, stamp(c.StartMS), stamp(c.EndMS), strings.ReplaceAll(html.EscapeString(c.Text), "\n\n", "\n"))
	}
	return b.String()
}

// Columns approximates subtitle width in monospace columns: CJK and other wide
// runes count as two, combining marks as zero.
func Columns(text string) int {
	total := 0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || r >= 0x20000:
			total += 2
		default:
			total++
		}
	}
	return total
}

// CJK reports whether text contains Han, Kana or Hangul runes. Burn output for
// such text needs an explicitly chosen font whose glyph coverage is known.
func CJK(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return true
		}
	}
	return false
}

// Lines splits on the authored line breaks. It does not rewrap text: wrapping
// policy would silently change line structure, so the reviewer is told instead.
func Lines(text string) []string {
	return strings.Split(text, "\n")
}

// AnalyseTrack returns display intervals plus every finding a reviewer needs.
// It only observes: no text is removed, reflowed or uniformly shrunk.
func AnalyseTrack(cues []domain.CaptionCue, durationMS int64, policy domain.CaptionPolicy) ([]Interval, []domain.Finding) {
	intervals, findings := Display(cues, durationMS)
	findings = append(findings, AnalyseIntervals(intervals, policy)...)
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Cue < findings[j].Cue })
	return intervals, findings
}

func AnalyseIntervals(intervals []Interval, policy domain.CaptionPolicy) []domain.Finding {
	findings := []domain.Finding{}
	for _, c := range intervals {
		lineCount := 0
		perLine := []int{}
		for _, line := range Lines(c.Text) {
			if columns := Columns(strings.TrimSpace(line)); columns > 0 {
				lineCount++
				perLine = append(perLine, columns)
			}
		}
		if lineCount == 0 {
			findings = append(findings, domain.Finding{Cue: c.Order + 1, Code: "empty_display", Detail: "cue has no visible text"})
			continue
		}
		if policy.MaxLines > 0 && lineCount > policy.MaxLines {
			findings = append(findings, domain.Finding{Cue: c.Order + 1, Code: "too_many_lines", Detail: fmt.Sprintf("%d lines exceed the %d-line reading budget", lineCount, policy.MaxLines)})
		}
		for n, columns := range perLine {
			if policy.MaxColumnsPerLine > 0 && columns > policy.MaxColumnsPerLine {
				findings = append(findings, domain.Finding{Cue: c.Order + 1, Code: "line_too_wide", Detail: fmt.Sprintf("line %d is %d columns; reading budget is %d", n+1, columns, policy.MaxColumnsPerLine)})
			}
		}
		width := 0
		for _, columns := range perLine {
			width += columns
		}
		seconds := float64(c.EndMS-c.StartMS) / 1000
		rate := policy.MaxColumnsPerSecond
		if rate <= 0 {
			rate = 20
		}
		if seconds > 0 && float64(width)/seconds > rate {
			findings = append(findings, domain.Finding{Cue: c.Order + 1, Code: "reading_rate", Detail: fmt.Sprintf("%d columns in %.2fs exceed %.0f columns/s; split, re-time or shorten the translation", width, seconds, rate)})
		}
		// A cue shorter than the time needed to read its own width is a short
		// shot that cannot carry the translation. Report it; do not shrink text.
		if c.EndMS-c.StartMS < 500 {
			findings = append(findings, domain.Finding{Cue: c.Order + 1, Code: "short_display", Detail: fmt.Sprintf("display window is %dms", c.EndMS-c.StartMS)})
		}
	}
	return findings
}

// SRT renders the frozen cues for the optional burn-in pass. The burn uses the
// same composition intervals and text as the sidecar, so both outputs agree.
func SRT(intervals []Interval) string {
	var b strings.Builder
	for i, c := range intervals {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, burnStamp(c.StartMS), burnStamp(c.EndMS), srtText(c.Text))
	}
	return b.String()
}

func burnStamp(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, ms%1000)
}

func srtText(text string) string {
	// libass interprets angle-bracket markup; map them to look-alikes so an
	// authored "<幅度>" is shown literally rather than treated as styling.
	return strings.ReplaceAll(strings.ReplaceAll(text, "<", "‹"), ">", "›")
}

// ParseVTT reads back a VTT produced by this package and is deliberately strict:
// the burn pass must reuse the frozen intervals, and an unreadable VTT must fail
// the attempt rather than silently burn different text.
func ParseVTT(text string) ([]Interval, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) < 4 || lines[0] != "WEBVTT" {
		return nil, fmt.Errorf("missing WEBVTT header")
	}
	out := []Interval{}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		parts := strings.Split(line, " --> ")
		if len(parts) != 2 {
			continue
		}
		start, err := parseStamp(parts[0])
		if err != nil {
			return nil, err
		}
		end, err := parseStamp(parts[1])
		if err != nil {
			return nil, err
		}
		text := []string{}
		for i+1 < len(lines) && lines[i+1] != "" {
			i++
			text = append(text, html.UnescapeString(lines[i]))
		}
		out = append(out, Interval{StartMS: start, EndMS: end, Text: strings.Join(text, "\n"), Order: len(out)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("VTT has no cues")
	}
	return out, nil
}

func parseStamp(value string) (int64, error) {
	clock := strings.LastIndex(value, ".")
	if clock < 0 {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	head, tail := value[:clock], value[clock+1:]
	if len(tail) != 3 {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	fields := strings.Split(head, ":")
	if len(fields) != 3 {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	values := make([]int64, 4)
	var err error
	for i, field := range fields {
		if values[i], err = strconv.ParseInt(field, 10, 64); err != nil {
			return 0, fmt.Errorf("invalid VTT timestamp %q", value)
		}
	}
	if values[2] < 0 || values[2] > 59 || values[1] < 0 || values[1] > 59 {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	ms, err := strconv.ParseInt(tail, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	return values[0]*3600000 + values[1]*60000 + values[2]*1000 + ms, nil
}
