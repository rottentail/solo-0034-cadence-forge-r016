package transport

import (
	"cadenceforge/internal/service"
	"net/http"
)

type API struct{ Service *service.Service }

func Handler(s *service.Service) http.Handler {
	a := API{Service: s}
	m := http.NewServeMux()
	m.HandleFunc("GET /forge/v1/health", a.health)
	m.HandleFunc("GET /forge/v1/state", a.state)
	m.HandleFunc("POST /forge/v1/sources", a.ingest)
	m.HandleFunc("DELETE /forge/v1/sources/{id}", a.retire)
	m.HandleFunc("POST /forge/v1/sources/{id}/loudness", a.loudness)
	m.HandleFunc("GET /forge/v1/sources/{id}/visual", a.visual)
	m.HandleFunc("POST /forge/v1/sheets", a.sheet)
	m.HandleFunc("PUT /forge/v1/sheets/{id}", a.sheet)
	m.HandleFunc("POST /forge/v1/recipes", a.recipe)
	m.HandleFunc("PUT /forge/v1/recipes/{id}", a.recipe)
	m.HandleFunc("GET /forge/v1/sheets/{id}/preview", a.preview)
	m.HandleFunc("POST /forge/v1/caption-groups", a.captionGroup)
	m.HandleFunc("PUT /forge/v1/caption-groups/{id}", a.captionGroup)
	m.HandleFunc("GET /forge/v1/caption-groups/{id}", a.captionGroupView)
	m.HandleFunc("PUT /forge/v1/caption-groups/{id}/languages/{language}", a.captionTrack)
	m.HandleFunc("GET /forge/v1/caption-groups/{id}/languages/{language}", a.captionTrackView)
	m.HandleFunc("POST /forge/v1/renders", a.submit)
	m.HandleFunc("GET /forge/v1/renders/{id}", a.render)
	m.HandleFunc("POST /forge/v1/renders/{id}/cancel", a.cancel)
	m.HandleFunc("POST /forge/v1/renders/{id}/retry", a.retry)
	m.HandleFunc("POST /forge/v1/renders/{id}/verify", a.verify)
	m.HandleFunc("GET /forge/v1/renders/{id}/artifact", a.artifact)
	m.HandleFunc("GET /forge/v1/renders/{id}/bundle", a.bundle)
	m.HandleFunc("GET /forge/v1/events", a.events)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		m.ServeHTTP(w, r)
	})
}
