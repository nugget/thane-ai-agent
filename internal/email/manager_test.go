package email

import (
	"log/slog"
	"sort"
	"testing"
)

func TestNewManager(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "personal", IMAP: IMAPConfig{Host: "imap.personal.com", Port: 993, Username: "user1"}},
			{Name: "work", IMAP: IMAPConfig{Host: "imap.work.com", Port: 993, Username: "user2"}},
		},
	}

	mgr := NewManager(cfg, slog.Default())

	if mgr.Primary() != "personal" {
		t.Errorf("Primary() = %q, want %q", mgr.Primary(), "personal")
	}

	names := mgr.AccountNames()
	sort.Strings(names)
	if len(names) != 2 || names[0] != "personal" || names[1] != "work" {
		t.Errorf("AccountNames() = %v, want [personal work]", names)
	}
}

func TestManager_Account(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "personal", IMAP: IMAPConfig{Host: "imap.example.com", Port: 993, Username: "user"}},
			{Name: "work", IMAP: IMAPConfig{Host: "imap.work.com", Port: 993, Username: "user2"}},
		},
	}

	mgr := NewManager(cfg, slog.Default())

	// Named account lookup.
	client, err := mgr.Account("work")
	if err != nil {
		t.Fatalf("Account(work) error: %v", err)
	}
	if client == nil {
		t.Fatal("Account(work) returned nil client")
	}

	// Empty name returns primary.
	primary, err := mgr.Account("")
	if err != nil {
		t.Fatalf("Account('') error: %v", err)
	}
	if primary == nil {
		t.Fatal("Account('') returned nil client")
	}

	// Unknown account returns error.
	_, err = mgr.Account("nonexistent")
	if err == nil {
		t.Error("Account(nonexistent) should return error")
	}
}

func TestManager_Close(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "test", IMAP: IMAPConfig{Host: "imap.example.com", Port: 993, Username: "user"}},
		},
	}

	mgr := NewManager(cfg, slog.Default())
	// Close should not panic even with no active connections.
	mgr.Close()
}

func TestManager_EmptyConfig(t *testing.T) {
	mgr := NewManager(Config{}, slog.Default())

	if mgr.Primary() != "" {
		t.Errorf("Primary() = %q, want empty", mgr.Primary())
	}
	if len(mgr.AccountNames()) != 0 {
		t.Errorf("AccountNames() = %v, want empty", mgr.AccountNames())
	}

	_, err := mgr.Account("")
	if err == nil {
		t.Error("Account('') on empty manager should return error")
	}
}
