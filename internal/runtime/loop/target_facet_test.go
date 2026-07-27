package loop

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/outputtargets"
)

const testTargetID = "apple_watch.rectangular"

// targetedOutput declares status_line plus one target facet, the shape a
// loop takes when it feeds both a reader and a rendering surface.
func targetedOutput(targets ...string) OutputSpec {
	facets := []FacetSpec{{Name: OutputFacetStatusLine}}
	for _, id := range targets {
		facets = append(facets, FacetSpec{Target: id})
	}
	return OutputSpec{
		Name:   "office status",
		Type:   OutputTypeMaintainedDocument,
		Ref:    "core:office.md",
		Facets: facets,
	}
}

func TestTargetFacetTakesItsFieldFromTheRegistry(t *testing.T) {
	target, ok := outputtargets.Lookup(testTargetID)
	if !ok {
		t.Fatalf("Lookup(%q) failed; the test target is no longer registered", testTargetID)
	}

	fields := targetedOutput(testTargetID).FacetFields()
	var got FacetField
	for _, field := range fields {
		if field.Target == testTargetID {
			got = field
		}
	}
	if got.Key != target.ArgKey() {
		t.Fatalf("target field key = %q, want the registry's arg key %q", got.Key, target.ArgKey())
	}
	if got.Format != FacetFormatJSON {
		t.Errorf("target field format = %q, want json — its value is a slot object", got.Format)
	}
	// A target facet carries no rune budget of its own: the slots have
	// their own, and one on the encoded object would be a second limit
	// nothing agreed to.
	if got.MaxRunes != 0 {
		t.Errorf("target field MaxRunes = %d, want 0", got.MaxRunes)
	}
}

func TestTargetFacetSectionsSitAheadOfTheBody(t *testing.T) {
	out := targetedOutput("apple_watch.circular", "apple_watch.rectangular")
	keys := make([]string, 0, 4)
	for _, field := range out.FacetFields() {
		keys = append(keys, field.Key)
	}
	// Sorted by target ID, not by declaration order, and always before
	// the document's substance.
	want := []string{"status_line", "apple_watch_circular", "apple_watch_rectangular", "full"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("field order = %v, want %v", keys, want)
	}
}

func TestTargetFacetRoundTripsThroughTheDocument(t *testing.T) {
	out := targetedOutput(testTargetID)
	payload := FacetPayload{
		StatusLine: "Printer online; 2 packages waiting.",
		Full:       "# Office\n\nDetail.",
		Targets: map[string]string{
			testTargetID: `{"value":"2 pkg","title":"Office","fraction":0.5}`,
		},
	}
	if err := out.ValidateFacetPayload(payload); err != nil {
		t.Fatalf("payload should be publishable: %v", err)
	}

	body := out.RenderFacetDocument(payload)
	if !strings.Contains(body, "## Apple Watch rectangular complication") {
		t.Fatalf("target section missing its heading:\n%s", body)
	}
	if !strings.Contains(body, "```json") {
		t.Fatalf("target section is not fenced:\n%s", body)
	}

	got := out.ParseFacetDocument(body)
	if !reflect.DeepEqual(got, payload) {
		t.Fatalf("round trip changed the payload:\ngot  %#v\nwant %#v", got, payload)
	}
}

// TestTargetFacetPayloadIsCheckedAgainstTheRegistry is the point of
// declaring a target rather than a bare json facet: the registry's slot
// rules apply to the stored value, not only to the tool call that
// produced it.
func TestTargetFacetPayloadIsCheckedAgainstTheRegistry(t *testing.T) {
	tests := []struct {
		name    string
		slots   string
		wantErr string
	}{
		{
			name:    "over-budget slot",
			slots:   `{"value":"a very long headline that will not fit"}`,
			wantErr: "renders at most 12",
		},
		{
			name:    "unknown slot",
			slots:   `{"value":"ok","headline":"nope"}`,
			wantErr: "has no slot named",
		},
		{
			name:    "missing required slot",
			slots:   `{"title":"Office"}`,
			wantErr: `slot "value" is required`,
		},
		{
			name:    "fraction out of range",
			slots:   `{"value":"ok","fraction":4}`,
			wantErr: "between 0.0 and 1.0",
		},
		{
			name:    "malformed color",
			slots:   `{"value":"ok","gauge_color":"greenish"}`,
			wantErr: "six-digit hex",
		},
		{
			name:    "not an object",
			slots:   `["value"]`,
			wantErr: "must be a JSON object of slot values",
		},
		{
			name:    "not json at all",
			slots:   `2 packages waiting`,
			wantErr: "not valid JSON",
		},
	}

	out := targetedOutput(testTargetID)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := out.ValidateFacetPayload(FacetPayload{
				StatusLine: "All clear.",
				Full:       "Body.",
				Targets:    map[string]string{testTargetID: tt.slots},
			})
			if err == nil {
				t.Fatalf("ValidateFacetPayload() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
			// Whatever went wrong, the model has to know which argument
			// to fix; a slot-level message with no field name does not
			// say that.
			if !strings.Contains(err.Error(), "apple_watch_rectangular") {
				t.Errorf("error = %v, want it to name the publish argument", err)
			}
		})
	}
}

func TestValidateOutputFacetsOnTargets(t *testing.T) {
	tests := []struct {
		name    string
		facets  []FacetSpec
		wantErr string
	}{
		{
			name:   "status line plus a target",
			facets: []FacetSpec{{Name: OutputFacetStatusLine}, {Target: testTargetID}},
		},
		{
			name:    "unknown target",
			facets:  []FacetSpec{{Name: OutputFacetStatusLine}, {Target: "casio.f91w"}},
			wantErr: "unknown target",
		},
		{
			name:    "name and target together",
			facets:  []FacetSpec{{Name: OutputFacetStatusLine}, {Name: OutputFacetDigest, Target: testTargetID}},
			wantErr: "declares both name",
		},
		{
			name:    "non-json format on a target",
			facets:  []FacetSpec{{Name: OutputFacetStatusLine}, {Target: testTargetID, Format: FacetFormatPlain}},
			wantErr: "always \"json\"",
		},
		{
			name:   "json format on a target is redundant but honest",
			facets: []FacetSpec{{Name: OutputFacetStatusLine}, {Target: testTargetID, Format: FacetFormatJSON}},
		},
		{
			name:    "the same target twice",
			facets:  []FacetSpec{{Name: OutputFacetStatusLine}, {Target: testTargetID}, {Target: testTargetID}},
			wantErr: "duplicate target",
		},
		{
			name:   "two different targets",
			facets: []FacetSpec{{Name: OutputFacetStatusLine}, {Target: testTargetID}, {Target: "apple_watch.circular"}},
		},
		{
			name:    "a target does not satisfy the status line requirement",
			facets:  []FacetSpec{{Target: testTargetID}},
			wantErr: "must include \"status_line\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := OutputSpec{Name: "office status", Type: OutputTypeMaintainedDocument, Ref: "core:office.md", Facets: tt.facets}
			err := validateOutputFacets(out)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateOutputFacets() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateOutputFacets() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestTargetFacetSurvivesSpecSerialization guards the declaration itself:
// a spec is stored as JSON, so a target that does not survive the round
// trip would come back as a facet with no identity.
func TestTargetFacetSurvivesSpecSerialization(t *testing.T) {
	original := targetedOutput(testTargetID)
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded OutputSpec
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded.Facets, original.Facets) {
		t.Fatalf("facets did not survive:\ngot  %#v\nwant %#v\nwire: %s", decoded.Facets, original.Facets, encoded)
	}
	// The short form is for a bare name; a target has to keep its object.
	if !strings.Contains(string(encoded), `"target":"`+testTargetID+`"`) {
		t.Errorf("encoded spec lost the target field: %s", encoded)
	}
}

// TestNoTargetTitleCollidesWithAReservedHeading protects the parser: a
// target section is found by its title, so a title equal to one of the
// contract's own headings would make two sections indistinguishable and
// silently merge their content.
func TestNoTargetTitleCollidesWithAReservedHeading(t *testing.T) {
	reserved := make([]string, 0, len(readingSections)+1)
	for _, section := range readingSections {
		reserved = append(reserved, section.Heading)
	}
	reserved = append(reserved, fullSection.Heading)

	for _, target := range outputtargets.All() {
		for _, heading := range reserved {
			if strings.EqualFold(target.Title, heading) {
				t.Errorf("target %q is titled %q, which collides with the reserved section heading %q", target.ID, target.Title, heading)
			}
		}
	}
}
