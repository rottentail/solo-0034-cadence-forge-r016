package transport

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/service"
	"net/http"
	"strconv"
)

func (a API) captionGroup(w http.ResponseWriter, r *http.Request) {
	var input service.GroupInput
	if err := decode(w, r, &input); err != nil {
		failure(w, err)
		return
	}
	rev, err := expected(r)
	if err != nil {
		failure(w, err)
		return
	}
	v, err := a.Service.SaveCaptionGroup(r.PathValue("id"), rev, input)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Itoa(v.Revision))
	write(w, 201, v)
}

func (a API) captionGroupView(w http.ResponseWriter, r *http.Request) {
	revision := 0
	if q := r.URL.Query().Get("revision"); q != "" {
		var err error
		if revision, err = strconv.Atoi(q); err != nil || revision < 1 {
			failure(w, domain.Invalid("revision must be a positive integer"))
			return
		}
	}
	v, err := a.Service.CaptionGroupView(r.PathValue("id"), revision)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, v)
}

func (a API) captionTrack(w http.ResponseWriter, r *http.Request) {
	var input service.TrackInput
	if err := decode(w, r, &input); err != nil {
		failure(w, err)
		return
	}
	rev, err := expected(r)
	if err != nil {
		failure(w, err)
		return
	}
	v, err := a.Service.SaveCaptionTrack(r.PathValue("id"), r.PathValue("language"), rev, input)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Itoa(v.Revision))
	write(w, 201, v)
}

func (a API) captionTrackView(w http.ResponseWriter, r *http.Request) {
	revision := 0
	if q := r.URL.Query().Get("revision"); q != "" {
		var err error
		if revision, err = strconv.Atoi(q); err != nil || revision < 1 {
			failure(w, domain.Invalid("revision must be a positive integer"))
			return
		}
	}
	v, current, err := a.Service.CaptionTrackView(r.PathValue("id"), r.PathValue("language"), revision)
	if err != nil {
		failure(w, err)
		return
	}
	payload := map[string]any{"track": v.Track, "vtt": v.VTT, "findings": v.Findings, "referenced_by_latest_group": current}
	write(w, 200, payload)
}
