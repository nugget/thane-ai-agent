package email

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestParseAddress(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Address
		wantErr bool
	}{
		{"bare", "alice@example.com", Address{Address: "alice@example.com"}, false},
		{"name and address", "Alice <alice@example.com>", Address{Name: "Alice", Address: "alice@example.com"}, false},
		{"quoted name", `"Liddell, Alice" <alice@example.com>`, Address{Name: "Liddell, Alice", Address: "alice@example.com"}, false},
		{"rfc2047 name", "=?utf-8?q?Alice_M=C3=BCller?= <alice@example.com>", Address{Name: "Alice Müller", Address: "alice@example.com"}, false},
		{"angle brackets only", "<bob@example.com>", Address{Address: "bob@example.com"}, false},
		{"surrounding space", "  bob@example.com  ", Address{Address: "bob@example.com"}, false},
		{"empty", "", Address{}, true},
		{"not an address", "not-an-email", Address{}, true},
		{"unterminated", "Alice <alice@example.com", Address{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAddress(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("parseAddress(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestAddressKeyDomainString(t *testing.T) {
	a := Address{Name: "Alice", Address: "Alice@Example.COM"}
	if a.Key() != "alice@example.com" {
		t.Errorf("Key = %q", a.Key())
	}
	if a.Domain() != "example.com" {
		t.Errorf("Domain = %q", a.Domain())
	}
	if a.String() != `"Alice" <Alice@Example.COM>` {
		t.Errorf("String = %q", a.String())
	}
	bare := Address{Address: "bob@example.com"}
	if bare.String() != "bob@example.com" {
		t.Errorf("bare String = %q", bare.String())
	}
	if (Address{}).Domain() != "" || !(Address{}).IsZero() {
		t.Error("zero address should have no domain and be zero")
	}
}

func TestCollectRecipientsDedupesCaseInsensitively(t *testing.T) {
	got := collectRecipients(
		[]Address{{Name: "Alice", Address: "Alice@example.com"}, {Address: "bob@example.com"}},
		[]Address{{Address: "cc@example.com"}},
		[]Address{{Address: "bcc@example.com"}, {Address: "alice@EXAMPLE.com"}},
	)
	want := []string{"Alice@example.com", "bob@example.com", "cc@example.com", "bcc@example.com"}
	if len(got) != len(want) {
		t.Fatalf("collectRecipients = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("recipient[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if len(collectRecipients(nil, nil)) != 0 {
		t.Error("empty inputs should produce no recipients")
	}
}

func TestImapAddressDropsGroupMarkers(t *testing.T) {
	list := imapAddresses([]imap.Address{
		{Name: "Team"}, // group start: no mailbox
		{Name: "Alice", Mailbox: "alice", Host: "example.com"},
		{}, // group end
	})
	if len(list) != 1 || list[0].Address != "alice@example.com" || list[0].Name != "Alice" {
		t.Errorf("imapAddresses = %+v", list)
	}
	if imapAddresses(nil) != nil {
		t.Error("nil in should be nil out")
	}
}

func TestContainsAddress(t *testing.T) {
	list := []Address{{Address: "A@example.com"}}
	if !containsAddress(list, Address{Address: "a@example.com"}) {
		t.Error("comparison must be case-insensitive")
	}
	if containsAddress(list, Address{Address: "b@example.com"}) {
		t.Error("unrelated address must not match")
	}
}
