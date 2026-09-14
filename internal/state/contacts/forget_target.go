package contacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// maxForgetSiblingsNamed bounds the other records a refused forget names.
const maxForgetSiblingsNamed = 3

// forgetTarget returns the record a contact_forget names, and whether it
// was named by name. Exactly one of name and contact_id must be set: a
// name resolves as contact_lookup resolves it, authority first, and a
// contact_id selects that one active record, which is how a known
// duplicate is removed when its name resolves to the record above known
// it duplicates.
func (t *Tools) forgetTarget(ctx context.Context, args ForgetContactArgs) (*Contact, bool, error) {
	hasName := strings.TrimSpace(args.Name) != ""
	rawID := strings.TrimSpace(args.ContactID)
	switch {
	case !hasName && rawID == "":
		return nil, false, fmt.Errorf("pass exactly one of name or contact_id: name resolves the contact the way contact_lookup does, and contact_id removes the one record with that UUID; nothing was removed")
	case hasName && rawID != "":
		return nil, false, fmt.Errorf("pass exactly one of name or contact_id, not both: name %q and contact_id %q may select different records; nothing was removed", echoForRefusal(args.Name), echoForRefusal(args.ContactID))
	case rawID != "":
		c, err := t.forgetTargetByID(ctx, rawID)
		return c, false, err
	}

	c, err := t.store.resolveContact(ctx, args.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("no contact matches %q; nothing was removed. Check the name with contact_lookup", args.Name)
	}
	if err != nil {
		return nil, false, fmt.Errorf("resolve contact: %w; nothing was removed", err)
	}
	return c, true, nil
}

// forgetTargetByID loads the one active record a canonical UUID names.
func (t *Tools) forgetTargetByID(ctx context.Context, rawID string) (*Contact, error) {
	id, err := uuid.Parse(rawID)
	if err != nil || id == uuid.Nil || id.String() != rawID {
		return nil, fmt.Errorf("contact_id must be a canonical non-zero UUID, lowercase with hyphens, as a duplicate report, contact_owner or a contact_forget refusal gives it; got %q; nothing was removed", echoForRefusal(rawID))
	}
	c, err := t.store.get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("contact_id %s is not an active contact, so it may already be forgotten; nothing was removed. Check the name with contact_lookup", id)
	}
	if err != nil {
		return nil, fmt.Errorf("load contact %s: %w; nothing was removed", id, err)
	}
	return c, nil
}

// forgetSiblingHint names the other records a forget by name may have
// meant instead of the custodied record c it resolved to: known
// records, neither the operator's nor bound to a Home Assistant person,
// that share a name key with c (see name_keys.go) and themselves answer
// to name, exactly or by a short form. contact_forget by contact_id can
// remove those. It returns "" when there are none. A failed lookup is
// logged and leaves the hint out, since the refusal stands either way.
func (t *Tools) forgetSiblingHint(ctx context.Context, c *Contact, name string) string {
	key := nameKey(name)
	_, siblings, err := t.store.nameSiblings(ctx, c)
	if err != nil {
		slog.Warn("contact_forget could not list the name siblings of a refused contact", "contact_id", c.ID.String(), "error", err)
		return ""
	}
	operatorID, err := t.custodyOperatorID(ctx)
	if err != nil {
		slog.Warn("contact_forget could not resolve the operator to list name siblings", "contact_id", c.ID.String(), "error", err)
		return ""
	}
	var named []string
	extra := 0
	for _, s := range siblings {
		if !s.keys.answersTo(key) {
			continue
		}
		if s.TrustZone != ZoneKnown || s.Operator || s.HAPersonEntity != "" || (operatorID != uuid.Nil && s.ContactID == operatorID) {
			continue
		}
		if len(named) == maxForgetSiblingsNamed {
			extra++
			continue
		}
		named = append(named, fmt.Sprintf("%s (known, %s)", echoForRefusal(s.Name), s.ContactID))
	}
	if len(named) == 0 {
		return ""
	}
	list := strings.Join(named, ", ")
	if extra > 0 {
		list += fmt.Sprintf(" and %d more", extra)
	}
	return fmt.Sprintf(". The name %q also fits %s, which contact_forget can remove: if that is the record you meant, call contact_forget with its contact_id instead of the name", echoForRefusal(name), list)
}
