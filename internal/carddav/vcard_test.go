package carddav

import (
	"bytes"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/google/uuid"

	"github.com/nugget/thane-ai-agent/internal/contacts"
)

func TestContactToCard_FullRoundTrip(t *testing.T) {
	id := uuid.New()
	c := &contacts.Contact{
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
		Properties: []contacts.Property{
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
	c := &contacts.Contact{
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
	original := &contacts.Contact{
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
		Properties: []contacts.Property{
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

	var workEmail, homeEmail *contacts.Property
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

func TestObjectPath(t *testing.T) {
	id := uuid.MustParse("01234567-89ab-cdef-0123-456789abcdef")
	got := objectPath(id)
	want := "/carddav/default/01234567-89ab-cdef-0123-456789abcdef.vcf"
	if got != want {
		t.Errorf("objectPath() = %q, want %q", got, want)
	}
}

func TestContactIDFromPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{
			name: "valid path",
			path: "/carddav/default/01234567-89ab-cdef-0123-456789abcdef.vcf",
			want: "01234567-89ab-cdef-0123-456789abcdef",
		},
		{
			name:    "invalid UUID",
			path:    "/carddav/default/not-a-uuid.vcf",
			wantErr: true,
		},
		{
			name: "no extension still parses",
			path: "/carddav/default/01234567-89ab-cdef-0123-456789abcdef",
			want: "01234567-89ab-cdef-0123-456789abcdef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := contactIDFromPath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("contactIDFromPath() = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestContactToCard_EncodesValidVCard(t *testing.T) {
	c := &contacts.Contact{
		ID:            uuid.New(),
		Kind:          "individual",
		FormattedName: "Encode Test",
		TrustZone:     "known",
		Rev:           time.Now().UTC().Format(time.RFC3339),
		Properties: []contacts.Property{
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
