package contacts

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-vcard"
)

// coreProperties is the set of vCard properties that map to Contact
// struct fields rather than contact_properties rows.  When parsing an
// incoming vCard, these are extracted into the struct and NOT stored
// as separate Property rows.
var coreProperties = map[string]bool{
	vcard.FieldVersion:       true,
	vcard.FieldUID:           true,
	vcard.FieldFormattedName: true,
	vcard.FieldName:          true,
	vcard.FieldKind:          true,
	vcard.FieldNickname:      true,
	vcard.FieldBirthday:      true,
	vcard.FieldAnniversary:   true,
	vcard.FieldGender:        true,
	vcard.FieldOrganization:  true,
	vcard.FieldTitle:         true,
	vcard.FieldRole:          true,
	vcard.FieldNote:          true,
	vcard.FieldPhoto:         true,
	vcard.FieldRevision:      true,
	"X-THANE-TRUST-ZONE":     true,
	"X-THANE-AI-SUMMARY":     true,
}

// skipProperties lists vCard properties that should never be stored as
// contact_properties rows. These are either metadata about the vCard
// file itself or Apple-specific grouped labels that lose meaning when
// the group prefix is stripped during parsing.
var skipProperties = map[string]bool{
	"X-ABLABEL": true, // Apple grouped label — orphaned without group context
	"PRODID":    true, // vCard generator identifier, not contact data
}

// ContactToCard converts a Contact with its Properties into a
// vcard.Card.  The contact must have Properties populated (via
// GetWithProperties).
func ContactToCard(c *Contact) vcard.Card {
	card := make(vcard.Card)

	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, c.ID.String())
	card.SetValue(vcard.FieldFormattedName, c.FormattedName)

	card.SetKind(vcard.Kind(c.Kind))

	card.SetName(&vcard.Name{
		FamilyName:      c.FamilyName,
		GivenName:       c.GivenName,
		AdditionalName:  c.AdditionalNames,
		HonorificPrefix: c.NamePrefix,
		HonorificSuffix: c.NameSuffix,
	})

	if c.Nickname != "" {
		card.SetValue(vcard.FieldNickname, c.Nickname)
	}
	if c.Birthday != "" {
		card.SetValue(vcard.FieldBirthday, c.Birthday)
	}
	if c.Anniversary != "" {
		card.SetValue(vcard.FieldAnniversary, c.Anniversary)
	}
	if c.Gender != "" {
		card.SetGender(vcard.Sex(c.Gender), "")
	}
	if c.Org != "" {
		card.SetValue(vcard.FieldOrganization, c.Org)
	}
	if c.Title != "" {
		card.SetValue(vcard.FieldTitle, c.Title)
	}
	if c.Role != "" {
		card.SetValue(vcard.FieldRole, c.Role)
	}
	if c.Note != "" {
		card.SetValue(vcard.FieldNote, c.Note)
	}
	if c.PhotoURI != "" {
		card.Add(vcard.FieldPhoto, &vcard.Field{
			Value:  c.PhotoURI,
			Params: vcard.Params{vcard.ParamValue: {"uri"}},
		})
	}

	if c.Rev != "" {
		if t, err := time.Parse(time.RFC3339, c.Rev); err == nil {
			card.SetRevision(t)
		}
	}

	// Thane-specific extensions.
	card.SetValue("X-THANE-TRUST-ZONE", c.TrustZone)
	if c.AISummary != "" {
		card.SetValue("X-THANE-AI-SUMMARY", c.AISummary)
	}

	// Multi-value properties from contact_properties.
	for _, p := range c.Properties {
		field := &vcard.Field{
			Value:  p.Value,
			Params: make(vcard.Params),
		}

		if p.Type != "" {
			for _, t := range strings.Split(p.Type, ",") {
				field.Params.Add(vcard.ParamType, strings.TrimSpace(t))
			}
		}
		if p.Pref > 0 {
			field.Params.Set(vcard.ParamPreferred, strconv.Itoa(p.Pref))
		}
		if p.Label != "" {
			field.Params.Set("LABEL", p.Label)
		}
		if p.MediaType != "" {
			field.Params.Set(vcard.ParamMediaType, p.MediaType)
		}

		card.Add(p.Property, field)
	}

	return card
}

// CardToContact converts a vcard.Card into a Contact and its
// Properties.  The caller is responsible for setting the Contact ID
// and handling persistence via the store.
func CardToContact(card vcard.Card) (*Contact, []Property) {
	c := &Contact{
		FormattedName: card.PreferredValue(vcard.FieldFormattedName),
	}

	// Kind with validation.
	kind := string(card.Kind())
	if kind == "" {
		kind = "individual"
	}
	if !ValidKinds[kind] {
		kind = "individual"
	}
	c.Kind = kind

	// Structured name.
	if n := card.Name(); n != nil {
		c.FamilyName = n.FamilyName
		c.GivenName = n.GivenName
		c.AdditionalNames = n.AdditionalName
		c.NamePrefix = n.HonorificPrefix
		c.NameSuffix = n.HonorificSuffix
	}

	c.Nickname = card.PreferredValue(vcard.FieldNickname)
	c.Birthday = card.PreferredValue(vcard.FieldBirthday)
	c.Anniversary = card.PreferredValue(vcard.FieldAnniversary)

	sex, _ := card.Gender()
	c.Gender = string(sex)

	c.Org = card.PreferredValue(vcard.FieldOrganization)
	c.Title = card.PreferredValue(vcard.FieldTitle)
	c.Role = card.PreferredValue(vcard.FieldRole)
	c.Note = card.PreferredValue(vcard.FieldNote)

	if photo := card.Get(vcard.FieldPhoto); photo != nil {
		c.PhotoURI = photo.Value
	}

	// Thane extensions.
	trustZone := card.PreferredValue("X-THANE-TRUST-ZONE")
	if trustZone == "" || !ValidTrustZones[trustZone] {
		trustZone = "known"
	}
	c.TrustZone = trustZone

	c.AISummary = card.PreferredValue("X-THANE-AI-SUMMARY")

	// Multi-value properties: everything not in coreProperties or skipProperties.
	var props []Property
	for name, fields := range card {
		if coreProperties[name] || skipProperties[name] {
			continue
		}
		for _, f := range fields {
			p := Property{
				Property: name,
				Value:    f.Value,
			}
			if types := f.Params.Types(); len(types) > 0 {
				p.Type = strings.Join(types, ",")
			}
			if prefStr := f.Params.Get(vcard.ParamPreferred); prefStr != "" {
				if pref, err := strconv.Atoi(prefStr); err == nil {
					p.Pref = pref
				}
			}
			p.Label = f.Params.Get("LABEL")
			p.MediaType = f.Params.Get(vcard.ParamMediaType)

			props = append(props, p)
		}
	}

	return c, props
}

// EncodeVCard serializes a Contact (with Properties populated) into
// vCard 4.0 text.
func EncodeVCard(c *Contact) (string, error) {
	card := ContactToCard(c)
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return "", fmt.Errorf("encode vcard: %w", err)
	}
	return buf.String(), nil
}

// EncodeVCards serializes multiple contacts into a multi-vCard text
// stream (concatenated BEGIN:VCARD ... END:VCARD blocks).
func EncodeVCards(contacts []*Contact) (string, error) {
	var buf bytes.Buffer
	enc := vcard.NewEncoder(&buf)
	for _, c := range contacts {
		card := ContactToCard(c)
		if err := enc.Encode(card); err != nil {
			return "", fmt.Errorf("encode vcard for %q: %w", c.FormattedName, err)
		}
	}
	return buf.String(), nil
}

// DecodeVCards parses one or more vCards from the reader. Each vCard
// produces a Contact and its associated Properties.
func DecodeVCards(r io.Reader) ([]*Contact, [][]Property, error) {
	dec := vcard.NewDecoder(r)

	var contacts []*Contact
	var allProps [][]Property

	for {
		card, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("decode vcard: %w", err)
		}
		c, props := CardToContact(card)
		contacts = append(contacts, c)
		allProps = append(allProps, props)
	}

	return contacts, allProps, nil
}

// zoneHierarchy orders trust zones from most to least privileged.
// Used for cascading PHOTO resolution.
var zoneHierarchy = []string{
	ZoneAdmin,
	ZoneHousehold,
	ZoneTrusted,
	ZoneKnown,
}

// FilterCardForTrustZone strips or adjusts vCard fields based on the
// recipient's trust zone. This is used when exporting the self-contact
// to share with contacts at different trust levels. The props parameter
// provides zone-tagged PHOTO properties for zone-specific photo
// resolution.
func FilterCardForTrustZone(card vcard.Card, zone string, props []Property) vcard.Card {
	if zone == ZoneUnknown {
		return filterUnknown(card)
	}
	if zone == ZoneKnown {
		return filterKnown(card, props)
	}
	// admin, household, trusted: keep everything, resolve zone-specific photo.
	resolveZonePhoto(card, zone, props)
	return card
}

// filterUnknown strips all sensitive fields for unknown contacts.
func filterUnknown(card vcard.Card) vcard.Card {
	delete(card, vcard.FieldPhoto)
	delete(card, vcard.FieldEmail)
	delete(card, vcard.FieldTelephone)
	delete(card, vcard.FieldIMPP)
	delete(card, vcard.FieldNote)
	delete(card, vcard.FieldAddress)
	delete(card, vcard.FieldURL)

	// Remove all X-THANE-* extensions.
	for name := range card {
		if strings.HasPrefix(name, "X-THANE-") {
			delete(card, name)
		}
	}

	return card
}

// filterKnown limits fields for known-zone contacts: first EMAIL/IMPP
// only, truncated NOTE, no TEL/ADR/X-THANE-*.
func filterKnown(card vcard.Card, props []Property) vcard.Card {
	// Keep first EMAIL only.
	if emails := card[vcard.FieldEmail]; len(emails) > 1 {
		card[vcard.FieldEmail] = emails[:1]
	}

	// Remove TEL.
	delete(card, vcard.FieldTelephone)

	// Keep first IMPP only.
	if impp := card[vcard.FieldIMPP]; len(impp) > 1 {
		card[vcard.FieldIMPP] = impp[:1]
	}

	// Truncate NOTE (rune-safe to avoid splitting multi-byte characters).
	if notes := card[vcard.FieldNote]; len(notes) > 0 && utf8.RuneCountInString(notes[0].Value) > 100 {
		runes := []rune(notes[0].Value)
		card[vcard.FieldNote] = []*vcard.Field{{
			Value:  string(runes[:100]) + "…",
			Params: notes[0].Params,
		}}
	}

	// Remove ADR.
	delete(card, vcard.FieldAddress)

	// Remove all X-THANE-* extensions.
	for name := range card {
		if strings.HasPrefix(name, "X-THANE-") {
			delete(card, name)
		}
	}

	// Resolve zone-specific photo.
	resolveZonePhoto(card, ZoneKnown, props)

	return card
}

// resolveZonePhoto replaces the card's PHOTO field with a zone-specific
// photo if one is found in the contact properties. It searches for a
// PHOTO property with Label matching the target zone, then cascades up
// the trust hierarchy. If no zone-specific photo is found, the existing
// PHOTO (from Contact.PhotoURI) is left unchanged.
func resolveZonePhoto(card vcard.Card, zone string, props []Property) {
	// Build a map of zone → PHOTO URI from properties.
	zonePhotos := make(map[string]string)
	for _, p := range props {
		if p.Property == vcard.FieldPhoto && p.Label != "" {
			zonePhotos[p.Label] = p.Value
		}
	}

	if len(zonePhotos) == 0 {
		return // No zone-specific photos; keep default.
	}

	// Find the target zone's position in the hierarchy.
	startIdx := -1
	for i, z := range zoneHierarchy {
		if z == zone {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return // Unknown zone position; keep default.
	}

	// Search from target zone upward (toward more privileged).
	for i := startIdx; i >= 0; i-- {
		if uri, ok := zonePhotos[zoneHierarchy[i]]; ok {
			card[vcard.FieldPhoto] = []*vcard.Field{{
				Value:  uri,
				Params: vcard.Params{vcard.ParamValue: {"uri"}},
			}}
			return
		}
	}
	// No match found; keep default PHOTO.
}
