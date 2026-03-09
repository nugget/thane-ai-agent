package contacts

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/google/uuid"
)

func TestContactToCard_FullRoundTrip(t *testing.T) {
	id := uuid.New()
	c := &Contact{
		ID:              id,
		Kind:            "individual",
		FormattedName:   "Jane Doe",
		FamilyName:      "Doe",
		GivenName:       "Jane",
		AdditionalNames: "Marie",
		NamePrefix:      "Dr.",
		NameSuffix:      "PhD",
		Nickname:        "Janey",
		Birthday:        "1990-05-15",
		Anniversary:     "2015-06-20",
		Gender:          "F",
		Org:             "Acme Corp",
		Title:           "VP Engineering",
		Role:            "Technical Leadership",
		Note:            "Met at conference",
		PhotoURI:        "https://example.com/photo.jpg",
		TrustZone:       "trusted",
		AISummary:       "Key business contact",
		Rev:             "2025-01-15T10:30:00Z",
		Properties: []Property{
			{Property: "EMAIL", Value: "jane@example.com", Type: "work", Pref: 1},
			{Property: "EMAIL", Value: "jane@personal.com", Type: "home"},
			{Property: "TEL", Value: "+15551234567", Type: "cell"},
			{Property: "IMPP", Value: "signal:+15551234567"},
			{Property: "URL", Value: "https://jane.example.com"},
		},
	}

	card := ContactToCard(c)

	// Verify core fields.
	if got := card.Value(vcard.FieldUID); got != id.String() {
		t.Errorf("UID = %q, want %q", got, id.String())
	}
	if got := card.Value(vcard.FieldFormattedName); got != "Jane Doe" {
		t.Errorf("FN = %q, want %q", got, "Jane Doe")
	}
	if got := card.Kind(); got != vcard.KindIndividual {
		t.Errorf("KIND = %q, want %q", got, vcard.KindIndividual)
	}

	n := card.Name()
	if n == nil {
		t.Fatal("Name() returned nil")
	}
	if n.FamilyName != "Doe" {
		t.Errorf("FamilyName = %q, want %q", n.FamilyName, "Doe")
	}
	if n.GivenName != "Jane" {
		t.Errorf("GivenName = %q, want %q", n.GivenName, "Jane")
	}
	if n.AdditionalName != "Marie" {
		t.Errorf("AdditionalName = %q, want %q", n.AdditionalName, "Marie")
	}
	if n.HonorificPrefix != "Dr." {
		t.Errorf("HonorificPrefix = %q, want %q", n.HonorificPrefix, "Dr.")
	}
	if n.HonorificSuffix != "PhD" {
		t.Errorf("HonorificSuffix = %q, want %q", n.HonorificSuffix, "PhD")
	}

	if got := card.Value(vcard.FieldNickname); got != "Janey" {
		t.Errorf("NICKNAME = %q, want %q", got, "Janey")
	}
	if got := card.Value(vcard.FieldBirthday); got != "1990-05-15" {
		t.Errorf("BDAY = %q, want %q", got, "1990-05-15")
	}
	if got := card.Value(vcard.FieldOrganization); got != "Acme Corp" {
		t.Errorf("ORG = %q, want %q", got, "Acme Corp")
	}
	if got := card.Value(vcard.FieldTitle); got != "VP Engineering" {
		t.Errorf("TITLE = %q, want %q", got, "VP Engineering")
	}
	if got := card.Value(vcard.FieldNote); got != "Met at conference" {
		t.Errorf("NOTE = %q, want %q", got, "Met at conference")
	}

	if got := card.Value("X-THANE-TRUST-ZONE"); got != "trusted" {
		t.Errorf("X-THANE-TRUST-ZONE = %q, want %q", got, "trusted")
	}
	if got := card.Value("X-THANE-AI-SUMMARY"); got != "Key business contact" {
		t.Errorf("X-THANE-AI-SUMMARY = %q, want %q", got, "Key business contact")
	}

	// Verify multi-value properties.
	emails := card[vcard.FieldEmail]
	if len(emails) != 2 {
		t.Fatalf("expected 2 EMAIL fields, got %d", len(emails))
	}

	tels := card[vcard.FieldTelephone]
	if len(tels) != 1 {
		t.Fatalf("expected 1 TEL field, got %d", len(tels))
	}
	if tels[0].Value != "+15551234567" {
		t.Errorf("TEL value = %q, want %q", tels[0].Value, "+15551234567")
	}
}

func TestContactToCard_Minimal(t *testing.T) {
	c := &Contact{
		ID:            uuid.New(),
		Kind:          "individual",
		FormattedName: "Minimal Contact",
		TrustZone:     "known",
		Rev:           "2025-01-01T00:00:00Z",
	}

	card := ContactToCard(c)

	if got := card.Value(vcard.FieldFormattedName); got != "Minimal Contact" {
		t.Errorf("FN = %q, want %q", got, "Minimal Contact")
	}
	if got := card.Value(vcard.FieldVersion); got != "4.0" {
		t.Errorf("VERSION = %q, want %q", got, "4.0")
	}

	// Optional fields should be absent.
	if card.Value(vcard.FieldNickname) != "" {
		t.Errorf("NICKNAME should be empty for minimal contact")
	}
	if card.Value(vcard.FieldNote) != "" {
		t.Errorf("NOTE should be empty for minimal contact")
	}
}

func TestCardToContact_FullRoundTrip(t *testing.T) {
	id := uuid.New()
	original := &Contact{
		ID:              id,
		Kind:            "individual",
		FormattedName:   "Jane Doe",
		FamilyName:      "Doe",
		GivenName:       "Jane",
		AdditionalNames: "Marie",
		NamePrefix:      "Dr.",
		NameSuffix:      "PhD",
		Nickname:        "Janey",
		Birthday:        "1990-05-15",
		Anniversary:     "2015-06-20",
		Gender:          "F",
		Org:             "Acme Corp",
		Title:           "VP Engineering",
		Role:            "Technical Leadership",
		Note:            "Met at conference",
		TrustZone:       "trusted",
		AISummary:       "Key business contact",
		Rev:             "2025-01-15T10:30:00Z",
		Properties: []Property{
			{Property: "EMAIL", Value: "jane@example.com", Type: "work", Pref: 1},
			{Property: "TEL", Value: "+15551234567", Type: "cell"},
		},
	}

	// Convert to vCard and back.
	card := ContactToCard(original)
	restored, props := CardToContact(card)

	if restored.FormattedName != original.FormattedName {
		t.Errorf("FormattedName = %q, want %q", restored.FormattedName, original.FormattedName)
	}
	if restored.Kind != original.Kind {
		t.Errorf("Kind = %q, want %q", restored.Kind, original.Kind)
	}
	if restored.FamilyName != original.FamilyName {
		t.Errorf("FamilyName = %q, want %q", restored.FamilyName, original.FamilyName)
	}
	if restored.GivenName != original.GivenName {
		t.Errorf("GivenName = %q, want %q", restored.GivenName, original.GivenName)
	}
	if restored.AdditionalNames != original.AdditionalNames {
		t.Errorf("AdditionalNames = %q, want %q", restored.AdditionalNames, original.AdditionalNames)
	}
	if restored.NamePrefix != original.NamePrefix {
		t.Errorf("NamePrefix = %q, want %q", restored.NamePrefix, original.NamePrefix)
	}
	if restored.NameSuffix != original.NameSuffix {
		t.Errorf("NameSuffix = %q, want %q", restored.NameSuffix, original.NameSuffix)
	}
	if restored.Nickname != original.Nickname {
		t.Errorf("Nickname = %q, want %q", restored.Nickname, original.Nickname)
	}
	if restored.Birthday != original.Birthday {
		t.Errorf("Birthday = %q, want %q", restored.Birthday, original.Birthday)
	}
	if restored.Org != original.Org {
		t.Errorf("Org = %q, want %q", restored.Org, original.Org)
	}
	if restored.Title != original.Title {
		t.Errorf("Title = %q, want %q", restored.Title, original.Title)
	}
	if restored.Role != original.Role {
		t.Errorf("Role = %q, want %q", restored.Role, original.Role)
	}
	if restored.Note != original.Note {
		t.Errorf("Note = %q, want %q", restored.Note, original.Note)
	}
	if restored.TrustZone != original.TrustZone {
		t.Errorf("TrustZone = %q, want %q", restored.TrustZone, original.TrustZone)
	}
	if restored.AISummary != original.AISummary {
		t.Errorf("AISummary = %q, want %q", restored.AISummary, original.AISummary)
	}

	// Verify properties round-tripped.
	emailCount := 0
	telCount := 0
	for _, p := range props {
		switch p.Property {
		case "EMAIL":
			emailCount++
			if p.Value != "jane@example.com" {
				t.Errorf("unexpected EMAIL value %q", p.Value)
			}
		case "TEL":
			telCount++
			if p.Value != "+15551234567" {
				t.Errorf("unexpected TEL value %q", p.Value)
			}
		}
	}
	if emailCount != 1 {
		t.Errorf("expected 1 EMAIL property, got %d", emailCount)
	}
	if telCount != 1 {
		t.Errorf("expected 1 TEL property, got %d", telCount)
	}
}

func TestCardToContact_InvalidKindDefaultsToIndividual(t *testing.T) {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Test")
	card.SetValue(vcard.FieldKind, "invalid-kind")

	c, _ := CardToContact(card)
	if c.Kind != "individual" {
		t.Errorf("Kind = %q, want %q for invalid input", c.Kind, "individual")
	}
}

func TestCardToContact_InvalidTrustZoneDefaultsToKnown(t *testing.T) {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Test")
	card.SetValue("X-THANE-TRUST-ZONE", "invalid-zone")

	c, _ := CardToContact(card)
	if c.TrustZone != "known" {
		t.Errorf("TrustZone = %q, want %q for invalid input", c.TrustZone, "known")
	}
}

func TestCardToContact_LegacyOwnerDefaultsToKnown(t *testing.T) {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Legacy Owner")
	card.SetValue("X-THANE-TRUST-ZONE", "owner")

	c, _ := CardToContact(card)
	if c.TrustZone != "known" {
		t.Errorf("TrustZone = %q, want %q for legacy 'owner' value", c.TrustZone, "known")
	}
}

func TestCardToContact_MissingTrustZoneDefaultsToKnown(t *testing.T) {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Test")

	c, _ := CardToContact(card)
	if c.TrustZone != "known" {
		t.Errorf("TrustZone = %q, want %q when missing", c.TrustZone, "known")
	}
}

func TestCardToContact_MultiValuePropertiesWithParams(t *testing.T) {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Test User")

	card.Add(vcard.FieldEmail, &vcard.Field{
		Value: "work@example.com",
		Params: vcard.Params{
			vcard.ParamType:      {"work"},
			vcard.ParamPreferred: {"1"},
		},
	})
	card.Add(vcard.FieldEmail, &vcard.Field{
		Value: "home@example.com",
		Params: vcard.Params{
			vcard.ParamType: {"home"},
		},
	})

	_, props := CardToContact(card)

	var workEmail, homeEmail *Property
	for i := range props {
		if props[i].Property == "EMAIL" {
			switch props[i].Value {
			case "work@example.com":
				workEmail = &props[i]
			case "home@example.com":
				homeEmail = &props[i]
			}
		}
	}

	if workEmail == nil {
		t.Fatal("work email not found in properties")
	}
	if workEmail.Type != "work" {
		t.Errorf("work email Type = %q, want %q", workEmail.Type, "work")
	}
	if workEmail.Pref != 1 {
		t.Errorf("work email Pref = %d, want 1", workEmail.Pref)
	}

	if homeEmail == nil {
		t.Fatal("home email not found in properties")
	}
	if homeEmail.Type != "home" {
		t.Errorf("home email Type = %q, want %q", homeEmail.Type, "home")
	}
}

func TestCardToContact_IMPPWithScheme(t *testing.T) {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Signal User")
	card.Add(vcard.FieldIMPP, &vcard.Field{
		Value:  "signal:+15551234567",
		Params: make(vcard.Params),
	})

	_, props := CardToContact(card)

	found := false
	for _, p := range props {
		if p.Property == "IMPP" && p.Value == "signal:+15551234567" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected IMPP property with signal scheme, got: %+v", props)
	}
}

func TestContactToCard_EncodesValidVCard(t *testing.T) {
	c := &Contact{
		ID:            uuid.New(),
		Kind:          "individual",
		FormattedName: "Encode Test",
		TrustZone:     "known",
		Rev:           time.Now().UTC().Format(time.RFC3339),
		Properties: []Property{
			{Property: "EMAIL", Value: "test@example.com", Type: "work"},
		},
	}

	card := ContactToCard(c)

	// Verify it encodes without error.
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		t.Fatalf("failed to encode vCard: %v", err)
	}

	// Verify it decodes back.
	decoded, err := vcard.NewDecoder(&buf).Decode()
	if err != nil {
		t.Fatalf("failed to decode vCard: %v", err)
	}
	if decoded.Value(vcard.FieldFormattedName) != "Encode Test" {
		t.Errorf("decoded FN = %q, want %q", decoded.Value(vcard.FieldFormattedName), "Encode Test")
	}
}

func TestEncodeVCard(t *testing.T) {
	c := &Contact{
		ID:            uuid.New(),
		Kind:          "individual",
		FormattedName: "VCF Test",
		TrustZone:     "trusted",
		Properties: []Property{
			{Property: "EMAIL", Value: "test@example.com"},
		},
	}

	text, err := EncodeVCard(c)
	if err != nil {
		t.Fatalf("EncodeVCard: %v", err)
	}
	if !strings.Contains(text, "BEGIN:VCARD") {
		t.Error("encoded vCard should contain BEGIN:VCARD")
	}
	if !strings.Contains(text, "VCF Test") {
		t.Error("encoded vCard should contain formatted name")
	}
}

func TestEncodeVCards_Multiple(t *testing.T) {
	contacts := []*Contact{
		{ID: uuid.New(), Kind: "individual", FormattedName: "Alice", TrustZone: "trusted"},
		{ID: uuid.New(), Kind: "individual", FormattedName: "Bob", TrustZone: "known"},
	}

	text, err := EncodeVCards(contacts)
	if err != nil {
		t.Fatalf("EncodeVCards: %v", err)
	}
	if count := strings.Count(text, "BEGIN:VCARD"); count != 2 {
		t.Errorf("expected 2 BEGIN:VCARD blocks, got %d", count)
	}
}

func TestDecodeVCards(t *testing.T) {
	vcardText := `BEGIN:VCARD
VERSION:4.0
FN:Alice Smith
KIND:individual
N:Smith;Alice;;;
EMAIL:alice@example.com
END:VCARD
BEGIN:VCARD
VERSION:4.0
FN:Bob Jones
KIND:individual
N:Jones;Bob;;;
TEL:+15551234567
END:VCARD
`
	contacts, allProps, err := DecodeVCards(strings.NewReader(vcardText))
	if err != nil {
		t.Fatalf("DecodeVCards: %v", err)
	}
	if len(contacts) != 2 {
		t.Fatalf("expected 2 contacts, got %d", len(contacts))
	}
	if contacts[0].FormattedName != "Alice Smith" {
		t.Errorf("contacts[0].FormattedName = %q, want %q", contacts[0].FormattedName, "Alice Smith")
	}
	if contacts[1].FormattedName != "Bob Jones" {
		t.Errorf("contacts[1].FormattedName = %q, want %q", contacts[1].FormattedName, "Bob Jones")
	}

	// Alice should have 1 EMAIL property.
	emailCount := 0
	for _, p := range allProps[0] {
		if p.Property == "EMAIL" {
			emailCount++
		}
	}
	if emailCount != 1 {
		t.Errorf("expected 1 EMAIL for Alice, got %d", emailCount)
	}

	// Bob should have 1 TEL property.
	telCount := 0
	for _, p := range allProps[1] {
		if p.Property == "TEL" {
			telCount++
		}
	}
	if telCount != 1 {
		t.Errorf("expected 1 TEL for Bob, got %d", telCount)
	}
}

func TestDecodeVCards_RoundTrip(t *testing.T) {
	original := &Contact{
		ID:            uuid.New(),
		Kind:          "individual",
		FormattedName: "Round Trip",
		GivenName:     "Round",
		FamilyName:    "Trip",
		TrustZone:     "trusted",
		Properties: []Property{
			{Property: "EMAIL", Value: "rt@example.com", Type: "work"},
		},
	}

	text, err := EncodeVCard(original)
	if err != nil {
		t.Fatalf("EncodeVCard: %v", err)
	}

	contacts, _, err := DecodeVCards(strings.NewReader(text))
	if err != nil {
		t.Fatalf("DecodeVCards: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(contacts))
	}
	if contacts[0].FormattedName != "Round Trip" {
		t.Errorf("FormattedName = %q, want %q", contacts[0].FormattedName, "Round Trip")
	}
}

func TestFilterCardForTrustZone_Unknown(t *testing.T) {
	card := buildTestCard()
	filtered := FilterCardForTrustZone(card, ZoneUnknown, nil)

	// Unknown should strip all sensitive fields.
	if len(filtered[vcard.FieldPhoto]) > 0 {
		t.Error("PHOTO should be removed for unknown")
	}
	if len(filtered[vcard.FieldEmail]) > 0 {
		t.Error("EMAIL should be removed for unknown")
	}
	if len(filtered[vcard.FieldTelephone]) > 0 {
		t.Error("TEL should be removed for unknown")
	}
	if len(filtered[vcard.FieldNote]) > 0 {
		t.Error("NOTE should be removed for unknown")
	}
	if len(filtered[vcard.FieldAddress]) > 0 {
		t.Error("ADR should be removed for unknown")
	}
	if len(filtered["X-THANE-TRUST-ZONE"]) > 0 {
		t.Error("X-THANE-TRUST-ZONE should be removed for unknown")
	}

	// FN and VERSION should still exist.
	if filtered.Value(vcard.FieldFormattedName) != "Test User" {
		t.Error("FN should be preserved for unknown")
	}
}

func TestFilterCardForTrustZone_Known(t *testing.T) {
	card := buildTestCard()
	filtered := FilterCardForTrustZone(card, ZoneKnown, nil)

	// Known keeps first EMAIL only.
	if emails := filtered[vcard.FieldEmail]; len(emails) != 1 {
		t.Errorf("expected 1 EMAIL for known, got %d", len(emails))
	}

	// TEL removed for known.
	if len(filtered[vcard.FieldTelephone]) > 0 {
		t.Error("TEL should be removed for known")
	}

	// ADR removed for known.
	if len(filtered[vcard.FieldAddress]) > 0 {
		t.Error("ADR should be removed for known")
	}

	// X-THANE-* removed for known.
	if len(filtered["X-THANE-TRUST-ZONE"]) > 0 {
		t.Error("X-THANE-* should be removed for known")
	}

	// URL preserved for known.
	if len(filtered[vcard.FieldURL]) == 0 {
		t.Error("URL should be preserved for known")
	}
}

func TestFilterCardForTrustZone_Known_TruncatesNote(t *testing.T) {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Test")
	longNote := strings.Repeat("x", 200)
	card.SetValue(vcard.FieldNote, longNote)

	filtered := FilterCardForTrustZone(card, ZoneKnown, nil)

	notes := filtered[vcard.FieldNote]
	if len(notes) == 0 {
		t.Fatal("NOTE should be present for known")
	}
	// 100 chars + "…"
	if len(notes[0].Value) > 104 {
		t.Errorf("NOTE should be truncated, got length %d", len(notes[0].Value))
	}
}

func TestFilterCardForTrustZone_Trusted(t *testing.T) {
	card := buildTestCard()
	filtered := FilterCardForTrustZone(card, ZoneTrusted, nil)

	// Trusted keeps everything.
	if len(filtered[vcard.FieldEmail]) != 2 {
		t.Errorf("expected 2 EMAILs for trusted, got %d", len(filtered[vcard.FieldEmail]))
	}
	if len(filtered[vcard.FieldTelephone]) == 0 {
		t.Error("TEL should be preserved for trusted")
	}
	if len(filtered[vcard.FieldNote]) == 0 {
		t.Error("NOTE should be preserved for trusted")
	}
}

func TestFilterCardForTrustZone_ZonePhoto_ExactMatch(t *testing.T) {
	card := buildTestCard()
	props := []Property{
		{Property: "PHOTO", Value: "https://example.com/household.jpg", Label: "household"},
		{Property: "PHOTO", Value: "https://example.com/known.jpg", Label: "known"},
	}

	filtered := FilterCardForTrustZone(card, ZoneKnown, props)

	photos := filtered[vcard.FieldPhoto]
	if len(photos) != 1 {
		t.Fatalf("expected 1 PHOTO, got %d", len(photos))
	}
	if photos[0].Value != "https://example.com/known.jpg" {
		t.Errorf("PHOTO = %q, want known photo", photos[0].Value)
	}
}

func TestFilterCardForTrustZone_ZonePhoto_CascadesUp(t *testing.T) {
	card := buildTestCard()
	props := []Property{
		{Property: "PHOTO", Value: "https://example.com/admin.jpg", Label: "admin"},
	}

	// Trusted has no specific photo, should cascade to admin.
	filtered := FilterCardForTrustZone(card, ZoneTrusted, props)

	photos := filtered[vcard.FieldPhoto]
	if len(photos) != 1 {
		t.Fatalf("expected 1 PHOTO, got %d", len(photos))
	}
	if photos[0].Value != "https://example.com/admin.jpg" {
		t.Errorf("PHOTO = %q, want admin photo (cascaded)", photos[0].Value)
	}
}

func TestFilterCardForTrustZone_ZonePhoto_UnknownStrips(t *testing.T) {
	card := buildTestCard()
	props := []Property{
		{Property: "PHOTO", Value: "https://example.com/known.jpg", Label: "known"},
	}

	filtered := FilterCardForTrustZone(card, ZoneUnknown, props)

	if len(filtered[vcard.FieldPhoto]) > 0 {
		t.Error("PHOTO should be stripped for unknown regardless of zone photos")
	}
}

// buildTestCard creates a vCard with common fields for filter tests.
func buildTestCard() vcard.Card {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldFormattedName, "Test User")
	card.SetValue(vcard.FieldNote, "Some notes about this contact")
	card.SetValue("X-THANE-TRUST-ZONE", "admin")
	card.SetValue("X-THANE-AI-SUMMARY", "Test summary")
	card.Add(vcard.FieldPhoto, &vcard.Field{
		Value:  "https://example.com/default.jpg",
		Params: vcard.Params{vcard.ParamValue: {"uri"}},
	})
	card.Add(vcard.FieldEmail, &vcard.Field{
		Value:  "work@example.com",
		Params: vcard.Params{vcard.ParamType: {"work"}},
	})
	card.Add(vcard.FieldEmail, &vcard.Field{
		Value:  "home@example.com",
		Params: vcard.Params{vcard.ParamType: {"home"}},
	})
	card.Add(vcard.FieldTelephone, &vcard.Field{
		Value:  "+15551234567",
		Params: vcard.Params{vcard.ParamType: {"cell"}},
	})
	card.Add(vcard.FieldIMPP, &vcard.Field{
		Value:  "signal:+15551234567",
		Params: make(vcard.Params),
	})
	card.Add(vcard.FieldAddress, &vcard.Field{
		Value:  ";;123 Main St;Springfield;IL;62701;US",
		Params: vcard.Params{vcard.ParamType: {"home"}},
	})
	card.Add(vcard.FieldURL, &vcard.Field{
		Value:  "https://example.com",
		Params: make(vcard.Params),
	})
	return card
}
