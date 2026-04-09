package messages

import (
	"strings"
	"testing"
	"time"
)

func TestEnvelopeNormalizeTrimsTargetAndClonesBinaryFields(t *testing.T) {
	t.Parallel()

	sig := []byte{1, 2, 3}
	cert := []byte{4, 5, 6}
	got, err := (Envelope{
		From: Identity{Kind: IdentityCore, Name: " core "},
		To: Destination{
			Kind:   DestinationLoop,
			Target: "  battery-watch  ",
		},
		Type:       TypeSignal,
		Signature:  sig,
		SignerCert: cert,
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	sig[0] = 9
	cert[0] = 9

	if got.To.Target != "battery-watch" {
		t.Fatalf("target = %q, want trimmed target", got.To.Target)
	}
	if got.Signature[0] != 1 {
		t.Fatalf("signature = %v, want cloned data", got.Signature)
	}
	if got.SignerCert[0] != 4 {
		t.Fatalf("signer_cert = %v, want cloned data", got.SignerCert)
	}
}

func TestEnvelopeNormalizeRejectsUnsupportedIdentityKind(t *testing.T) {
	t.Parallel()

	_, err := (Envelope{
		From: Identity{Kind: IdentityKind("typo")},
		To: Destination{
			Kind:   DestinationLoop,
			Target: "battery-watch",
		},
		Type: TypeSignal,
	}).Normalize(time.Now())
	if err == nil || !strings.Contains(err.Error(), "unsupported identity kind") {
		t.Fatalf("Normalize err = %v, want unsupported identity kind", err)
	}
}
