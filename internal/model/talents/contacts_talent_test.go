package talents

import (
	"strings"
	"testing"
)

// peopleTalentText returns the in-tree talents that teach how a contact
// name resolves (the contacts tree, notifications and the people
// trailhead), keyed by name, with whitespace collapsed so a phrase check
// does not depend on where the markdown wraps.
func peopleTalentText(t *testing.T) map[string]string {
	t.Helper()
	want := []string{"contacts", "contacts_lookup", "contacts_save", "contacts_vcf", "notifications", "people-trailhead"}
	out := make(map[string]string)
	for _, talent := range loadRepoTalents(t) {
		for _, name := range want {
			if talent.Name == name {
				out[name] = strings.Join(strings.Fields(talent.Content), " ")
			}
		}
	}
	for _, name := range want {
		if _, ok := out[name]; !ok {
			t.Fatalf("talent %q not loaded; the guard would be meaningless", name)
		}
	}
	return out
}

// TestPeopleTalentsTeachNameResolution pins how the talents teach name
// resolution: a formatted name or nickname first, then a given name or
// first word only when exactly one contact has it, never a note or
// summary, and an ambiguity that hands back contact_id values. It pins
// the fork remedy's warning that forgetting the only exact holder of a
// name leaves the name to that first-name rule, and that the lines
// teaching the old free-text fallback are gone.
func TestPeopleTalentsTeachNameResolution(t *testing.T) {
	text := peopleTalentText(t)
	present := []struct {
		name   string
		talent string
		want   string
	}{
		{"first names need a unique holder", "contacts_lookup", "the first word of its formatted name, and then exactly one contact must fit"},
		{"authority breaks no first-name tie", "contacts_lookup", "the lookup returns neither, whatever their zones"},
		{"ambiguity lists ids and fields", "contacts_lookup", "`contact_id`, and the field it matched"},
		{"ambiguity names the retry", "contacts_lookup", "Retry with the full formatted name of the one you mean"},
		{"a note does not make the person", "contacts_lookup", "never makes that contact the person"},
		{"query is the text door", "contacts_lookup", "It is the only lookup that reads those text fields"},
		{"routing shares the first-name rule", "contacts_save", "a first name two contacts share reaches neither"},
		{"descriptions reach no one", "contacts_save", "addressed by a description (\"the plumber\") reaches no one"},
		{"forget by ambiguous name removes nothing", "contacts_save", "resolves to neither and removes nothing"},
		{"forget result check", "contacts_save", "nickname or first-name match can land on a record you did not mean"},
		{"remedy spots the only exact holder", "contacts_save", "When the shared name resolves to the duplicate itself, the duplicate is the only contact holding that name exactly"},
		{"remedy warns of the first-name rule", "contacts_save", "Forgetting the duplicate leaves the name to the first-name rule"},
		{"remedy routes the nickname to the operator", "contacts_save", "so the operator can give it the name as a nickname through CardDAV"},
		{"remedy keeps custody of the nickname", "contacts_save", "set it yourself only when the operator asks in their own message"},
		{"dedup recipe points at the warning", "contacts_vcf", "Duplicates says what forgetting it does to that name"},
		{"recipients share the first-name rule", "notifications", "a first name two contacts share reaches neither"},
		{"recipients ignore free text", "notifications", "Notes, orgs, and AI summaries never resolve a recipient"},
		{"recipient retry", "notifications", "send again with the full formatted name of the one you mean"},
		{"trailhead: names only", "people-trailhead", "never through what a note says about someone"},
		{"trailhead: carry the id", "people-trailhead", "the error hands you each `contact_id`"},
	}
	for _, tt := range present {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(text[tt.talent], tt.want) {
				t.Errorf("talent %s must contain %q", tt.talent, tt.want)
			}
		})
	}

	absent := []struct {
		name string
		gone string
	}{
		{"lookup falls back to search", "fall back to a search"},
		{"routing searches the text", "search the text only when"},
		{"lookup reads no given name", "reads no given name"},
		{"recipients search free text", "text search over names, notes"},
		{"a description reaches the search", "reaches only the search"},
		{"search reaches through the note", "The text search also reaches a contact"},
		{"forget lands by search", "nickname or search match"},
	}
	for _, tt := range absent {
		t.Run(tt.name, func(t *testing.T) {
			for name, body := range text {
				if strings.Contains(body, tt.gone) {
					t.Errorf("talent %s still contains %q", name, tt.gone)
				}
			}
		})
	}
}
