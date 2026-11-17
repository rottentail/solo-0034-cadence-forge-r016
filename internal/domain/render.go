package domain

import (
	"encoding/json"

	"time"
)

type Attempt struct {
	Number     int        `json:"number"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Outcome    string     `json:"outcome"`
	Detail     string     `json:"detail,omitempty"`
}
type Inspection struct {
	Media           Media  `json:"media"`
	Digest          string `json:"digest"`
	Bytes           int64  `json:"bytes"`
	DurationDeltaMS int64  `json:"duration_delta_ms"`
}
type Artifact struct {
	Filename         string      `json:"filename"`
	CaptionFile      string      `json:"caption_file"`
	CaptionFiles     []string    `json:"caption_files,omitempty"`
	BurnedFile       string      `json:"burned_file,omitempty"`
	BurnedLanguages  []string    `json:"burned_languages,omitempty"`
	Inspection       Inspection  `json:"inspection"`
	BurnedInspection *Inspection `json:"burned_inspection,omitempty"`
}
type Render struct {
	ID          string            `json:"id"`
	Key         string            `json:"key"`
	RequestHash string            `json:"request_hash"`
	Sheet       Sheet             `json:"sheet"`
	Recipe      Recipe            `json:"recipe"`
	Sources     map[string]Source `json:"sources"`
	Plan        Plan              `json:"plan"`
	State       string            `json:"state"`
	Attempts    []Attempt         `json:"attempts"`
	Artifact    *Artifact         `json:"artifact,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}
type Event struct {
	Sequence uint64    `json:"sequence"`
	Subject  string    `json:"subject"`
	Kind     string    `json:"kind"`
	Detail   string    `json:"detail"`
	At       time.Time `json:"at"`
}
type State struct {
	Schema        int                                  `json:"schema"`
	Sequence      uint64                               `json:"sequence"`
	Sources       map[string]Source                    `json:"sources"`
	Sheets        map[string][]Sheet                   `json:"sheets"`
	Recipes       map[string][]Recipe                  `json:"recipes"`
	CaptionGroups map[string][]CaptionGroup            `json:"caption_groups"`
	CaptionTracks map[string]map[string][]CaptionTrack `json:"caption_tracks"`
	Renders       map[string]Render                    `json:"renders"`
	Events        []Event                              `json:"events"`
}

func Empty() State {
	return State{Schema: 1, Sources: map[string]Source{}, Sheets: map[string][]Sheet{}, Recipes: map[string][]Recipe{}, CaptionGroups: map[string][]CaptionGroup{}, CaptionTracks: map[string]map[string][]CaptionTrack{}, Renders: map[string]Render{}, Events: []Event{}}
}
func (s *State) UnmarshalJSON(data []byte) error {
	type shadow struct {
		Schema        int                                  `json:"schema"`
		Sequence      uint64                               `json:"sequence"`
		Sources       map[string]Source                    `json:"sources"`
		Sheets        map[string][]Sheet                   `json:"sheets"`
		Recipes       map[string][]Recipe                  `json:"recipes"`
		CaptionGroups map[string][]CaptionGroup            `json:"caption_groups"`
		CaptionTracks map[string]map[string][]CaptionTrack `json:"caption_tracks"`
		Renders       map[string]Render                    `json:"renders"`
		Events        []Event                              `json:"events"`
	}
	var aux shadow
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*s = State{Schema: aux.Schema, Sequence: aux.Sequence, Sources: aux.Sources, Sheets: aux.Sheets, Recipes: aux.Recipes, CaptionGroups: aux.CaptionGroups, CaptionTracks: aux.CaptionTracks, Renders: aux.Renders, Events: aux.Events}
	if s.Sources == nil {
		s.Sources = map[string]Source{}
	}
	if s.Sheets == nil {
		s.Sheets = map[string][]Sheet{}
	}
	if s.Recipes == nil {
		s.Recipes = map[string][]Recipe{}
	}
	if s.CaptionGroups == nil {
		s.CaptionGroups = map[string][]CaptionGroup{}
	}
	if s.CaptionTracks == nil {
		s.CaptionTracks = map[string]map[string][]CaptionTrack{}
	}
	if s.Renders == nil {
		s.Renders = map[string]Render{}
	}
	if s.Events == nil {
		s.Events = []Event{}
	}
	return nil
}

// GroupRevision resolves one group revision. revision 0 means the latest.
func (s State) GroupRevision(id string, revision int) (CaptionGroup, error) {
	versions := s.CaptionGroups[id]
	if len(versions) == 0 {
		return CaptionGroup{}, Missing("caption group", id)
	}
	if revision == 0 {
		revision = len(versions)
	}
	if revision < 1 || revision > len(versions) {
		return CaptionGroup{}, Missing("caption group revision", id)
	}
	return versions[revision-1], nil
}
func (s State) LatestGroupRevision(id string) (CaptionGroup, int, error) {
	versions := s.CaptionGroups[id]
	if len(versions) == 0 {
		return CaptionGroup{}, 0, Missing("caption group", id)
	}
	return versions[len(versions)-1], len(versions), nil
}
func (s State) Track(id, language string) ([]CaptionTrack, error) {
	byLanguage, ok := s.CaptionTracks[id]
	if !ok {
		return nil, Missing("caption group", id)
	}
	versions := byLanguage[language]
	if len(versions) == 0 {
		return nil, Missing("caption track", id+"/"+language)
	}
	return versions, nil
}
func (s *State) PutTrack(track CaptionTrack) {
	byLanguage, ok := s.CaptionTracks[track.GroupID]
	if !ok {
		byLanguage = map[string][]CaptionTrack{}
		s.CaptionTracks[track.GroupID] = byLanguage
	}
	byLanguage[track.Language] = append(byLanguage[track.Language], track)
}
func (s *State) Emit(id, kind, detail string) {
	s.Sequence++
	s.Events = append(s.Events, Event{Sequence: s.Sequence, Subject: id, Kind: kind, Detail: detail, At: time.Now().UTC()})
}
func Terminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}
