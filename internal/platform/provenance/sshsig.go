// Package provenance provides a git-backed file store with SSH
// signature enforcement. Files written through a [Store] are
// automatically committed with cryptographic signatures, providing
// provenance tracking, tamper detection, and audit history.
//
// Identity files (ego.md, metacognitive.md) are the first clients, but
// the package is general-purpose — any file needing integrity
// guarantees can be managed through a Store.
package provenance

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// sshsigNamespace is the namespace used for git commit signing per the
// OpenSSH PROTOCOL.sshsig specification. It prevents cross-protocol
// signature reuse.
const sshsigNamespace = "git"

// sshsigVersion is the current sshsig format version.
const sshsigVersion = 1

// sshsigMagic is the six-byte preamble for sshsig blobs.
var sshsigMagic = []byte("SSHSIG")

// sshsigSign produces an armored SSH signature over payload using the
// given [ssh.Signer]. The output matches the format produced by
// ssh-keygen -Y sign and is accepted by git verify-commit when
// gpg.format=ssh.
func sshsigSign(signer ssh.Signer, payload []byte) ([]byte, error) {
	// Hash the message with SHA-512.
	h := sha512.Sum512(payload)

	// Build the signed-data blob per PROTOCOL.sshsig:
	//   magic "SSHSIG" || namespace || reserved || hash_algorithm || H(message)
	signedData := marshalSignedData(h[:])

	// Sign the blob.
	sig, err := signer.Sign(rand.Reader, signedData)
	if err != nil {
		return nil, fmt.Errorf("sshsig: sign: %w", err)
	}

	// Build the output blob:
	//   magic || version(uint32) || public_key || namespace || reserved || hash_algorithm || signature
	blob := marshalSigBlob(signer.PublicKey(), sig)

	// Armor with PEM-like envelope.
	return armor(blob), nil
}

// marshalSignedData constructs the byte sequence that gets signed.
func marshalSignedData(hash []byte) []byte {
	var buf []byte
	buf = append(buf, sshsigMagic...)
	buf = appendString(buf, sshsigNamespace)
	buf = appendString(buf, "") // reserved
	buf = appendString(buf, "sha512")
	buf = appendString(buf, string(hash))
	return buf
}

// marshalSigBlob constructs the full signature blob.
func marshalSigBlob(pubKey ssh.PublicKey, sig *ssh.Signature) []byte {
	var buf []byte
	buf = append(buf, sshsigMagic...)
	buf = appendUint32(buf, sshsigVersion)
	buf = appendString(buf, string(pubKey.Marshal()))
	buf = appendString(buf, sshsigNamespace)
	buf = appendString(buf, "") // reserved
	buf = appendString(buf, "sha512")
	buf = appendString(buf, string(ssh.Marshal(sig)))
	return buf
}

// armor wraps a binary blob in the SSH signature PEM envelope.
func armor(blob []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(blob)

	// Wrap at 76 characters per line.
	var wrapped []byte
	wrapped = append(wrapped, "-----BEGIN SSH SIGNATURE-----\n"...)
	for len(encoded) > 0 {
		end := min(76, len(encoded))
		wrapped = append(wrapped, encoded[:end]...)
		wrapped = append(wrapped, '\n')
		encoded = encoded[end:]
	}
	wrapped = append(wrapped, "-----END SSH SIGNATURE-----"...)
	return wrapped
}

// appendString appends an SSH-style length-prefixed string.
func appendString(buf []byte, s string) []byte {
	buf = appendUint32(buf, uint32(len(s)))
	return append(buf, s...)
}

// appendUint32 appends a big-endian uint32.
func appendUint32(buf []byte, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return append(buf, b[:]...)
}
