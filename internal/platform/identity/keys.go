// Package identity bootstraps Thane instance identity material.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"golang.org/x/crypto/ssh"
)

// SigningKeyPair contains a generated Ed25519 SSH signing key pair.
type SigningKeyPair struct {
	PrivateKey  ed25519.PrivateKey
	PrivatePEM  []byte
	Public      string
	Fingerprint string
}

// CertificateAuthority contains a generated self-signed X.509 CA.
type CertificateAuthority struct {
	PrivateKey  ed25519.PrivateKey
	PrivatePEM  []byte
	Certificate []byte
	Fingerprint string
	NotBefore   time.Time
	NotAfter    time.Time
}

// GenerateSigningKeyPair creates a new Ed25519 SSH signing key pair.
func GenerateSigningKeyPair(comment string) (*SigningKeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("encode signing public key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, fmt.Errorf("marshal signing private key: %w", err)
	}

	return &SigningKeyPair{
		PrivateKey:  priv,
		PrivatePEM:  pem.EncodeToMemory(block),
		Public:      string(ssh.MarshalAuthorizedKey(sshPub)),
		Fingerprint: ssh.FingerprintSHA256(sshPub),
	}, nil
}

// GenerateCertificateAuthority creates a self-signed Ed25519 root CA.
func GenerateCertificateAuthority(commonName string, now time.Time) (*CertificateAuthority, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate CA serial: %w", err)
	}
	if serial.Sign() == 0 {
		serial = big.NewInt(1)
	}

	notBefore := now.UTC().Truncate(time.Second)
	notAfter := notBefore.AddDate(10, 0, 0)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            2,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal CA private key: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse generated CA certificate: %w", err)
	}

	return &CertificateAuthority{
		PrivateKey: priv,
		PrivatePEM: pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: keyDER,
		}),
		Certificate: pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: der,
		}),
		Fingerprint: certificateFingerprint(cert),
		NotBefore:   notBefore,
		NotAfter:    notAfter,
	}, nil
}

func certificateFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}
