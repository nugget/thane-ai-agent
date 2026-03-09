package carddav

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/google/uuid"

	"github.com/nugget/thane-ai-agent/internal/contacts"
)

// addressBookPath is the fixed path for the single Thane address book.
const addressBookPath = "/carddav/default/"

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

// ContactToCard converts a Contact with its Properties into a
// vcard.Card.  The contact must have Properties populated (via
// GetWithProperties).
func ContactToCard(c *contacts.Contact) vcard.Card {
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
func CardToContact(card vcard.Card) (*contacts.Contact, []contacts.Property) {
	c := &contacts.Contact{
		FormattedName: card.PreferredValue(vcard.FieldFormattedName),
	}

	// Kind with validation.
	kind := string(card.Kind())
	if kind == "" {
		kind = "individual"
	}
	if !contacts.ValidKinds[kind] {
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
	if trustZone == "" || !contacts.ValidTrustZones[trustZone] {
		trustZone = "known"
	}
	c.TrustZone = trustZone

	c.AISummary = card.PreferredValue("X-THANE-AI-SUMMARY")

	// Multi-value properties: everything not in coreProperties.
	var props []contacts.Property
	for name, fields := range card {
		if coreProperties[name] {
			continue
		}
		for _, f := range fields {
			p := contacts.Property{
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

// objectPath constructs the CardDAV object path for a contact ID.
func objectPath(id uuid.UUID) string {
	return path.Join(addressBookPath, id.String()+".vcf")
}

// contactIDFromPath extracts the contact UUID from a CardDAV object
// path.  Paths follow the pattern /carddav/default/{uuid}.vcf.
func contactIDFromPath(p string) (uuid.UUID, error) {
	base := path.Base(p)
	name := strings.TrimSuffix(base, ".vcf")
	id, err := uuid.Parse(name)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid contact path %q: %w", p, err)
	}
	return id, nil
}
