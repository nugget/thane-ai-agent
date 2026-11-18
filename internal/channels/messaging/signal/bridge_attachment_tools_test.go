package signal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// bindingResolver resolves every Signal sender to one fixed binding.
type bindingResolver struct {
	zone  string
	owner bool
}

func (r bindingResolver) ResolveChannelBinding(channel, address string) *memory.ChannelBinding {
	return &memory.ChannelBinding{Channel: channel, Address: address, ContactName: "Alice", TrustZone: r.zone, IsOwner: r.owner}
}

// TestAttachmentToolsGrantedOnTrustedAttachmentTurns checks which Signal
// turns start with the attachment tools active: a turn whose messages
// carry an attachment, where the attachment store exists, from the owner
// or a sender whose trust zone allows broad tool access. The tools read
// every stored attachment, so a less trusted sender never gets them.
func TestAttachmentToolsGrantedOnTrustedAttachmentTurns(t *testing.T) {
	const sender = "+15551234567"
	image := []Attachment{{ID: "att-1", ContentType: "image/jpeg", Filename: "photo.jpg"}}
	voice := []Attachment{{ID: "att-2", ContentType: "audio/aac"}}
	message := func(text string, atts []Attachment) *DataMessage {
		return &DataMessage{Message: text, Attachments: atts}
	}
	reaction := &DataMessage{Reaction: &Reaction{Emoji: "👍", TargetAuthor: sender, TargetSentTimestamp: 1700000000000}}

	tests := []struct {
		name        string
		resolver    ContactResolver // nil: sender resolves to an unknown address
		noStore     bool
		messages    []*DataMessage
		wantGranted bool
		wantSummary string // "" when no decision is recorded
	}{
		{name: "owner sends a photo", resolver: bindingResolver{zone: contacts.ZoneAdmin, owner: true}, messages: []*DataMessage{message("", image)}, wantGranted: true, wantSummary: "granted"},
		{name: "owner outside a broad zone", resolver: bindingResolver{zone: contacts.ZoneKnown, owner: true}, messages: []*DataMessage{message("", image)}, wantGranted: true, wantSummary: "granted"},
		{name: "admin", resolver: bindingResolver{zone: contacts.ZoneAdmin}, messages: []*DataMessage{message("look", image)}, wantGranted: true, wantSummary: "granted"},
		{name: "household", resolver: bindingResolver{zone: contacts.ZoneHousehold}, messages: []*DataMessage{message("", image)}, wantGranted: true, wantSummary: "granted"},
		{name: "household voice note", resolver: bindingResolver{zone: contacts.ZoneHousehold}, messages: []*DataMessage{message("", voice)}, wantGranted: true, wantSummary: "granted"},
		{name: "attachment later in a batch", resolver: bindingResolver{zone: contacts.ZoneHousehold}, messages: []*DataMessage{message("hi", nil), message("", image)}, wantGranted: true, wantSummary: "granted"},
		{name: "trusted", resolver: bindingResolver{zone: contacts.ZoneTrusted}, messages: []*DataMessage{message("", image)}, wantSummary: "withheld"},
		{name: "known", resolver: bindingResolver{zone: contacts.ZoneKnown}, messages: []*DataMessage{message("", image)}, wantSummary: "withheld"},
		{name: "contact without a zone", resolver: bindingResolver{}, messages: []*DataMessage{message("", image)}, wantSummary: "withheld"},
		{name: "unknown sender", messages: []*DataMessage{message("", image)}, wantSummary: "withheld"},
		{name: "no attachment store", resolver: bindingResolver{zone: contacts.ZoneAdmin, owner: true}, noStore: true, messages: []*DataMessage{message("", image)}},
		{name: "text only", resolver: bindingResolver{zone: contacts.ZoneAdmin, owner: true}, messages: []*DataMessage{message("hello", nil)}},
		{name: "reaction only", resolver: bindingResolver{zone: contacts.ZoneAdmin, owner: true}, messages: []*DataMessage{reaction}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			var logs syncBuffer
			cfg := BridgeConfig{
				Client: &Client{}, Runner: &testRunner{},
				Logger:   slog.New(slog.NewTextHandler(&logs, nil)),
				Resolver: tt.resolver,
			}
			if !tt.noStore {
				cfg.AttachmentStore = newTestAttachmentStore(t, filepath.Join(dir, "attachments.db"), filepath.Join(dir, "store"))
			}
			bridge := NewBridge(cfg)

			items := make([]loop.MailboxItem, 0, len(tt.messages))
			for i, dm := range tt.messages {
				ts := int64(1700000000000 + i*1000)
				dm.Timestamp = ts
				payload, err := json.Marshal(&Envelope{Source: sender, Timestamp: ts, DataMessage: dm})
				if err != nil {
					t.Fatal(err)
				}
				items = append(items, loop.MailboxItem{ID: fmt.Sprintf("signal:%d", ts), Payload: payload})
			}

			turn, err := bridge.prepareSignalMailboxTurn(context.Background(), sender, items, nil)
			if err != nil || turn == nil {
				t.Fatalf("prepareSignalMailboxTurn = %+v, %v", turn, err)
			}
			wantTags := []string{"signal"}
			if tt.wantGranted {
				wantTags = append(wantTags, attachmentsTag)
			}
			if !slices.Equal(turn.Request.InitialTags, wantTags) {
				t.Errorf("InitialTags = %v, want %v", turn.Request.InitialTags, wantTags)
			}
			got, _ := turn.Summary["attachment_tools"].(string)
			if got != tt.wantSummary {
				t.Errorf("summary attachment_tools = %q, want %q", got, tt.wantSummary)
			}
			if tt.wantSummary != "" && !strings.Contains(logs.String(), `msg="signal attachment tools `+tt.wantSummary+`"`) {
				t.Errorf("decision %q not logged:\n%s", tt.wantSummary, logs.String())
			}
		})
	}
}

// TestAttachmentsTagIsTheCatalogTag keeps the granted tag name tied to
// the catalog: a renamed tag would otherwise be granted and match no
// tools.
func TestAttachmentsTagIsTheCatalogTag(t *testing.T) {
	if _, ok := toolcatalog.LookupBuiltinTagSpec(attachmentsTag); !ok {
		t.Fatalf("tag %q is not a built-in tag in the tool catalog", attachmentsTag)
	}
	var owned []string
	for name, entry := range toolcatalog.BuiltinToolSpecs() {
		if slices.Contains(entry.Tags, attachmentsTag) {
			owned = append(owned, name)
		}
	}
	slices.Sort(owned)
	if want := []string{"attachment_describe", "attachment_list", "attachment_search"}; !slices.Equal(owned, want) {
		t.Errorf("tools tagged %q = %v, want %v", attachmentsTag, owned, want)
	}
}
