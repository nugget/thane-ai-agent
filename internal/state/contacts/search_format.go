package contacts

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// A query result is model-facing, and nothing bounds the length of a
// contact's name, organization or AI summary, so contact_lookup's query
// result holds to the 16 KB ceiling the repository's other search
// results hold to (the document tools' and email's). A long field is
// cut on a rune boundary and ends with searchCutMarker, as email cuts
// one, and the rows that would pass the ceiling are left off the end of
// the list and counted. A listed row is never cut short, so its
// contact_id and trust zone are always whole.
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
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d contact(s):\n\n", len(contacts))
	listed := 0
	for _, c := range contacts {
		row := searchRow(c, query, echo)
		// Room is kept for the note that would count the rows after this
		// one, so the note always fits once a row does not.
		note := ""
		if listed+1 < len(contacts) {
			note = omittedRowsNote(listed+1, len(contacts))
		}
		if sb.Len()+len(row)+len(note)+len(stop) > searchResultMaxBytes {
			break
		}
		sb.WriteString(row)
		listed++
	}
	if listed < len(contacts) {
		sb.WriteString(omittedRowsNote(listed, len(contacts)))
	}
	sb.WriteString(stop)
	return sb.String()
}

// omittedRowsNote says how many of the contacts found a query result
// lists, and that the rest were left off its end to stay within
// searchResultMaxBytes.
func omittedRowsNote(listed, found int) string {
	return fmt.Sprintf("\nListed %d of the %d contacts found; the last %d are left off to keep this result within %d KB. Narrow the query, or look one up by its full formatted name as name, to reach them.\n",
		listed, found, found-listed, searchResultMaxBytes>>10)
}

// searchRow renders one query row: the contact's one-line summary with
// each long field cut, then its contact_id and trust zone whole, and
// the name field it answers to query by when it does. echo is query as
// the result quotes it.
func searchRow(c *Contact, query, echo string) string {
	var sb strings.Builder
	writeContactRow(&sb,
		clipSearchField(c.FormattedName, searchFieldMaxBytes),
		clipSearchField(c.Org, searchFieldMaxBytes),
		clipSearchField(c.AISummary, searchSummaryMaxBytes))
	fmt.Fprintf(&sb, "\n  contact_id %s | trust zone %s", c.ID, c.TrustZone)
	if field := NameMatchField(c, query); field != "" {
		fmt.Fprintf(&sb, " | answers to %q by %s", echo, field)
	}
	sb.WriteString("\n")
	return sb.String()
}

// writeContactRow writes the one-line summary a contact list shows: the
// formatted name, then the organization and AI summary when present.
func writeContactRow(sb *strings.Builder, name, org, summary string) {
	fmt.Fprintf(sb, "**%s**", name)
	if org != "" {
		fmt.Fprintf(sb, " (%s)", org)
	}
	if summary != "" {
		fmt.Fprintf(sb, " — %s", summary)
	}
}
