package provenance

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Signer produces SSH signatures for git commit objects. Implementations
// hold key material in memory so signing never requires disk I/O after
// construction.
type Signer interface {
	// Sign produces an armored SSH signature over payload using the
	// sshsig format. The output is suitable for embedding in a git
	// commit object's gpgsig header.
	Sign(payload []byte) (armoredSig []byte, err error)

	// PublicKey returns the SSH public key in authorized_keys format
	// (e.g., "ssh-ed25519 AAAA..."). Used to populate the
	// .allowed_signers file.
	PublicKey() string
}

// SSHFileSigner loads an SSH private key from disk at construction time
// and holds the parsed [ssh.Signer] in memory. Supports both
// unencrypted and passphrase-encrypted keys (encrypted keys require
// terminal interaction at startup).
type SSHFileSigner struct {
	signer ssh.Signer
	pubKey string
}

// NewSSHFileSigner reads the SSH private key at keyPath and returns a
// [Signer] backed by the parsed key. If the key is encrypted,
// [ssh.ParsePrivateKey] returns a [*ssh.PassphraseMissingError] and the
// caller must handle passphrase prompting.
func NewSSHFileSigner(keyPath string) (*SSHFileSigner, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("provenance: read signing key %s: %w", keyPath, err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		var ppErr *ssh.PassphraseMissingError
		if errors.As(err, &ppErr) {
			return nil, fmt.Errorf("provenance: signing key %s is passphrase-protected; "+
				"use an unencrypted key or configure provenance.passphrase", keyPath)
		}
		return nil, fmt.Errorf("provenance: parse signing key: %w", err)
	}

	pubKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))

	return &SSHFileSigner{
		signer: signer,
		pubKey: pubKey,
	}, nil
}

// NewSSHFileSignerWithPassphrase reads an encrypted SSH private key at
// keyPath and decrypts it with the given passphrase.
func NewSSHFileSignerWithPassphrase(keyPath string, passphrase []byte) (*SSHFileSigner, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("provenance: read signing key %s: %w", keyPath, err)
	}

	signer, err := ssh.ParsePrivateKeyWithPassphrase(keyBytes, passphrase)
	if err != nil {
		return nil, fmt.Errorf("provenance: parse encrypted signing key: %w", err)
	}

	pubKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))

	return &SSHFileSigner{
		signer: signer,
		pubKey: pubKey,
	}, nil
}

// NewSSHSignerFromKey wraps an existing [ed25519.PrivateKey] as a
// [Signer]. Useful for testing without touching the filesystem.
func NewSSHSignerFromKey(key ed25519.PrivateKey) (*SSHFileSigner, error) {
	signer, err := ssh.NewSignerFromSigner(key)
	if err != nil {
		return nil, fmt.Errorf("provenance: wrap ed25519 key: %w", err)
	}

	pubKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))

	return &SSHFileSigner{
		signer: signer,
		pubKey: pubKey,
	}, nil
}

// Sign produces an armored SSH signature over payload.
func (s *SSHFileSigner) Sign(payload []byte) ([]byte, error) {
	return sshsigSign(s.signer, payload)
}

// PublicKey returns the SSH public key in authorized_keys format.
func (s *SSHFileSigner) PublicKey() string {
	return s.pubKey
}

// FileHistory describes the git history of a file in the store.
type FileHistory struct {
	// LastModified is the timestamp of the most recent commit touching
	// this file.
	LastModified time.Time

	// LastAuthor is the commit message of the most recent commit
	// (typically a loop name or conversation ID).
	LastAuthor string

	// RevisionCount is the total number of commits touching this file.
	RevisionCount int

	// RecentEdits contains the last few commits, newest first.
	RecentEdits []EditEntry
}

// EditEntry represents a single commit in a file's history.
type EditEntry struct {
	Hash      string
	Message   string
	Timestamp time.Time
}

// Store wraps a git repository to provide signed, auditable file
// management. Every [Store.Write] creates a signed git commit. Read and
// History operations query the repository without modification.
type Store struct {
	path   string
	signer Signer
	logger *slog.Logger
}

// New creates a [Store] backed by a git repository at path. If the
// directory does not exist or is not yet a git repository, New
// initializes it (git init, user config, .allowed_signers). The signer
// is used for all commit signing.
func New(path string, signer Signer, logger *slog.Logger) (*Store, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: resolve path: %w", err)
	}

	s := &Store{
		path:   absPath,
		signer: signer,
		logger: logger,
	}

	if err := s.ensureRepo(); err != nil {
		return nil, fmt.Errorf("provenance: init repository: %w", err)
	}

	return s, nil
}

// Path returns the absolute path to the store's git repository.
func (s *Store) Path() string {
	return s.path
}

// Write writes content to filename within the store, then creates a
// signed git commit with the given message. The filename is relative to
// the store root (e.g., "ego.md", "metacognitive.md"). Parent
// directories are created automatically. Filenames containing path
// traversal components (e.g., "..") or absolute paths are rejected.
func (s *Store) Write(ctx context.Context, filename, content, message string) error {
	if err := validateFilename(filename); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}

	absPath := filepath.Join(s.path, filename)

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("provenance: create directory for %s: %w", filename, err)
	}

	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("provenance: write %s: %w", filename, err)
	}

	if err := s.commitFile(ctx, filename, message); err != nil {
		return fmt.Errorf("provenance: commit %s: %w", filename, err)
	}

	s.logger.Info("provenance file committed",
		"file", filename,
		"bytes", len(content),
		"message", message,
	)

	return nil
}

// validateFilename rejects filenames that could escape the store root
// via path traversal or absolute paths.
func validateFilename(filename string) error {
	if filepath.IsAbs(filename) {
		return fmt.Errorf("absolute path not allowed: %s", filename)
	}
	cleaned := filepath.Clean(filename)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path traversal not allowed: %s", filename)
	}
	return nil
}

// Read returns the content of filename within the store.
func (s *Store) Read(filename string) (string, error) {
	absPath := filepath.Join(s.path, filename)

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("provenance: read %s: %w", filename, err)
	}

	return string(data), nil
}

// History returns git metadata for filename. If the file has no commits
// yet, History returns a zero-value FileHistory with no error.
func (s *Store) History(ctx context.Context, filename string) (*FileHistory, error) {
	return s.fileHistory(ctx, filename)
}

// FilePath returns the absolute filesystem path for a file within the
// store. This is useful for code that needs to access the file directly
// (e.g., for size checks or passing to other subsystems).
func (s *Store) FilePath(filename string) string {
	return filepath.Join(s.path, filename)
}
