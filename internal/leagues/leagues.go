// Package leagues holds the league registry shared by the seeder and the API.
// leagues.json is the single source of truth for which Cricsheet competitions
// are loaded and which league code each match belongs to.
package leagues

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed leagues.json
var leaguesJSON []byte

type League struct {
	Code        string            `json:"code"`
	Name        string            `json:"name"`
	Gender      string            `json:"gender"`
	Source      string            `json:"source"`
	Events      []string          `json:"events"`
	TeamRenames map[string]string `json:"teamRenames,omitempty"`
	Visible     bool              `json:"visible"`
}

type Registry struct {
	Leagues []League `json:"leagues"`

	byCode  map[string]League
	byEvent map[eventKey]League
}

type eventKey struct {
	event  string
	gender string
}

// Load parses the embedded leagues.json and validates it.
func Load() (*Registry, error) {
	var r Registry
	if err := json.Unmarshal(leaguesJSON, &r); err != nil {
		return nil, fmt.Errorf("parse leagues.json: %w", err)
	}

	r.byCode = make(map[string]League, len(r.Leagues))
	r.byEvent = make(map[eventKey]League)

	for _, l := range r.Leagues {
		if l.Code == "" || l.Source == "" || len(l.Events) == 0 {
			return nil, fmt.Errorf("league %q: code, source and events are required", l.Code)
		}
		if l.Gender != "male" && l.Gender != "female" {
			return nil, fmt.Errorf("league %s: gender must be male or female, got %q", l.Code, l.Gender)
		}
		if _, dup := r.byCode[l.Code]; dup {
			return nil, fmt.Errorf("duplicate league code %s", l.Code)
		}
		r.byCode[l.Code] = l

		for _, e := range l.Events {
			k := eventKey{event: e, gender: l.Gender}
			if other, dup := r.byEvent[k]; dup {
				return nil, fmt.Errorf("event %q (%s) mapped to both %s and %s", e, l.Gender, other.Code, l.Code)
			}
			r.byEvent[k] = l
		}
	}

	return &r, nil
}

// Resolve returns the league for a match from its Cricsheet event and gender.
func (r *Registry) Resolve(event, gender string) (League, bool) {
	l, ok := r.byEvent[eventKey{event: event, gender: gender}]
	return l, ok
}

func (r *Registry) Get(code string) (League, bool) {
	l, ok := r.byCode[strings.ToUpper(code)]
	return l, ok
}

func (r *Registry) Codes() []string {
	codes := make([]string, 0, len(r.Leagues))
	for _, l := range r.Leagues {
		codes = append(codes, l.Code)
	}
	return codes
}

// Sources returns the distinct Cricsheet archive directories, in config order.
// Several leagues can share one source (e.g. ssm_csv2 holds SSM and WSSM).
func (r *Registry) Sources() []string {
	seen := make(map[string]bool)
	var sources []string
	for _, l := range r.Leagues {
		if !seen[l.Source] {
			seen[l.Source] = true
			sources = append(sources, l.Source)
		}
	}
	return sources
}

// SourcesFor returns the distinct sources needed to load the given league codes.
func (r *Registry) SourcesFor(codes []string) ([]string, error) {
	seen := make(map[string]bool)
	var sources []string
	for _, c := range codes {
		l, ok := r.Get(c)
		if !ok {
			valid := r.Codes()
			sort.Strings(valid)
			return nil, fmt.Errorf("unknown league %q (valid: %s)", c, strings.Join(valid, ", "))
		}
		if !seen[l.Source] {
			seen[l.Source] = true
			sources = append(sources, l.Source)
		}
	}
	return sources, nil
}
