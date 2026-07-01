package config

import (
	"strings"
	"testing"
)

// Two distinct, valid ed25519 public keys (throwaway pairs generated for
// tests). Comment-free so tests can append their own comments to probe
// canonicalization.
const (
	testSignerKeyAlice = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGyUStZXWURqF4b7IWfSTz2W6zYz5JnXrKbcuPfGAmUo"
	testSignerKeyBob   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO+3xdUdsJA9XoATiuDErHwn2cDSIO1U1/t+BuN6P3Gv"
)

func TestValidateAllowedSigners(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		signers []AllowedSigner
		want    string // substring of expected error; "" means the entry is valid
	}{
		{
			name:    "valid minimal",
			signers: []AllowedSigner{{Principal: "alice@example.com", Key: testSignerKeyAlice}},
		},
		{
			name: "valid with label and window",
			signers: []AllowedSigner{{
				Principal:   "alice@example.com",
				Key:         testSignerKeyAlice,
				Label:       "Alice laptop",
				ValidAfter:  "2026-01-01T00:00:00Z",
				ValidBefore: "2027-01-01T00:00:00Z",
			}},
		},
		{
			name:    "missing principal",
			signers: []AllowedSigner{{Key: testSignerKeyAlice}},
			want:    "principal is required",
		},
		{
			name:    "principal with space",
			signers: []AllowedSigner{{Principal: "alice example", Key: testSignerKeyAlice}},
			want:    "whitespace or control",
		},
		{
			name:    "principal with embedded newline",
			signers: []AllowedSigner{{Principal: "alice\n@example.com", Key: testSignerKeyAlice}},
			want:    "whitespace or control",
		},
		{
			name:    "missing key",
			signers: []AllowedSigner{{Principal: "alice@example.com"}},
			want:    "key is required",
		},
		{
			name:    "malformed key",
			signers: []AllowedSigner{{Principal: "alice@example.com", Key: "definitely-not-a-key"}},
			want:    "valid SSH public key",
		},
		{
			name:    "label with control character",
			signers: []AllowedSigner{{Principal: "alice@example.com", Key: testSignerKeyAlice, Label: "bad\x07label"}},
			want:    "label must not contain control",
		},
		{
			name:    "unparseable valid_after",
			signers: []AllowedSigner{{Principal: "alice@example.com", Key: testSignerKeyAlice, ValidAfter: "yesterday"}},
			want:    "valid_after",
		},
		{
			name: "inverted validity window",
			signers: []AllowedSigner{{
				Principal:   "alice@example.com",
				Key:         testSignerKeyAlice,
				ValidAfter:  "2027-01-01T00:00:00Z",
				ValidBefore: "2026-01-01T00:00:00Z",
			}},
			want: "strictly before",
		},
		{
			name: "duplicate key blob under different principals",
			signers: []AllowedSigner{
				{Principal: "alice@example.com", Key: testSignerKeyAlice},
				{Principal: "eve@example.com", Key: testSignerKeyAlice},
			},
			want: "duplicates the key",
		},
		{
			name: "same key differing only by comment is still a duplicate",
			signers: []AllowedSigner{
				{Principal: "alice@example.com", Key: testSignerKeyAlice + " comment-a"},
				{Principal: "alice2@example.com", Key: testSignerKeyAlice + " comment-b"},
			},
			want: "duplicates the key",
		},
		{
			name: "two distinct keys are fine",
			signers: []AllowedSigner{
				{Principal: "alice@example.com", Key: testSignerKeyAlice},
				{Principal: "bob@example.com", Key: testSignerKeyBob},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateAllowedSigners("signing.allowed_signers", tc.signers)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateAllowedSigners() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAllowedSigners() = nil, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateAllowedSigners() = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestValidate_SigningBlockWired confirms the shared signing.allowed_signers
// block is reached by Config.Validate and labels errors with its path.
func TestValidate_SigningBlockWired(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Signing = SigningConfig{AllowedSigners: []AllowedSigner{{Principal: "alice example", Key: testSignerKeyAlice}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "signing.allowed_signers[0].principal") {
		t.Fatalf("Validate() = %v, want signing.allowed_signers[0].principal error", err)
	}
}

// TestValidate_PerRootAllowedSignersWired confirms per-root
// git.allowed_signers is reached by Config.Validate through validateDocRoots.
func TestValidate_PerRootAllowedSignersWired(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.DocRoots = map[string]DocumentRootConfig{
		"kb": {Git: DocumentRootGitConfig{AllowedSigners: []AllowedSigner{{Principal: "bob@example.com", Key: "not-a-key"}}}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "doc_roots.kb.git.allowed_signers[0].key") {
		t.Fatalf("Validate() = %v, want doc_roots.kb.git.allowed_signers[0].key error", err)
	}
}

// TestValidate_ValidAllowedSignersPass confirms a well-formed shared block and
// per-root block validate cleanly end-to-end.
func TestValidate_ValidAllowedSignersPass(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Signing = SigningConfig{AllowedSigners: []AllowedSigner{{Principal: "alice@example.com", Key: testSignerKeyAlice, Label: "Alice laptop"}}}
	cfg.DocRoots = map[string]DocumentRootConfig{
		"kb": {Git: DocumentRootGitConfig{AllowedSigners: []AllowedSigner{{Principal: "bob@example.com", Key: testSignerKeyBob}}}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}
