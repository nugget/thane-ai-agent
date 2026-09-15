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
		{"standing decides between exact holders", "contacts_lookup", "standing decides: the operator's own contact first, then a contact above `known`, then a `known` one"},
		{"an exact tie at one standing returns none", "contacts_lookup", "Two or more at the same standing are a tie, and the lookup returns none of them"},
		{"a formatted name does not beat a nickname", "contacts_lookup", "Holding the name as a formatted name does not beat holding it as a nickname, and no ID breaks the tie"},
		{"a tie is not settled by a first name", "contacts_lookup", "the lookup tries no given name or first word after a tie"},
		{"a tied formatted name needs its id", "contacts_lookup", "then only its `contact_id` tells it apart"},
		{"first names need a unique holder", "contacts_lookup", "the first word of its formatted name, and then exactly one contact must fit"},
		{"authority breaks no first-name tie", "contacts_lookup", "the lookup returns neither, whatever their zones"},
		{"ambiguity lists ids and fields", "contacts_lookup", "`contact_id`, and the field it matched"},
		{"ambiguity names the retry", "contacts_lookup", "Retry with the full formatted name of the one you mean"},
		{"a note does not make the person", "contacts_lookup", "never makes that contact the person"},
		{"query is the text door", "contacts_lookup", "It is the only lookup that reads those text fields"},
		{"ambiguity lists at most five", "contacts_lookup", "The error lists up to five of them"},
		{"query reads given names", "contacts_lookup", "`nickname`, `given_name`, `note`"},
		{"query lists name holders first", "contacts_lookup", "those whose formatted name, nickname, given name, or first word the query is listed first"},
		{"query says when it stops", "contacts_lookup", "Returns up to 50 matching contacts"},
		{"query stops only when more match", "contacts_lookup", "says so when more match than it lists"},
		{"ambiguity points at query for the rest", "contacts_lookup", "`query` set to that name lists every contact that fits ahead of any other match"},
		{"ambiguity: query rows carry ids", "contacts_lookup", "ahead of any other match, each with its `contact_id`, so it reaches the ones the error left out while no more than 50 share the name"},
		{"query rows carry ids", "contacts_lookup", "Each row carries the contact's `contact_id` and trust zone"},
		{"query stays within its budget", "contacts_lookup", "rows past the limit are left off"},
		{"a key/value list stays within its budget", "contacts_lookup", "within the same 16 KB as a query"},
		{"a browse list stays within its budget", "contacts_lookup", "stays within 16 KB as a query's does"},
		{"routing shares the first-name rule", "contacts_save", "a first name two contacts share reaches neither"},
		{"routing reaches no tied holder", "contacts_save", "and none of two or more at the same standing"},
		{"a same-standing nickname splits both", "contacts_save", "a second contact at the same standing holding it exactly leaves the name reaching neither"},
		{"check a nickname before giving it", "contacts_save", "check with `contact_lookup` that no contact at its standing already goes by it"},
		{"check a new contact's name too", "contacts_save", "Before giving a new contact its name, or any contact a nickname"},
		{"a new contact's name ties with a nickname", "contacts_save", "saving someone under a name a `known` contact has as its nickname creates a second contact, and the name then reaches neither"},
		{"a shared name reaches neither at one standing", "contacts_save", "A name lookup reaches only the one with more standing (the operator's own, then one above `known`), and neither when they stand alike"},
		{"contact_owner reports a tied legacy name", "contacts_lookup", "`contact_owner` returns an error listing them with their `contact_id`s, not a record"},
		{"a tied legacy name is the operator's to fix", "contacts_lookup", "Don't try to settle it with those tools; tell the operator to set `identity.operator_contact_id`"},
		{"an authority first name is custodied", "contacts_save", "Outside the operator's own message, `contact_save` also refuses a new contact's name, or any contact's nickname, that is the given name or the first word of the formatted name of an admin, household, trusted, or operator contact"},
		{"a first-name takeover is why", "contacts_save", "A formatted name or nickname is found before any first name"},
		{"the operator's message lifts the first-name rule", "contacts_save", "if the operator wants the other contact to go by that name, they say so in their own message"},
		{"a custodied given name follows the target rule", "contacts_save", "**Changing the nickname or given name** of a contact above `known`"},
		{"a given name is why", "contacts_save", "rewriting it would free that person's first name for another contact"},
		{"a shared first name can still be saved", "contacts_save", "nothing stops a second contact with the same first name from being saved later"},
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
		{"recipients: a tie reaches no one", "notifications", "two or more at the same standing are a tie that reaches none of them"},
		{"recipients: a tied formatted name goes to the operator", "notifications", "unless a tie leaves it no other name, and then ask the operator which contact should keep the name"},
		{"recipients: the list is bounded", "notifications", "The error lists up to five of the contacts that share it"},
		{"recipients: query finds the rest", "notifications", "`contact_lookup` with that first name as `query` lists ahead of any other match while no more than 50 share it"},
		{"trailhead: names only", "people-trailhead", "never through what a note says about someone"},
		{"trailhead: the most standing needs no error", "people-trailhead", "the one with the most standing is chosen without an error"},
		{"trailhead: a tie at one standing is an error", "people-trailhead", "Two or more at the same standing are an error"},
		{"trailhead: so is a shared first name", "people-trailhead", "and so is a name no contact holds that way that several have as a given name or first word"},
		{"trailhead: carry the id", "people-trailhead", "the error lists up to five of them with their `contact_id`"},
		{"trailhead: query lists the rest while they fit", "people-trailhead", "lists every one of them, each with its `contact_id`, ahead of any other match while no more than 50 share it"},
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
		{"the ambiguity lists every candidate", "The error lists each with"},
		{"the error hands over every id", "hands you each `contact_id`"},
		{"forget lists every id", "the error lists each `contact_id`"},
		{"query stops at exactly 50", "says so when it stops at 50"},
		{"query lists the rest without a bound", "name as `query` lists the rest"},
		{"a formatted name breaks a tie", "formatted-name match before a"},
		{"the lowest id breaks a tie", "then the lowest ID"},
		{"any exact holder is chosen without an error", "one is chosen without an error"},
		{"only the short-form case is an error", "A name is an error only when no contact holds it that way"},
		{"a known duplicate takes the notifications", "between two `known` contacts it can take their"},
		{"a shared name always reaches one", "reaches only one of them"},
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

// TestContactsTalentsTeachDossierCitations pins how the contacts
// talents teach archive-session citations in a dossier: the whole id in
// the colon form, the hyphen spelling repaired in Go, a leading part
// refused with its resolution rather than completed, content search to
// choose among candidates, and Open Questions (never prose about a
// prefix) for evidence that cannot be pinned to one session. It pins the
// same rule where a duplicate's dossier is folded into the survivor,
// because that fold is where inherited prefixes arrive.
func TestContactsTalentsTeachDossierCitations(t *testing.T) {
	text := peopleTalentText(t)
	for _, tt := range []struct {
		name   string
		talent string
		want   string
	}{
		{"whole id in the colon form", "contacts", "Archive evidence cites the whole session id, `archive:session:<full-session-uuid>`"},
		{"hyphen spelling is repaired", "contacts", "rewritten for you and listed under `canonicalized_citations`"},
		{"a leading part is not completed", "contacts", "A leading part is refused, never completed silently"},
		{"the refusal resolves each case", "contacts", "names its full citation when one session matches, lists the candidates when several share it, and says so when none does"},
		{"content search chooses", "contacts", "searching `archive_search` for the claim's own words, since every hit carries its full `session_id`"},
		{"unpinned evidence has a home", "contacts", "Evidence you cannot pin to one session goes under Open Questions, never into prose that describes a prefix"},
		{"folding carries citations whole", "contacts_save", "Carry each citation over whole"},
		{"folding resolves inherited prefixes", "contacts_save", "copy the full citation it names, or move the claim to Open Questions, rather than rewording the citation into prose"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(text[tt.talent], tt.want) {
				t.Errorf("talent %s must contain %q", tt.talent, tt.want)
			}
		})
	}
}
