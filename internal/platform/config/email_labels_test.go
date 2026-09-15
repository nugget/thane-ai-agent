package config

import (
	"fmt"
	"strings"
	"testing"
)

// TestEmailLabelsValidate pins the label vocabulary's rules: a meaning
// and a keyword or colour on every label, IMAP atom keywords that are
// neither system flags nor colour bits, one label per colour and per
// keyword, one known apply rule, and at most eight labels.
func TestEmailLabelsValidate(t *testing.T) {
	contact := EmailLabelConfig{Meaning: "The sender matches a contact record", Keyword: "thane-contact", Color: "blue", Apply: EmailLabelApplyContactMatched}
	nine := make(map[string]EmailLabelConfig)
	for i := range 9 {
		nine[fmt.Sprintf("l%d", i)] = EmailLabelConfig{Meaning: "m", Keyword: fmt.Sprintf("k%d", i)}
	}
	tests := []struct {
		name    string
		labels  map[string]EmailLabelConfig
		wantErr string
	}{
		{"no labels", nil, ""},
		{"the contact label", map[string]EmailLabelConfig{"contact": contact}, ""},
		{"keyword only", map[string]EmailLabelConfig{"later": {Meaning: "Read later", Keyword: "thane-later"}}, ""},
		{"colour only", map[string]EmailLabelConfig{"urgent": {Meaning: "Urgent", Color: "red"}}, ""},
		{"colour case and spaces are folded", map[string]EmailLabelConfig{"x": {Meaning: "m", Color: " Blue "}}, ""},
		{"meaning is required", map[string]EmailLabelConfig{"x": {Keyword: "k"}}, "email.labels.x: meaning is required"},
		{"meaning over its budget", map[string]EmailLabelConfig{"x": {Meaning: strings.Repeat("m", 201), Keyword: "k"}}, "meaning is 201 bytes, over the 200-byte limit"},
		{"neither keyword nor colour", map[string]EmailLabelConfig{"x": {Meaning: "m"}}, "set keyword, color, or both"},
		{"unknown colour", map[string]EmailLabelConfig{"x": {Meaning: "m", Color: "gray"}}, `color "gray" is not one of red, orange, yellow, green, blue, purple, grey`},
		{"one label per colour", map[string]EmailLabelConfig{"a": {Meaning: "m", Color: "blue"}, "b": {Meaning: "m", Color: "blue"}}, "email.labels.b: color blue is also label a's"},
		{"keywords ignore case", map[string]EmailLabelConfig{"a": {Meaning: "m", Keyword: "Thane-X"}, "b": {Meaning: "m", Keyword: "thane-x"}}, `email.labels.b: keyword "thane-x" is also label a's`},
		{"system flag keyword", map[string]EmailLabelConfig{"x": {Meaning: "m", Keyword: `\Flagged`}}, "IMAP system flag"},
		{"colour bit keyword", map[string]EmailLabelConfig{"x": {Meaning: "m", Keyword: "$mailflagbit2"}}, "flag-colour keyword"},
		{"space in keyword", map[string]EmailLabelConfig{"x": {Meaning: "m", Keyword: "thane contact"}}, "is not an IMAP keyword"},
		{"atom special in keyword", map[string]EmailLabelConfig{"x": {Meaning: "m", Keyword: "thane]"}}, "is not an IMAP keyword"},
		{"wildcard in keyword", map[string]EmailLabelConfig{"x": {Meaning: "m", Keyword: "thane*"}}, "is not an IMAP keyword"},
		{"non-ASCII keyword", map[string]EmailLabelConfig{"x": {Meaning: "m", Keyword: "thané"}}, "is not an IMAP keyword"},
		{"keyword over its budget", map[string]EmailLabelConfig{"x": {Meaning: "m", Keyword: strings.Repeat("k", 65)}}, "over the 64-byte limit"},
		{"unknown apply rule", map[string]EmailLabelConfig{"x": {Meaning: "m", Keyword: "k", Apply: "sender_known"}}, `apply "sender_known" is not a rule`},
		{"name must be lowercase", map[string]EmailLabelConfig{"Contact": {Meaning: "m", Keyword: "k"}}, "label name uses lowercase letters"},
		{"name may not start with a digit", map[string]EmailLabelConfig{"1st": {Meaning: "m", Keyword: "k"}}, "label name uses lowercase letters"},
		{"at most eight labels", nine, "declares 9 labels, over the limit of 8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := EmailConfig{
				Accounts: []EmailAccountConfig{{Name: "personal", IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice@example.com"}}},
				Labels:   tt.labels,
			}
			cfg.ApplyDefaults()
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "email.labels") {
				t.Fatalf("Validate() = %v, want an error under email.labels containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestEmailLabelKeywordsClientsAlreadyRead pins the keywords a label may
// not write because clients and servers already give them meaning.
func TestEmailLabelKeywordsClientsAlreadyRead(t *testing.T) {
	tests := []struct {
		keyword string
		wantErr string
	}{
		{"$Junk", "begins with $"},
		{"$notjunk", "begins with $"},
		{"$Forwarded", "begins with $"},
		{"$Phishing", "begins with $"},
		{"Junk", "junk filters read and write"},
		{"nonjunk", "junk filters read and write"},
		{"NotJunk", "junk filters read and write"},
		{"thane-junk-review", ""},
		{"thane-contact", ""},
	}
	for _, tt := range tests {
		t.Run(tt.keyword, func(t *testing.T) {
			err := ValidateEmailKeyword(tt.keyword)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateEmailKeyword(%q) = %v, want nil", tt.keyword, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateEmailKeyword(%q) = %v, want an error containing %q", tt.keyword, err, tt.wantErr)
			}
		})
	}
}

// TestEmailAccountLabelsValidate pins mailbox.labels: each name declared
// under email.labels, listed once, and none on an account whose access
// is read.
func TestEmailAccountLabelsValidate(t *testing.T) {
	contact := EmailLabelConfig{Meaning: "The sender matches a contact record", Keyword: "thane-contact", Color: "blue", Apply: EmailLabelApplyContactMatched}
	tests := []struct {
		name    string
		labels  map[string]EmailLabelConfig
		access  string
		carried []string
		wantErr string
	}{
		{"an account without labels", map[string]EmailLabelConfig{"contact": contact}, "", nil, ""},
		{"an account carrying a declared label", map[string]EmailLabelConfig{"contact": contact}, EmailAccessOrganize, []string{" contact "}, ""},
		{"an undeclared label", map[string]EmailLabelConfig{"contact": contact}, "", []string{"urgent"}, `mailbox.labels names "urgent", which email.labels does not declare (declared: contact)`},
		{"no vocabulary at all", nil, "", []string{"contact"}, "(declared: none)"},
		{"a label named twice", map[string]EmailLabelConfig{"contact": contact}, "", []string{"contact", "contact"}, `names "contact" twice`},
		{"labels on a read-only account", map[string]EmailLabelConfig{"contact": contact}, EmailAccessRead, []string{"contact"}, "the account's access is read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acct := EmailAccountConfig{Name: "personal", IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice@example.com"}}
			acct.Policy.Access = tt.access
			acct.Mailbox.Labels = tt.carried
			cfg := EmailConfig{Accounts: []EmailAccountConfig{acct}, Labels: tt.labels}
			cfg.ApplyDefaults()
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "email.accounts[0] (personal): mailbox.labels") {
				t.Fatalf("Validate() = %v, want an error under mailbox.labels containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestEmailLabelsDefaultsNormalize pins ApplyDefaults on a label: fields
// trimmed, colour and apply lowercased, and the keyword kept as spelled.
func TestEmailLabelsDefaultsNormalize(t *testing.T) {
	cfg := EmailConfig{Labels: map[string]EmailLabelConfig{"contact": {Meaning: " m ", Keyword: " Thane-Contact ", Color: " BLUE ", Apply: " Contact_Matched "}}}
	cfg.ApplyDefaults()
	got := cfg.Labels["contact"]
	want := EmailLabelConfig{Meaning: "m", Keyword: "Thane-Contact", Color: "blue", Apply: EmailLabelApplyContactMatched}
	if got != want {
		t.Errorf("normalized label = %+v, want %+v", got, want)
	}
}
