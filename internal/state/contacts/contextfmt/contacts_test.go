package contextfmt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormat_EmptyReturnsEmpty(t *testing.T) {
	if got := Format(nil); got != "" {
		t.Errorf("Format(nil) = %q, want empty", got)
	}
	if got := Format([]Match{}); got != "" {
		t.Errorf("Format([]) = %q, want empty", got)
	}
}

func TestFormat_HeadingPrecedesJSON(t *testing.T) {
	got := Format([]Match{
		{Name: "Alice", Score: 0.9},
	})
	if !strings.HasPrefix(got, "### Relevant Contacts\n\n") {
		t.Fatalf("output missing heading prefix\nGot:\n%s", got)
	}
}

func TestFormat_JSONIsParseable(t *testing.T) {
	got := Format([]Match{
		{
			Name:      "Alice Relevant",
			Org:       "TechCo",
			Summary:   "Works at TechCo",
			TrustZone: "trusted",
			Score:     0.92,
			Properties: []Property{
				{Kind: "EMAIL", Type: "INTERNET", Value: "alice@techco.com"},
				{Kind: "timezone", Value: "America/Chicago"},
			},
		},
	})
	body := strings.TrimPrefix(got, "### Relevant Contacts\n\n")

	var env struct {
		Contacts []Match `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("body not parseable JSON: %v\nBody: %s", err, body)
	}
	if len(env.Contacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(env.Contacts))
	}
	c := env.Contacts[0]
	if c.Name != "Alice Relevant" {
		t.Errorf("name = %q", c.Name)
	}
	if c.TrustZone != "trusted" {
		t.Errorf("trust_zone = %q", c.TrustZone)
	}
	if c.Score != 0.92 {
		t.Errorf("score = %f", c.Score)
	}
	if len(c.Properties) != 2 {
		t.Fatalf("properties len = %d", len(c.Properties))
	}
	if c.Properties[0].Type != "INTERNET" {
		t.Errorf("property[0].type = %q", c.Properties[0].Type)
	}
	if c.Properties[1].Type != "" {
		t.Errorf("property[1].type should omit when empty, got %q", c.Properties[1].Type)
	}
}

func TestFormat_OmitsEmptyOptionalFields(t *testing.T) {
	got := Format([]Match{
		{Name: "Minimal", Score: 0.5},
	})
	body := strings.TrimPrefix(got, "### Relevant Contacts\n\n")
	// Schema stability: required fields stay; optional empties drop.
	if !strings.Contains(body, `"name":"Minimal"`) {
		t.Errorf("name field missing\nBody: %s", body)
	}
	if !strings.Contains(body, `"score":0.5`) {
		t.Errorf("score field missing\nBody: %s", body)
	}
	for _, f := range []string{`"org":`, `"summary":`, `"trust_zone":`, `"properties":`} {
		if strings.Contains(body, f) {
			t.Errorf("expected %s to be omitted when empty\nBody: %s", f, body)
		}
	}
}
