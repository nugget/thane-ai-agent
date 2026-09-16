package facets

import (
	"strings"
	"testing"
)

func jsonDigestContract() Contract {
	return Contract{Facets: []Spec{
		{Name: StatusLine},
		{Name: Digest, Format: FormatJSON},
	}}
}

// TestJSONFacetRejectsProse pins the guarantee a json projection makes.
// A consumer that asked for json and received prose has no recourse at
// read time, so the refusal has to happen at publish.
func TestJSONFacetRejectsProse(t *testing.T) {
	err := jsonDigestContract().Validate(Payload{
		StatusLine: "nugget home since 09:14",
		Digest:     "He has been home all morning.",
		Full:       "# Nugget\n\nHome.\n",
	})
	if err == nil {
		t.Fatal("Validate() accepted prose in a json projection")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("error does not name the cause: %v", err)
	}
	if !strings.Contains(err.Error(), string(Digest)) {
		t.Errorf("error does not name the offending projection: %v", err)
	}
}

// TestJSONFacetAcceptsJSON covers the other side, and that the sibling
// markdown projection is unaffected by its neighbour's format.
func TestJSONFacetAcceptsJSON(t *testing.T) {
	if err := jsonDigestContract().Validate(Payload{
		StatusLine: "nugget home since 09:14",
		Digest:     `{"reading":"home","expires_at":"2026-09-05T18:00:00Z"}`,
		Full:       "# Nugget\n\nHome.\n",
	}); err != nil {
		t.Fatalf("Validate() rejected valid JSON: %v", err)
	}
}

// TestJSONFacetStillHonorsTheOtherBudgets guards against the format
// check short-circuiting the rest: a json value is still rune-capped and
// still single-line where the projection is.
func TestJSONFacetStillHonorsTheOtherBudgets(t *testing.T) {
	long := `{"reading":"` + strings.Repeat("x", statusLineMaxRunes) + `"}`
	err := Contract{Facets: []Spec{{Name: StatusLine, Format: FormatJSON}}}.Validate(Payload{
		StatusLine: long,
		Full:       "# x\n",
	})
	if err == nil {
		t.Fatal("Validate() accepted an over-budget json status_line")
	}
	if !strings.Contains(err.Error(), "the limit is") {
		t.Errorf("error does not report the budget: %v", err)
	}
}
