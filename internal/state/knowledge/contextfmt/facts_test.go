package contextfmt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatSimilarity_EmptyReturnsEmpty(t *testing.T) {
	if got := FormatSimilarity(nil); got != "" {
		t.Errorf("FormatSimilarity(nil) = %q, want empty", got)
	}
	if got := FormatSimilarity([]SimilarityFact{}); got != "" {
		t.Errorf("FormatSimilarity([]) = %q, want empty", got)
	}
}

func TestFormatSimilarity_HeadingPrecedesJSON(t *testing.T) {
	got := FormatSimilarity([]SimilarityFact{
		{Category: "device", Key: "office_light", Value: "Hue desk lamp", Score: 0.85},
	})
	if !strings.HasPrefix(got, "### Relevant Facts\n\n") {
		t.Fatalf("output missing heading prefix\nGot:\n%s", got)
	}
	body := strings.TrimPrefix(got, "### Relevant Facts\n\n")
	var env struct {
		Facts []SimilarityFact `json:"facts"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("body not parseable JSON: %v\nBody: %s", err, body)
	}
	if len(env.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(env.Facts))
	}
	if env.Facts[0].Score != 0.85 {
		t.Errorf("score = %f, want 0.85", env.Facts[0].Score)
	}
}

func TestFormatSubjectKeyed_EmptyReturnsEmpty(t *testing.T) {
	if got := FormatSubjectKeyed(nil); got != "" {
		t.Errorf("FormatSubjectKeyed(nil) = %q, want empty", got)
	}
	if got := FormatSubjectKeyed([]SubjectFact{}); got != "" {
		t.Errorf("FormatSubjectKeyed([]) = %q, want empty", got)
	}
}

func TestFormatSubjectKeyed_RendersFields(t *testing.T) {
	got := FormatSubjectKeyed([]SubjectFact{
		{
			Category: "device",
			Key:      "office_light",
			Value:    "Hue desk lamp",
			Subjects: []string{"entity:light.office", "zone:office"},
			Ref:      "devices/office_light.md",
		},
		{
			Category: "home",
			Key:      "house_layout",
			Value:    "Two-story craftsman",
		},
	})
	if !strings.HasPrefix(got, "### Subject-Keyed Facts\n\n") {
		t.Fatalf("output missing heading prefix\nGot:\n%s", got)
	}
	body := strings.TrimPrefix(got, "### Subject-Keyed Facts\n\n")
	var env struct {
		Facts []SubjectFact `json:"facts"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("body not parseable JSON: %v\nBody: %s", err, body)
	}
	if len(env.Facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(env.Facts))
	}
	if env.Facts[0].Ref != "devices/office_light.md" {
		t.Errorf("fact[0].ref = %q", env.Facts[0].Ref)
	}
	// Schema stability: a fact without Subjects/Ref drops those fields.
	if !strings.Contains(body, `"category":"home"`) {
		t.Errorf("home fact missing category\nBody: %s", body)
	}
	if strings.Contains(body, `"subjects":null`) || strings.Contains(body, `"subjects":[]`) {
		t.Errorf("empty subjects should be omitted, not present as null/empty\nBody: %s", body)
	}
}
