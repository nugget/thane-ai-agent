package carddav

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// TestPlantedRowsDoNotSurviveCardDAVRoundTrip pins the emission guard
// end to end. Rows whose names are vCard syntax (a grouped EMAIL, a
// PREF-promoted trust zone) or a codec-owned header would, if emitted,
// decode on the operator's next PUT as a live address, an admin zone and
// a Home Assistant binding. The guard withholds them, so the round trip
// keeps the record as the operator left it and the PUT drops the plants.
func TestPlantedRowsDoNotSurviveCardDAVRoundTrip(t *testing.T) {
	var logs bytes.Buffer
	b := NewBackend(newTestBackend(t).store, false, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()

	c, err := b.store.Upsert(&contacts.Contact{FormattedName: "Bob Household", Kind: "individual", TrustZone: contacts.ZoneHousehold})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []contacts.Property{
		{Property: "item1.EMAIL", Value: "mallory@example.net"},
		{Property: "X-THANE-TRUST-ZONE;PREF=1", Value: contacts.ZoneAdmin},
		{Property: "X-THANE-HA-PERSON", Value: "person.mallory"},
	} {
		if err := b.store.AddProperty(c.ID, &p); err != nil {
			t.Fatal(err)
		}
	}

	full, err := b.store.GetWithProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := b.contactToObject(full)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(obj.Card); err != nil {
		t.Fatal(err)
	}
	card, err := vcard.NewDecoder(&buf).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.PutAddressObject(ctx, objectPath(c.ID), card, nil); err != nil {
		t.Fatal(err)
	}

	got, err := b.store.GetWithProperties(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrustZone != contacts.ZoneHousehold {
		t.Errorf("zone after round trip = %q, want household", got.TrustZone)
	}
	if matches, err := b.store.FindByPropertyExact("EMAIL", "mallory@example.net"); err != nil || len(matches) != 0 {
		t.Errorf("planted address went live: %+v, %v", matches, err)
	}
	if entity, _, err := b.store.HAPersonEntity(c.ID); err != nil || entity != "" {
		t.Errorf("planted binding went live: %q, %v", entity, err)
	}
	for _, p := range got.Properties {
		if !contacts.EmittablePropertyName(p.Property) {
			t.Errorf("planted row survived the PUT: %s=%q", p.Property, p.Value)
		}
	}
	if out := logs.String(); !strings.Contains(out, "contact properties withheld from CardDAV") || !strings.Contains(out, c.ID.String()) || !strings.Contains(out, "item1.EMAIL") {
		t.Errorf("withheld rows were not logged by contact and property:\n%s", out)
	}
}
