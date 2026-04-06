package memory

import "testing"

func TestSQLiteStoreConversationChannelBindingRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir()+"/memory.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	binding := &ChannelBinding{
		Channel:     "signal",
		Address:     "+15551234567",
		ContactID:   "contact-1",
		ContactName: "Alice Smith",
		TrustZone:   "known",
		LinkSource:  "tel",
	}
	if err := store.BindConversationChannel("signal-15551234567", binding); err != nil {
		t.Fatalf("BindConversationChannel: %v", err)
	}

	conv := store.GetConversation("signal-15551234567")
	if conv == nil {
		t.Fatal("GetConversation() = nil, want conversation")
	}
	if conv.Metadata == nil || conv.Metadata.ChannelBinding == nil {
		t.Fatalf("Conversation metadata = %#v", conv.Metadata)
	}
	got := conv.Metadata.ChannelBinding
	if got.Channel != "signal" || got.Address != "+15551234567" || got.ContactID != "contact-1" {
		t.Fatalf("ChannelBinding = %#v", got)
	}

	all := store.GetAllConversations()
	if len(all) != 1 || all[0].Metadata == nil || all[0].Metadata.ChannelBinding == nil {
		t.Fatalf("GetAllConversations() = %#v", all)
	}
	if all[0].Metadata.ChannelBinding.ContactName != "Alice Smith" {
		t.Fatalf("GetAllConversations binding = %#v", all[0].Metadata.ChannelBinding)
	}
}

func TestSQLiteStoreGetOrCreateConversation_InvalidMetadataTreatedAsNil(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir()+"/memory.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.db.Exec(`
		INSERT INTO conversations (id, created_at, updated_at, metadata)
		VALUES (?, datetime('now'), datetime('now'), ?)
	`, "broken-conv", `{"channel_binding":`)
	if err != nil {
		t.Fatalf("insert broken conversation: %v", err)
	}

	conv, err := store.GetOrCreateConversation("broken-conv")
	if err != nil {
		t.Fatalf("GetOrCreateConversation: %v", err)
	}
	if conv == nil {
		t.Fatal("GetOrCreateConversation() = nil, want conversation")
	}
	if conv.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for invalid stored metadata", conv.Metadata)
	}
}
