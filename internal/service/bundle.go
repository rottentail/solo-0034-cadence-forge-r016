package service

import (
	"archive/zip"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/inspect"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func (s *Service) Artifact(id string) (domain.Render, string, error) {
	st, err := s.Store.Snapshot()
	if err != nil {
		return domain.Render{}, "", err
	}
	r, ok := st.Renders[id]
	if !ok {
		return r, "", domain.Missing("render", id)
	}
	if r.State != "succeeded" || r.Artifact == nil {
		return r, "", domain.Conflict("render has no committed artifact")
	}
	dir, err := s.Blobs.RenderDir(id, len(r.Attempts))
	return r, dir, err
}

func (s *Service) Verify(ctx context.Context, id string) (domain.Inspection, error) {
	r, dir, err := s.Artifact(id)
	if err != nil {
		return domain.Inspection{}, err
	}
	in, err := inspect.Artifact(ctx, s.Probe, filepath.Join(dir, r.Artifact.Filename), r)
	if err != nil {
		return in, err
	}
	if in.Digest != r.Artifact.Inspection.Digest || in.Bytes != r.Artifact.Inspection.Bytes {
		return in, fmt.Errorf("committed artifact integrity failed")
	}
	if err = s.verifyCaptions(r, dir); err != nil {
		return in, err
	}
	if r.Artifact.BurnedFile != "" {
		if r.Artifact.BurnedInspection == nil {
			return in, fmt.Errorf("burned derivative has no committed inspection")
		}
		burned, err := inspect.Artifact(ctx, s.Probe, filepath.Join(dir, r.Artifact.BurnedFile), r)
		if err != nil {
			return in, err
		}
		if burned.Digest != r.Artifact.BurnedInspection.Digest || burned.Bytes != r.Artifact.BurnedInspection.Bytes {
			return in, fmt.Errorf("burned derivative integrity failed")
		}
	}
	return in, nil
}

// verifyCaptions checks every authorized language file against the frozen set.
// A missing or edited file fails the whole delivery rather than quietly
// shipping a partial group.
func (s *Service) verifyCaptions(r domain.Render, dir string) error {
	if r.Plan.Captions != nil {
		if len(r.Artifact.CaptionFiles) != len(r.Plan.Captions.Tracks) {
			return fmt.Errorf("caption file set does not match frozen languages")
		}
		for _, track := range r.Plan.Captions.Tracks {
			name := "captions." + track.Language + ".vtt"
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return fmt.Errorf("caption file %s: %w", name, err)
			}
			if string(data) != track.VTT {
				return fmt.Errorf("caption integrity failed for %s revision %d", track.Language, track.Revision)
			}
		}
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, r.Artifact.CaptionFile))
	if err != nil {
		return err
	}
	if string(data) != r.Plan.VTT {
		return fmt.Errorf("caption integrity failed")
	}
	return nil
}

// Bundle exports the verified clean master, every sidecar, the optional burned
// derivative and the frozen manifest. Members are sorted so the same frozen
// render always produces the same archive ordering.
func (s *Service) Bundle(ctx context.Context, id string, dest io.Writer) error {
	if _, err := s.Verify(ctx, id); err != nil {
		return err
	}
	r, dir, err := s.Artifact(id)
	if err != nil {
		return err
	}
	names := []string{r.Artifact.Filename}
	if r.Artifact.BurnedFile != "" {
		names = append(names, r.Artifact.BurnedFile)
	}
	if len(r.Artifact.CaptionFiles) > 0 {
		names = append(names, r.Artifact.CaptionFiles...)
	} else {
		names = append(names, r.Artifact.CaptionFile)
	}
	sort.Strings(names)
	z := zip.NewWriter(dest)
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			z.Close()
			return err
		}
		w, err := z.Create(name)
		if err == nil {
			_, err = io.Copy(w, f)
		}
		f.Close()
		if err != nil {
			z.Close()
			return err
		}
	}
	manifest, err := json.MarshalIndent(bundleManifest(r), "", "  ")
	if err != nil {
		z.Close()
		return err
	}
	w, err := z.Create("composition.json")
	if err != nil {
		z.Close()
		return err
	}
	if _, err = w.Write(manifest); err != nil {
		z.Close()
		return err
	}
	return z.Close()
}

// bundleManifest is the same frozen render plus an explicit explanation of every
// readability finding that could not be fixed without changing the text.
func bundleManifest(r domain.Render) any {
	out := struct {
		domain.Render
		Report      []captionReport `json:"caption_report,omitempty"`
		Explanation string          `json:"caption_explanation,omitempty"`
	}{Render: r}
	if r.Plan.Captions != nil {
		out.Explanation = CaptionExplanation()
		for _, track := range r.Plan.Captions.Tracks {
			out.Report = append(out.Report, captionReport{Language: track.Language, Revision: track.Revision, Findings: track.Findings})
		}
	}
	return out
}

type captionReport struct {
	Language string           `json:"language"`
	Revision int              `json:"revision"`
	Findings []domain.Finding `json:"findings"`
}
