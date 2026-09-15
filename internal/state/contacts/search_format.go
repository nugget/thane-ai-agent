package contacts

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// A contact list result is model-facing, and nothing bounds the length
// of a contact's name, organization or AI summary, so every list of
// contacts a tool returns (contact_lookup's query, kind and key/value
// results, and contact_list's) holds to the 16 KB ceiling the
// repository's other search results hold to (the document tools' and
// email's). A long field is cut on a rune boundary and ends with
// searchCutMarker, as email cuts one, and the rows that would pass the
// ceiling are left off the end of the list and counted. A listed row is
// never cut short, so a query row's contact_id and trust zone are always
// whole.
const (
	// searchResultMaxBytes bounds the whole result, notes included.
	searchResultMaxBytes = 16 << 10

	// searchFieldMaxBytes bounds a row's formatted name and organization,
	// and the query wherever the result quotes it, marker included.
	searchFieldMaxBytes = 256

	// searchSummaryMaxBytes bounds a row's AI summary, marker included.
	searchSummaryMaxBytes = 512

	// searchCutMarker ends a field cut to its bound.
	searchCutMarker = "…[cut]"
)

// The last sentence of the note that counts the rows a result left off,
// saying how to reach them.
const (
	// queryReach ends a query result's note.
	queryReach = "Narrow the query, or look one up by its full formatted name as name, to reach them."

	// listReach ends the note of a kind, key/value or contact_list
	// result, whose rows run in formatted-name order.
	listReach = "Rows run in formatted-name order; contact_lookup by name, or with a query, reaches the ones left off."
)

// clipSearchField cuts s to at most limit bytes on a rune boundary,
// ending a cut value with searchCutMarker.
func clipSearchField(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := max(limit-len(searchCutMarker), 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + searchCutMarker
}

// formatSearchResults formats the contacts a query found, in the order
// the search returned them, within searchResultMaxBytes. Each row
// carries the contact's contact_id and trust zone, since a query is
// where an ambiguous name sends the model to find the candidates the
// error left out, and two of them can share a formatted name. A contact
// that answers to the query as a name also names the field it answers
// by, as the ambiguity error does. truncated reports that the search
// stopped at SearchLimit with more contacts matching, which the result
// says last. Rows past the budget are left off the end, after the name
// holders the search lists first, and a note counts them.
func formatSearchResults(contacts []*Contact, query string, truncated bool) string {
	echo := clipSearchField(query, searchFieldMaxBytes)
	stop := ""
	if truncated {
		stop = fmt.Sprintf("\nStopped at %d matches; more contacts match %q. Contacts whose formatted name, nickname, given name or first word it is are listed first. contact_lookup with a contact's full formatted name as name reaches one this list left out.\n", SearchLimit, echo)
	}
	row := func(c *Contact) string { return searchRow(c, query, echo) }
	return formatBudgetedList(contacts, row, queryReach, stop)
}

// formatContactList formats the contacts a kind, key/value or
// contact_list lookup returned, one summary line each, in the order the
// store returned them, within searchResultMaxBytes.
func formatContactList(contacts []*Contact) string {
	row := func(c *Contact) string {
		var sb strings.Builder
		writeContactRow(&sb, c)
		sb.WriteString("\n")
		return sb.String()
	}
	return formatBudgetedList(contacts, row, listReach, "")
}

// formatBudgetedList writes the count found, then each contact's row in
// order while the result stays within searchResultMaxBytes, then a note
// counting the rows left off, ending with reach, then tail. Room is kept
// for tail and for the note that would count the rows after each row, so
// both always fit once a row does not.
func formatBudgetedList(contacts []*Contact, row func(*Contact) string, reach, tail string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d contact(s):\n\n", len(contacts))
	listed := 0
	for _, c := range contacts {
		r := row(c)
		note := ""
		if listed+1 < len(contacts) {
			note = omittedRowsNote(listed+1, len(contacts), reach)
		}
		if sb.Len()+len(r)+len(note)+len(tail) > searchResultMaxBytes {
			break
		}
		sb.WriteString(r)
		listed++
	}
	if listed < len(contacts) {
		sb.WriteString(omittedRowsNote(listed, len(contacts), reach))
	}
	sb.WriteString(tail)
	return sb.String()
}

// omittedRowsNote says how many of the contacts found a result lists,
// and that the rest were left off its end to stay within
// searchResultMaxBytes, then how to reach them.
func omittedRowsNote(listed, found int, reach string) string {
	return fmt.Sprintf("\nListed %d of the %d contacts found; the last %d are left off to keep this result within %d KB. %s\n",
		listed, found, found-listed, searchResultMaxBytes>>10, reach)
}

// searchRow renders one query row: the contact's one-line summary with
// each long field cut, then its contact_id and trust zone whole, and
// the name field it answers to query by when it does. echo is query as
// the result quotes it.
func searchRow(c *Contact, query, echo string) string {
	var sb strings.Builder
	writeContactRow(&sb, c)
	fmt.Fprintf(&sb, "\n  contact_id %s | trust zone %s", c.ID, c.TrustZone)
	if field := NameMatchField(c, query); field != "" {
		fmt.Fprintf(&sb, " | answers to %q by %s", echo, field)
	}
	sb.WriteString("\n")
	return sb.String()
}

// writeContactRow writes the one-line summary a contact list shows: the
// formatted name, then the organization and AI summary when present,
// each cut to its bound.
func writeContactRow(sb *strings.Builder, c *Contact) {
	fmt.Fprintf(sb, "**%s**", clipSearchField(c.FormattedName, searchFieldMaxBytes))
	if c.Org != "" {
		fmt.Fprintf(sb, " (%s)", clipSearchField(c.Org, searchFieldMaxBytes))
	}
	if c.AISummary != "" {
		fmt.Fprintf(sb, " — %s", clipSearchField(c.AISummary, searchSummaryMaxBytes))
	}
}
