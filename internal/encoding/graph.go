package encoding

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"fmt"
	"strings"
)

func seconds(ms int64) string { return fmt.Sprintf("%.3f", float64(ms)/1000) }

// Arguments builds the clean output graph. Burned captions are produced by a
// separate pass so the clean master stays available for review even when a
// burn is requested.
func Arguments(render domain.Render, paths map[string]string, output string) ([]string, error) {
	return arguments(render, paths, output, nil)
}

// BurnArguments builds the optional burn pass from the already verified clean
// master. Burn languages are stacked with libass so two languages never cover
// each other; neither is scaled down to fit a short shot.
func BurnArguments(render domain.Render, paths map[string]string, master, output string, burn []BurnSpec) ([]string, error) {
	if render.Recipe.Format != "mp4" {
		return nil, fmt.Errorf("burned captions require an MP4 recipe")
	}
	policy := render.Plan.Captions.Policy
	args := []string{"-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-i", master}
	filters := []string{}
	label := "[0:v:0]"
	for i, spec := range burn {
		next := fmt.Sprintf("[burn%d]", i)
		filters = append(filters, fmt.Sprintf("%ssubtitles=filename=%s:force_style='%s'%s", label, escapeFilterPath(spec.SRTPath), burnStyle(&policy, i), next))
		label = next
	}
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", label, "-map", "0:a:0", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-threads", "1", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart")
	return append(args, "-t", seconds(render.Plan.DurationMS), "-map_metadata", "-1", output), nil
}

func arguments(render domain.Render, paths map[string]string, output string, _ []BurnSpec) ([]string, error) {
	r := render.Recipe
	args := []string{"-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-filter_complex_threads", "1"}
	filters := []string{}
	joins := []string{}
	for i, p := range render.Plan.Placements {
		path, ok := paths[p.SourceID]
		if !ok {
			return nil, fmt.Errorf("source object missing")
		}
		args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", media.Formats, "-i", path)
		audio := fmt.Sprintf("[%d:a:0]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aresample=%d,aformat=sample_fmts=fltp:channel_layouts=stereo,volume=%.3fdB[a%d]", i, seconds(p.SourceInMS), seconds(p.SourceOutMS), r.SampleRate, p.GainDB, i)
		if !render.Sources[p.SourceID].Media.Audio {
			audio = fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d]", r.SampleRate, seconds(p.OutputOutMS-p.OutputInMS), i)
		}
		filters = append(filters, audio)
		if r.Format == "mp4" {
			filters = append(filters, fmt.Sprintf("[%d:v:0]trim=start=%s:end=%s,setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%d,format=yuv420p[v%d]", i, seconds(p.SourceInMS), seconds(p.SourceOutMS), r.Width, r.Height, r.Width, r.Height, r.FPS, i))
			joins = append(joins, fmt.Sprintf("[v%d][a%d]", i, i))
		} else {
			joins = append(joins, fmt.Sprintf("[a%d]", i))
		}
	}
	if len(joins) == 0 {
		return nil, fmt.Errorf("empty render")
	}
	if r.Format == "mp4" {
		filters = append(filters, strings.Join(joins, "")+fmt.Sprintf("concat=n=%d:v=1:a=1[vout][acat]", len(joins)))
	} else {
		filters = append(filters, strings.Join(joins, "")+fmt.Sprintf("concat=n=%d:v=0:a=1[acat]", len(joins)))
	}
	if r.Normalize {
		filters = append(filters, fmt.Sprintf("[acat]loudnorm=I=%.2f:TP=-1.5:LRA=11,aresample=%d[aout]", r.TargetLUFS, r.SampleRate))
	} else {
		filters = append(filters, "[acat]anull[aout]")
	}
	videoLabel := "[vout]"
	args = append(args, "-filter_complex", strings.Join(filters, ";"))
	if r.Format == "mp4" {
		args = append(args, "-map", videoLabel, "-map", "[aout]", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-threads", "1", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart")
	} else {
		args = append(args, "-map", "[aout]", "-c:a", "pcm_s16le")
	}
	return append(args, "-ar", fmt.Sprint(r.SampleRate), "-t", seconds(render.Plan.DurationMS), "-map_metadata", "-1", output), nil
}

// burnStyle uses a fixed, readable size and a stacked layout. When two
// languages are requested the first sits above the second, centred.
func burnStyle(policy *domain.CaptionPolicy, index int) string {
	size, outline, margin := 16, 1, 18
	font := "DejaVu Sans"
	if policy != nil {
		if policy.BurnFont != "" {
			font = policy.BurnFont
		}
		if policy.BurnFontSize > 0 {
			size = policy.BurnFontSize
		}
		if policy.BurnOutline > 0 {
			outline = policy.BurnOutline
		}
	}
	// Stack languages upward from the bottom edge; sharing one margin would
	// make the second language cover the first.
	margin += index * (size + 8)
	return fmt.Sprintf("FontName=%s,FontSize=%d,Outline=%d,Shadow=0,MarginV=%d,Alignment=2", font, size, outline, margin)
}

// escapeFilterPath escapes a path for the filtergraph value syntax.
func escapeFilterPath(path string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `:`, `\:`, `'`, `\'`, `[`, `\[`, `]`, `\]`, `,`, `\,`)
	return replacer.Replace(path)
}
