package contacts

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestAutomatedAddress(t *testing.T) {
	tests := []struct {
		address string
		want    string // "" means not automated
	}{
		// Positives, each with the pattern it matches.
		{"noreply@example.com", AutomatedNoReply},
		{"no-reply@example.com", AutomatedNoReply},
		{"no_reply@example.com", AutomatedNoReply},
		{"NoReply+abc@Example.com", AutomatedNoReply},
		{"no-reply-aws@amazon.example", AutomatedNoReply},
		{"noreply-aws@amazon.example", AutomatedNoReply},
		{"aws-noreply@amazon.example", AutomatedNoReply},
		{"info.no-reply@example.com", AutomatedNoReply},
		{"drive-shares-dm-noreply@google.example", AutomatedNoReply},
		{"  NOREPLY@example.com  ", AutomatedNoReply},
		{"donotreply@example.com", AutomatedDoNotReply},
		{"do-not-reply@example.com", AutomatedDoNotReply},
		{"do_not.reply@example.com", AutomatedDoNotReply},
		{"notifications@github.example", AutomatedNotifications},
		{"notification@example.com", AutomatedNotifications},
		{"calendar-notification@google.example", AutomatedNotifications},
		{"billing-notifications@example.com", AutomatedNotifications},
		{"mailer-daemon@example.com", AutomatedMailerDaemon},
		{"MAILER_DAEMON@example.com", AutomatedMailerDaemon},
		{"mailer.daemon@example.com", AutomatedMailerDaemon},
		{"mailerdaemon@example.com", AutomatedMailerDaemon},
		{"mailer-daemon+bounce-123@example.com", AutomatedMailerDaemon},
		{"bounces@example.com", AutomatedBounces},
		{"bounces+123@example.com", AutomatedBounces},
		{"bounce@example.com", AutomatedBounces},

		// Negative controls.
		{"alice@example.com", ""},
		{"noreen@example.com", ""},
		{"nora@example.com", ""},
		{"snoreply@example.com", ""},
		{"junoreply@example.com", ""},
		{"reply@example.com", ""},
		{"notify@example.com", ""},
		{"alerts@example.com", ""},
		{"info@example.com", ""},
		{"support@example.com", ""},
		{"postmaster@example.com", ""},
		{"mailer@example.com", ""},
		{"daemon-mailer-x@example.com", ""},
		// Each rule stops at a segment's right edge as well as its left.
		{"noreplyer@example.com", ""},
		{"no-replyer@example.com", ""},
		{"donotreplyx@example.com", ""},
		{"bouncer@example.com", ""},
		{"notificationsbot@example.com", ""},
		// mailer-daemon is the whole local part, never a run inside it.
		{"mailer-daemon-reports@example.com", ""},
		{"ops.mailer-daemon@example.com", ""},
		// Only the local part counts: the domain is never read.
		{"12345+alice@users.noreply.github.com", ""},
		{"alice@notifications.example.com", ""},
		// A tag after '+' is ignored.
		{"alice+noreply@example.com", ""},
		// Not an address at all.
		{"noreply", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got, ok := AutomatedAddress(tt.address)
			if ok != (tt.want != "") || got != tt.want {
				t.Errorf("AutomatedAddress(%q) = (%q, %v), want (%q, %v)", tt.address, got, ok, tt.want, tt.want != "")
			}
		})
	}
}

func TestCapAtKnown(t *testing.T) {
	tests := []struct {
		zone string
		want string
	}{
		{ZoneAdmin, ZoneKnown},
		{ZoneHousehold, ZoneKnown},
		{ZoneTrusted, ZoneKnown},
		{ZoneKnown, ZoneKnown},
		{ZoneUnknown, ZoneUnknown},
		// An unrecognised value fails low, as leastPrivilegedZone does.
		{"bogus", ZoneUnknown},
		{"", ZoneUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.zone, func(t *testing.T) {
			if got := CapAtKnown(tt.zone); got != tt.want {
				t.Errorf("CapAtKnown(%q) = %q, want %q", tt.zone, got, tt.want)
			}
		})
	}
}

// TestCapAtKnownNeverRaises pins the invariant against the zone
// hierarchy itself: whatever the input, the output's policy is never
// more privileged than the input's.
func TestCapAtKnownNeverRaises(t *testing.T) {
	rank := zoneRanks()
	for _, zone := range []string{ZoneAdmin, ZoneHousehold, ZoneTrusted, ZoneKnown, ZoneUnknown, "bogus", ""} {
		in := rank[Policy(zone).Zone]
		out := rank[Policy(CapAtKnown(zone)).Zone]
		if out < in {
			t.Errorf("CapAtKnown(%q) raised rank %d to %d", zone, in, out)
		}
		if out < rank[ZoneKnown] {
			t.Errorf("CapAtKnown(%q) = %q is above known", zone, CapAtKnown(zone))
		}
	}
}

// zoneRanks indexes the hierarchy: 0 is the most privileged.
func zoneRanks() map[string]int {
	rank := make(map[string]int)
	for i, p := range Policies() {
		rank[p.Zone] = i
	}
	return rank
}

func TestAutomatedAddressesAboveKnown(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	seed := func(name, zone string, props ...Property) *Contact {
		t.Helper()
		c, err := store.Upsert(&Contact{FormattedName: name, TrustZone: zone})
		if err != nil {
			t.Fatalf("Upsert %s: %v", name, err)
		}
		for i := range props {
			if err := store.AddProperty(c.ID, &props[i]); err != nil {
				t.Fatalf("AddProperty %s: %v", name, err)
			}
		}
		return c
	}
	email := func(v string) Property { return Property{Property: "EMAIL", Value: v} }

	forge := seed("Forge Notices", ZoneAdmin, email("notifications@forge.example"))
	alice := seed("Alice", ZoneHousehold, email("alice@example.com"), email("noreply@alice.example"))
	// Negative controls: a known record, a deleted trusted record, and
	// a trusted record whose automated-looking value is not an email.
	seed("Carrier", ZoneKnown, email("no-reply@carrier.example"))
	deleted := seed("Gone", ZoneTrusted, email("noreply@gone.example"))
	if err := store.Delete(deleted.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	seed("Phone Tree", ZoneTrusted, Property{Property: "TEL", Value: "noreply@phone.example"})
	seed("Bob", ZoneTrusted, email("bob@example.com"))

	want := []AutomatedAddressFinding{
		{ContactID: alice.ID, Name: "Alice", TrustZone: ZoneHousehold, Address: "noreply@alice.example", Pattern: AutomatedNoReply},
		{ContactID: forge.ID, Name: "Forge Notices", TrustZone: ZoneAdmin, Address: "notifications@forge.example", Pattern: AutomatedNotifications},
	}
	// The limit bounds the findings kept, never the count: every limit
	// reports both findings in Total and keeps the first min(limit, 2).
	for _, limit := range []int{-1, 0, 1, 2, 10} {
		audit, err := store.AutomatedAddressesAboveKnown(ctx, limit)
		if err != nil {
			t.Fatalf("AutomatedAddressesAboveKnown(%d): %v", limit, err)
		}
		if audit.Total != len(want) {
			t.Errorf("limit %d: Total = %d, want %d", limit, audit.Total, len(want))
		}
		kept := want[:min(max(limit, 0), len(want))]
		if len(audit.Findings) != len(kept) {
			t.Fatalf("limit %d: findings = %+v, want %+v", limit, audit.Findings, kept)
		}
		for i := range kept {
			if audit.Findings[i] != kept[i] {
				t.Errorf("limit %d: finding %d = %+v, want %+v", limit, i, audit.Findings[i], kept[i])
			}
		}
	}
}

// TestAutomatedAddressAuditJSON pins the snake_case contract the audit
// types carry across packages.
func TestAutomatedAddressAuditJSON(t *testing.T) {
	id := uuid.MustParse("7d2c6a4e-1111-4222-8333-944455556666")
	audit := AutomatedAddressAudit{
		Total: 3,
		Findings: []AutomatedAddressFinding{{
			ContactID: id, Name: "Forge Notices", TrustZone: ZoneAdmin,
			Address: "noreply@forge.example", Pattern: AutomatedNoReply,
		}},
	}
	data, err := json.Marshal(audit)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"total":3,"findings":[{"contact_id":"7d2c6a4e-1111-4222-8333-944455556666","contact_name":"Forge Notices","trust_zone":"admin","address":"noreply@forge.example","pattern":"no-reply"}]}`
	if string(data) != want {
		t.Errorf("JSON = %s\nwant   %s", data, want)
	}
}

func TestAutomatedAddressesAboveKnownCleanDirectory(t *testing.T) {
	store := newTestStore(t)
	c, err := store.Upsert(&Contact{FormattedName: "Bob", TrustZone: ZoneAdmin})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.AddProperty(c.ID, &Property{Property: "EMAIL", Value: "bob@example.com"}); err != nil {
		t.Fatalf("AddProperty: %v", err)
	}
	audit, err := store.AutomatedAddressesAboveKnown(context.Background(), 5)
	if err != nil || audit.Total != 0 || len(audit.Findings) != 0 {
		t.Errorf("clean directory = %+v, %v; want no findings", audit, err)
	}
}
