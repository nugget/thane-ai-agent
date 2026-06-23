package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

func TestIsHAEntityNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"api 404", &homeassistant.APIError{StatusCode: 404}, true},
		{"api 404 wrapped", fmt.Errorf("get state: %w", &homeassistant.APIError{StatusCode: 404}), true},
		{"api 500", &homeassistant.APIError{StatusCode: 500}, false},
		{"registry not_found", errors.New("not_found: entity does not exist"), true},
		{"generic error", errors.New("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHAEntityNotFound(tt.err); got != tt.want {
				t.Errorf("IsHAEntityNotFound(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// fakeEntityLister implements EntityLister for suggestion tests without a
// live HA client.
type fakeEntityLister struct {
	entities []homeassistant.EntityInfo
	err      error
}

func (f *fakeEntityLister) GetEntities(_ context.Context, domain string) ([]homeassistant.EntityInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	if domain == "" {
		return f.entities, nil
	}
	var out []homeassistant.EntityInfo
	for _, e := range f.entities {
		if e.Domain == domain {
			out = append(out, e)
		}
	}
	return out, nil
}

func decodeNotFound(t *testing.T, raw string) EntityNotFoundResult {
	t.Helper()
	var got EntityNotFoundResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal not-found envelope: %v\n%s", err, raw)
	}
	return got
}

func TestSuggestEntityNotFound_SurfacesCandidates(t *testing.T) {
	lister := &fakeEntityLister{entities: []homeassistant.EntityInfo{
		{EntityID: "light.office_main", FriendlyName: "Office Main", Domain: "light"},
		{EntityID: "light.office_desk", FriendlyName: "Office Desk", Domain: "light"},
		{EntityID: "switch.coffee", FriendlyName: "Coffee", Domain: "switch"},
	}}

	got := decodeNotFound(t, SuggestEntityNotFound(context.Background(), lister, "light.office_mian"))

	if got.Found {
		t.Errorf("Found = true, want false")
	}
	if got.Reason != "not_found" {
		t.Errorf("Reason = %q, want not_found", got.Reason)
	}
	if got.RequestedEntityID != "light.office_mian" {
		t.Errorf("RequestedEntityID = %q", got.RequestedEntityID)
	}
	if len(got.Candidates) == 0 {
		t.Fatalf("expected candidates, got none")
	}
	// The office lights share the "office" token with the typo'd id, so
	// they must surface; the unrelated switch must not crowd them out of
	// the top slot.
	if got.Candidates[0].EntityID != "light.office_main" && got.Candidates[0].EntityID != "light.office_desk" {
		t.Errorf("top candidate = %q, want an office light", got.Candidates[0].EntityID)
	}
	if got.Note == "" {
		t.Errorf("Note should always be set")
	}
}

func TestSuggestEntityNotFound_NoSimilarEntities(t *testing.T) {
	lister := &fakeEntityLister{entities: []homeassistant.EntityInfo{
		{EntityID: "switch.coffee", FriendlyName: "Coffee", Domain: "switch"},
	}}

	got := decodeNotFound(t, SuggestEntityNotFound(context.Background(), lister, "lock.front_door"))

	if got.Found {
		t.Errorf("Found = true, want false")
	}
	if len(got.Candidates) != 0 {
		t.Errorf("expected no candidates, got %d", len(got.Candidates))
	}
	if got.Note == "" {
		t.Errorf("Note should guide discovery when nothing matches")
	}
}

func TestSuggestEntityNotFound_DiscoveryFailureDegrades(t *testing.T) {
	lister := &fakeEntityLister{err: errors.New("ha unreachable")}

	got := decodeNotFound(t, SuggestEntityNotFound(context.Background(), lister, "light.kitchen"))

	if got.Found {
		t.Errorf("Found = true, want false")
	}
	if len(got.Candidates) != 0 {
		t.Errorf("discovery failure should degrade to no candidates, got %d", len(got.Candidates))
	}
	if got.Note == "" {
		t.Errorf("Note should still be set on discovery failure")
	}
}
