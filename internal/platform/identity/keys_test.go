package identity

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGenerateSigningKeyPair(t *testing.T) {
	key, err := GenerateSigningKeyPair("test-thane")
	if err != nil {
		t.Fatalf("GenerateSigningKeyPair: %v", err)
	}
	if len(key.PrivatePEM) == 0 {
		t.Fatal("PrivatePEM is empty")
	}
	if _, err := ssh.ParsePrivateKey(key.PrivatePEM); err != nil {
		t.Fatalf("generated private key is not parseable: %v", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key.Public))
	if err != nil {
		t.Fatalf("generated public key is not parseable: %v", err)
	}
	if got := ssh.FingerprintSHA256(pub); got != key.Fingerprint {
		t.Fatalf("fingerprint = %q, want %q", key.Fingerprint, got)
	}
}

func TestGenerateCertificateAuthority(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	ca, err := GenerateCertificateAuthority("test CA", now)
	if err != nil {
		t.Fatalf("GenerateCertificateAuthority: %v", err)
	}
	cert, err := ParseCACertificate(ca.Certificate)
	if err != nil {
		t.Fatalf("ParseCACertificate: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("generated certificate is not a CA")
	}
	if cert.Subject.CommonName != "test CA" {
		t.Fatalf("CommonName = %q, want test CA", cert.Subject.CommonName)
	}
	if got := certificateFingerprint(cert); got != ca.Fingerprint {
		t.Fatalf("fingerprint = %q, want %q", ca.Fingerprint, got)
	}

	keyBlock, _ := pem.Decode(ca.PrivatePEM)
	if keyBlock == nil {
		t.Fatal("generated CA private key missing PEM block")
	}
	if _, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err != nil {
		t.Fatalf("generated CA private key is not parseable: %v", err)
	}
}
