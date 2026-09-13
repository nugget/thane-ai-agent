package contacts

import (
	"fmt"
	"strconv"
	"strings"
)

// importCardListMax bounds how many card numbers one import note names,
// so a large vCard file with many skipped cards still returns a bounded
// result.
const importCardListMax = 20

// importDrops counts what one vCard import left out, by reason, and
// names each card it skipped.
type importDrops struct {
	keys          int
	names         int
	values        int
	ownerName     int
	identity      int
	writeFailures int
	// nameless are the cards skipped because they had no usable name.
	nameless []int
	// changed are the cards skipped because the operator re-zoned or
	// deleted their merge target between the import's read and its write.
	changed []int
	// unwritten are the cards skipped because their write transaction
	// failed.
	unwritten []int
}

// notes renders what the import left out as the sentences its result
// ends with: each drop as a count with its cause, and each skipped card
// by number with its cause and remedy.
func (d importDrops) notes() string {
	var b strings.Builder
	if d.keys > 0 {
		fmt.Fprintf(&b, " %d key propert(ies) were not imported: KEY and X-THANE-KEY-* are operator custody and never come in through a model tool.", d.keys)
	}
	if d.names > 0 {
		fmt.Fprintf(&b, " %d propert(ies) were not imported: their names are not plain vCard property names (a nested group such as a.b.EMAIL, a space, or more than 64 characters) and would become a different property on the next CardDAV round trip.", d.names)
	}
	if d.values > 0 {
		fmt.Fprintf(&b, " %d value(s) were not imported: they carry a carriage return or other control character, which a contacts client would read as the start of another property.", d.values)
	}
	if d.ownerName > 0 {
		fmt.Fprintf(&b, " %d name(s) were not imported: a card that would create a contact, or give an existing one a nickname, under the name Thane recognizes the operator by was left out, because that contact could take the operator's identity; ask the operator to add it through CardDAV or the contacts API.", d.ownerName)
	}
	if d.identity > 0 {
		fmt.Fprintf(&b, " %d address(es)/number(s) were not imported: EMAIL, TEL and IMPP values are operator custody on an admin, household, trusted or operator contact, and a value one of those already holds keeps its single holder; ask the operator to add them through CardDAV or the contacts API.", d.identity)
	}
	if d.writeFailures > 0 {
		fmt.Fprintf(&b, " %d propert(ies) failed to write and were not imported; the log names each one.", d.writeFailures)
	}
	if len(d.nameless) > 0 {
		fmt.Fprintf(&b, " %d card(s) were skipped because they have no usable name (FN): %s. Give each a plain FN and import it again.", len(d.nameless), cardList(d.nameless))
	}
	if len(d.changed) > 0 {
		fmt.Fprintf(&b, " %d card(s) were skipped, not merged, because the operator re-zoned or deleted the contact each would merge into while the import ran: %s. Nothing from them was written; import them again, or rerun the whole import, with merge on (the default), and the rules apply to each contact as it is now.", len(d.changed), cardList(d.changed))
	}
	if len(d.unwritten) > 0 {
		fmt.Fprintf(&b, " %d card(s) were skipped because their write failed, as it can when an operator change to the contacts collides with it: %s. Nothing from them was written and the log names each error; import them again, or rerun the whole import, with merge on (the default), which merges the cards already written rather than duplicating them.", len(d.unwritten), cardList(d.unwritten))
	}
	return b.String()
}

// cardList names card numbers as "card 3" or "cards 3, 7, 9", listing
// at most importCardListMax of them and counting the rest.
func cardList(cards []int) string {
	shown := cards
	if len(shown) > importCardListMax {
		shown = shown[:importCardListMax]
	}
	parts := make([]string, len(shown))
	for i, n := range shown {
		parts[i] = strconv.Itoa(n)
	}
	noun := "cards "
	if len(cards) == 1 {
		noun = "card "
	}
	list := noun + strings.Join(parts, ", ")
	if rest := len(cards) - len(shown); rest > 0 {
		list += fmt.Sprintf(" and %d more", rest)
	}
	return list
}
