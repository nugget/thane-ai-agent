package contacts

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Automated mailbox patterns, as [AutomatedAddress] reports them.
const (
	AutomatedDoNotReply    = "do-not-reply"
	AutomatedNoReply       = "no-reply"
	AutomatedMailerDaemon  = "mailer-daemon"
	AutomatedNotifications = "notifications"
	AutomatedBounces       = "bounces"
)

// AutomatedAddress reports whether a bare addr-spec (local@domain)
// names a mailbox nobody reads, judged from its local part alone, and
// which pattern matched. Everything before the last '@' is taken as the
// local part, so callers pass an email Address.Key() or a stored EMAIL
// value, never a raw header: that is what keeps the display name, which
// the sender types, out of the judgement. The domain is never read,
// since it would need a reputation judgement; postmaster is not
// automated, because RFC 5321 requires a person to read it.
//
// The local part is lower-cased, cut at the first '+', and split into
// segments on '.', '-' and '_'. It is automated when a contiguous run
// of whole segments spells noreply or donotreply (no-reply,
// aws-noreply, do-not-reply), when any single segment is
// notification(s) or bounce(s) (calendar-notification), or when the
// whole local part squeezes to mailerdaemon.
//
// The answer only ever lowers a zone ([CapAtKnown]), so a false
// positive lands a sender at the default for new contacts, and a false
// negative is no worse than having no rule.
func AutomatedAddress(address string) (pattern string, ok bool) {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return "", false
	}
	local, _, _ := strings.Cut(address[:at], "+")
	segments := strings.FieldsFunc(local, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
	switch {
	case segmentRunSpells(segments, "donotreply"):
		return AutomatedDoNotReply, true
	case segmentRunSpells(segments, "noreply"):
		return AutomatedNoReply, true
	case strings.Join(segments, "") == "mailerdaemon":
		return AutomatedMailerDaemon, true
	}
	for _, segment := range segments {
		switch segment {
		case "notification", "notifications":
			return AutomatedNotifications, true
		case "bounce", "bounces":
			return AutomatedBounces, true
		}
	}
	return "", false
}

// segmentRunSpells reports whether some contiguous run of whole
// segments joins to word, so no-reply and aws-noreply match noreply
// while snoreply does not.
func segmentRunSpells(segments []string, word string) bool {
	for start := range segments {
		joined := ""
		for _, segment := range segments[start:] {
			joined += segment
			if joined == word {
				return true
			}
			if len(joined) >= len(word) {
				break
			}
		}
	}
	return false
}

// CapAtKnown lowers a zone to at most [ZoneKnown]: admin, household
// and trusted become known, known and unknown are unchanged, and any
// value the zone hierarchy does not recognise becomes [ZoneUnknown],
// failing low as the least-privileged-zone rule does. It never raises
// a zone.
func CapAtKnown(zone string) string {
	switch zone {
	case ZoneAdmin, ZoneHousehold, ZoneTrusted, ZoneKnown:
		return ZoneKnown
	default:
		return ZoneUnknown
	}
}

// AutomatedAddressFinding is one active contact record above known
// that holds an automated-looking email address. The runtime reads that
// address at known whatever the record says, so the record's stored
// zone is not honoured in full.
type AutomatedAddressFinding struct {
	ContactID uuid.UUID
	Name      string
	TrustZone string
	Address   string
	// Pattern is the [AutomatedAddress] pattern the address matched.
	Pattern string
}

// AutomatedAddressesAboveKnown lists every EMAIL property on an active
// admin, household or trusted record whose address [AutomatedAddress]
// recognises, ordered by record name then address. It uses the same
// active filter and property name as [Store.FindAllByPropertyExact], so
// the audit and the email resolver agree about which rows count.
func (s *Store) AutomatedAddressesAboveKnown(ctx context.Context) ([]AutomatedAddressFinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT contacts.id, contacts.formatted_name, contacts.trust_zone, contact_properties.value
		FROM contacts
		JOIN contact_properties ON contacts.id = contact_properties.contact_id
		WHERE contacts.`+activeFilter+`
		  AND contact_properties.property = 'EMAIL'
		  AND contacts.trust_zone IN (?, ?, ?)
		ORDER BY contacts.formatted_name, contact_properties.value
	`, ZoneAdmin, ZoneHousehold, ZoneTrusted)
	if err != nil {
		return nil, fmt.Errorf("query automated addresses: %w", err)
	}
	defer rows.Close()

	var findings []AutomatedAddressFinding
	for rows.Next() {
		var id, name, zone, address string
		if err := rows.Scan(&id, &name, &zone, &address); err != nil {
			return nil, fmt.Errorf("scan automated address: %w", err)
		}
		pattern, ok := AutomatedAddress(address)
		if !ok {
			continue
		}
		contactID, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("parse contact id %q: %w", id, err)
		}
		findings = append(findings, AutomatedAddressFinding{
			ContactID: contactID,
			Name:      name,
			TrustZone: zone,
			Address:   address,
			Pattern:   pattern,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate automated addresses: %w", err)
	}
	return findings, nil
}
