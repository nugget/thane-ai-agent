package contacts

import (
	"context"
	"fmt"
	"strings"
)

// maxDossierSiblingsChecked bounds how many name siblings the first
// write of a dossier looks up dossiers for, taken in authority order,
// so one crowded name costs a bounded number of document reads.
const maxDossierSiblingsChecked = 16

// ConfigureDossierPresence installs the check [Tools.WriteDossier] uses
// to tell a dossier's first write from an update, and to find a name
// sibling's dossier. exists reports whether the document at a ref
// exists, and must record no read receipt. Until it is configured,
// WriteDossier does not refuse a second dossier.
func (t *Tools) ConfigureDossierPresence(exists func(ctx context.Context, ref string) (bool, error)) {
	if t == nil {
		return
	}
	t.dossierExists = exists
}

// secondDossierRefusal refuses the first write of contact's dossier when
// a record that shares a name key with it (see name_keys.go), and is
// likely the same person (see fork_evidence.go), already has a dossier:
// two dossiers split what is known about one person saved twice. Name
// siblings with no sign of being one person, such as two people who
// share a first name and each hold their own number, each keep their
// own dossier. An update to an existing dossier is never refused.
func (t *Tools) secondDossierRefusal(ctx context.Context, contact *Contact) error {
	if t.dossierExists == nil {
		return nil
	}
	exists, err := t.dossierExists(ctx, DossierRef(contact.ID))
	if err != nil {
		return fmt.Errorf("check whether contact %s already has a dossier: %w", contact.ID, err)
	}
	if exists {
		return nil
	}
	self, siblings, err := t.store.nameSiblings(ctx, contact)
	if err != nil {
		return fmt.Errorf("check name siblings of contact %s before its first dossier: %w", contact.ID, err)
	}
	var held []nameSibling
	checked := 0
	for _, sibling := range siblings {
		if sibling.Evidence == "" {
			continue
		}
		if checked == maxDossierSiblingsChecked {
			break
		}
		checked++
		ref := DossierRef(sibling.ContactID)
		exists, err := t.dossierExists(ctx, ref)
		if err != nil {
			return fmt.Errorf("check dossier %s of name sibling %s: %w", ref, sibling.ContactID, err)
		}
		if exists {
			held = append(held, sibling)
		}
	}
	if len(held) == 0 {
		return nil
	}
	return secondDossierError(self.member, held)
}

// secondDossierError teaches the refusal: which records, which name,
// why they look like one person, and what to do in either case. The
// advice follows authority: the record with more authority keeps the
// dossier, so a known duplicate's dossier is folded into its sibling
// above known, never the reverse.
func secondDossierError(self ContactForkMember, held []nameSibling) error {
	named := make([]string, 0, len(held))
	for _, s := range held {
		named = append(named, fmt.Sprintf("%s (%s, %s) at %s", echoForRefusal(s.Name), s.TrustZone, s.ContactID, DossierRef(s.ContactID)))
	}
	verb := "already has one"
	if len(held) > 1 {
		verb = "already have one each"
	}
	holder := held[0]
	return fmt.Errorf("contact_dossier_write refused to start a second dossier: %s (%s, %s) has no dossier yet, and %s %s. They share the name %q and %s, so they are likely one person saved twice, and a second dossier would split what is known about them. %s. If they are different people, write nothing and report both records to the operator, who can rename one through CardDAV or /v1/contacts. Nothing was written",
		echoForRefusal(self.Name), self.TrustZone, self.ContactID, strings.Join(named, " and "), verb,
		echoForRefusal(holder.Key), holder.Evidence, secondDossierAdvice(self, holder))
}

// secondDossierAdvice says what to do when the two records are one
// person, by which of them carries more authority.
func secondDossierAdvice(self ContactForkMember, holder nameSibling) string {
	switch {
	case holder.authorityRank() <= self.authorityRank():
		return fmt.Sprintf("If they are one person, write what you meant into the existing dossier with contact_dossier_write contact_id %s, and report the duplicate record to the operator", holder.ContactID)
	case holder.TrustZone == ZoneKnown && holder.HAPersonEntity == "":
		return fmt.Sprintf("If they are one person, this contact carries more authority and should keep the dossier: read the known duplicate's dossier with contact_dossier_read contact_id %s, forget the duplicate with contact_forget contact_id %s, then write this contact's first dossier with what the duplicate's dossier held", holder.ContactID, holder.ContactID)
	case holder.HAPersonEntity != "":
		return fmt.Sprintf("If they are one person, report the duplicate to the operator: %s is bound to Home Assistant person %s, so only the operator can remove it, and this contact's first dossier waits until it is gone", echoForRefusal(holder.Name), echoForRefusal(holder.HAPersonEntity))
	default:
		return fmt.Sprintf("If they are one person, report the duplicate to the operator: %s is above known, so only the operator can remove it, and this contact's first dossier waits until it is gone", echoForRefusal(holder.Name))
	}
}
