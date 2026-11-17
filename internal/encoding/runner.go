package encoding

import (
	"cadenceforge/internal/blob"
	"cadenceforge/internal/caption"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BurnSpec is one optional burned language. SRTPath is the stage-relative file
// written from the same frozen cues as the sidecar.
type BurnSpec struct {
	Language string
	SRTPath  string
}

// Plan names every file a render stage must keep. Sidecar VTT files are always
// produced; the burned file exists only when explicitly requested.
type Plan struct {
	MediaName  string
	Captions   []string
	Burn       []BurnSpec
	BurnedName string
}

func (p Plan) Outputs() []string {
	out := []string{p.MediaName}
	if p.BurnedName != "" {
		out = append(out, p.BurnedName)
	}
	return append(out, p.Captions...)
}

type Runner struct {
	Binary string
	Blobs  *blob.Store
}

func (r Runner) Encode(ctx context.Context, render domain.Render, stage string) (Plan, error) {
	paths := map[string]string{}
	for id, s := range render.Sources {
		path, err := r.Blobs.Object(s.Digest)
		if err != nil {
			return Plan{}, err
		}
		if _, err = os.Stat(path); err != nil {
			return Plan{}, fmt.Errorf("source %s unavailable: %w", id, err)
		}
		paths[id] = path
	}
	plan := Plan{MediaName: "composition." + render.Recipe.Format, Captions: []string{}}
	if render.Plan.Captions == nil {
		if err := os.WriteFile(filepath.Join(stage, "captions.vtt"), []byte(render.Plan.VTT), 0600); err != nil {
			return Plan{}, err
		}
		plan.Captions = append(plan.Captions, "captions.vtt")
	} else {
		for _, track := range render.Plan.Captions.Tracks {
			name := "captions." + track.Language + ".vtt"
			if err := os.WriteFile(filepath.Join(stage, name), []byte(track.VTT), 0600); err != nil {
				return Plan{}, err
			}
			plan.Captions = append(plan.Captions, name)
		}
		for _, language := range render.Plan.Captions.BurnLanguages {
			track, ok := render.Plan.Captions.Language(language)
			if !ok {
				return Plan{}, fmt.Errorf("burn language %s absent from frozen set", language)
			}
			intervals, err := caption.ParseVTT(track.VTT)
			if err != nil {
				return Plan{}, fmt.Errorf("frozen %s captions are unreadable: %w", language, err)
			}
			name := "burn." + language + ".srt"
			if err := os.WriteFile(filepath.Join(stage, name), []byte(caption.SRT(intervals)), 0600); err != nil {
				return Plan{}, err
			}
			plan.Burn = append(plan.Burn, BurnSpec{Language: language, SRTPath: filepath.Join(stage, name)})
		}
		if len(plan.Burn) > 0 {
			plan.BurnedName = "composition.burned.mp4"
		}
	}
	args, err := Arguments(render, paths, filepath.Join(stage, plan.MediaName))
	if err != nil {
		return Plan{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if _, err = media.Run(ctx, r.Binary, args, 65536); err != nil {
		return Plan{}, err
	}
	if len(plan.Burn) > 0 {
		// The burned file is a distinct derivative of the same verified master;
		// a failed burn fails the attempt and never replaces the clean master.
		burnArgs, err := BurnArguments(render, paths, filepath.Join(stage, plan.MediaName), filepath.Join(stage, plan.BurnedName), plan.Burn)
		if err != nil {
			return Plan{}, err
		}
		if _, err = media.Run(ctx, r.Binary, burnArgs, 65536); err != nil {
			return Plan{}, err
		}
	}
	return plan, nil
}
