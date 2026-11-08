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

// TestJSONStatusLineIsBudgetedInCharacters pins that a JSON-format
// status_line drops the word limit, which means nothing for code-read JSON,
// and is told and held to its character limit instead: spaced JSON past
// twenty whitespace tokens fits, and one character past the limit does not.
func TestJSONStatusLineIsBudgetedInCharacters(t *testing.T) {
	contract := Contract{Facets: []Spec{{Name: StatusLine, Format: FormatJSON}}}
	field := contract.Fields()[0]
	if field.MaxWords != 0 || field.Budget() != "180 characters" || field.BudgetClause() != " Maximum 180 characters — a ceiling, not a target; compose comfortably under it." {
		t.Fatalf("json status_line field = %+v, budget %q; want no word limit and 180 characters", field, field.Budget())
	}
	spaced := `{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8, "i": 9, "j": 10, "k": 11}`
	if WordCount(spaced) <= statusLineMaxWords {
		t.Fatalf("%q is not past the word limit", spaced)
	}
	if err := contract.Validate(Payload{StatusLine: spaced, Full: "# x\n"}); err != nil {
		t.Errorf("Validate() refused spaced JSON within its characters: %v", err)
	}
	long := `{"reading":"` + strings.Repeat("x", statusLineMaxRunes-13) + `"}`
	err := contract.Validate(Payload{StatusLine: long, Full: "# x\n"})
	if err == nil || !strings.Contains(err.Error(), "status_line is 181 characters and the limit is 180") {
		t.Errorf("Validate() = %v, want the 181-character JSON refused in characters", err)
	}
}
