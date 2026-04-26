package provenance

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
				"use an unencrypted key or load via ssh-agent", keyPath)
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

	// LastMessage is the commit message of the most recent commit
	// (typically a loop name or conversation ID).
	LastMessage string

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
//
// Store is safe for concurrent use; a mutex serializes git operations
// to prevent interleaved add/commit sequences from corrupting the
// repository.
type Store struct {
	mu                 sync.Mutex
	path               string
	signer             Signer
	logger             *slog.Logger
	allowedSignersPath string
}

// Options configures optional provenance store behavior.
type Options struct {
	// AllowedSignersPath points git signature verification at an
	// existing OpenSSH allowed signers file. Empty writes a
	// repository-local .allowed_signers file containing the signing key.
	AllowedSignersPath string
}

// New creates a [Store] backed by a git repository at path. If the
// directory does not exist or is not yet a git repository, New
// initializes it (git init, user config, .allowed_signers). The signer
// is used for all commit signing.
func New(path string, signer Signer, logger *slog.Logger) (*Store, error) {
	return NewWithOptions(path, signer, logger, Options{})
}

// NewWithOptions creates a [Store] backed by a git repository at path
// with optional repository verification settings.
func NewWithOptions(path string, signer Signer, logger *slog.Logger, opts Options) (*Store, error) {
	if signer == nil {
		return nil, fmt.Errorf("provenance: nil signer")
	}
	if logger == nil {
		logger = slog.Default()
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: resolve path: %w", err)
	}
	allowedSignersPath := strings.TrimSpace(opts.AllowedSignersPath)
	if allowedSignersPath != "" {
		allowedSignersPath, err = filepath.Abs(allowedSignersPath)
		if err != nil {
			return nil, fmt.Errorf("provenance: resolve allowed signers path: %w", err)
		}
	}

	s := &Store{
		path:               absPath,
		signer:             signer,
		logger:             logger,
		allowedSignersPath: allowedSignersPath,
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

	s.mu.Lock()
	defer s.mu.Unlock()

	absPath := filepath.Join(s.path, filename)

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("provenance: create directory for %s: %w", filename, err)
	}

	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("provenance: write %s: %w", filename, err)
	}

	committed, err := s.commitFile(ctx, filename, message)
	if err != nil {
		return fmt.Errorf("provenance: commit %s: %w", filename, err)
	}

	if committed {
		s.logger.Info("provenance file committed",
			"file", filename,
			"bytes", len(content),
			"message", message,
		)
	}

	return nil
}

// WriteFiles writes multiple files within the store and creates one
// signed git commit containing all resulting changes. Filenames are
// relative to the store root. Parent directories are created
// automatically. If all target files already contain the requested
// content, no commit is created.
func (s *Store) WriteFiles(ctx context.Context, files map[string]string, message string) error {
	if len(files) == 0 {
		return fmt.Errorf("provenance: no files to write")
	}

	filenames := make([]string, 0, len(files))
	for filename := range files {
		if err := validateFilename(filename); err != nil {
			return fmt.Errorf("provenance: %w", err)
		}
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, filename := range filenames {
		absPath := filepath.Join(s.path, filename)
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return fmt.Errorf("provenance: create directory for %s: %w", filename, err)
		}
		if err := os.WriteFile(absPath, []byte(files[filename]), 0o644); err != nil {
			return fmt.Errorf("provenance: write %s: %w", filename, err)
		}
	}

	committed, err := s.commitFiles(ctx, filenames, message)
	if err != nil {
		return fmt.Errorf("provenance: commit files: %w", err)
	}

	if committed {
		s.logger.Info("provenance files committed",
			"files", filenames,
			"message", message,
		)
	}

	return nil
}

// Delete removes filename within the store, then creates a signed git
// commit with the given message. If the file is already absent, Delete
// returns an os.ErrNotExist-wrapped error.
func (s *Store) Delete(ctx context.Context, filename, message string) error {
	if err := validateFilename(filename); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tracked, err := s.fileTracked(ctx, filename)
	if err != nil {
		return fmt.Errorf("provenance: check tracked file %s: %w", filename, err)
	}
	if !tracked {
		return fmt.Errorf("provenance: cannot commit signed deletion for untracked file %s; write it through provenance before deleting", filename)
	}

	absPath := filepath.Join(s.path, filename)
	if err := os.Remove(absPath); err != nil {
		return fmt.Errorf("provenance: delete %s: %w", filename, err)
	}

	committed, err := s.commitFile(ctx, filename, message)
	if err != nil {
		return fmt.Errorf("provenance: commit delete %s: %w", filename, err)
	}
	if committed {
		s.logger.Info("provenance file deletion committed",
			"file", filename,
			"message", message,
		)
	}
	return nil
}

// validateFilename rejects filenames that could escape the store root
// via path traversal or absolute paths.
func validateFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("empty filename not allowed")
	}
	if filepath.IsAbs(filename) {
		return fmt.Errorf("absolute path not allowed: %s", filename)
	}
	cleaned := filepath.Clean(filename)
	if cleaned == "." {
		return fmt.Errorf("current directory filename not allowed: %s", filename)
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileHistory(ctx, filename)
}

// FilePath returns the absolute filesystem path for a file within the
// store. This is useful for code that needs to access the file directly
// (e.g., for size checks or passing to other subsystems).
func (s *Store) FilePath(filename string) string {
	return filepath.Join(s.path, filename)
}
