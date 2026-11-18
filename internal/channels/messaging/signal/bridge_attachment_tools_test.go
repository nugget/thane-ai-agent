package signal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
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

// TestAttachmentToolsGrantedMidTurn checks the same rule applied to
// messages merged into a running turn: the first attachment a pull
// delivers decides once, a grant reaches the live run through the pull
// context's tag requester, a turn the waking messages already decided is
// left alone, and a grant that cannot be applied is recorded and warned.
func TestAttachmentToolsGrantedMidTurn(t *testing.T) {
	const sender = "+15551234567"
	image := []Attachment{{ID: "att-1", ContentType: "image/jpeg", Filename: "photo.jpg"}}
	voice := []Attachment{{ID: "att-2", ContentType: "audio/aac"}}
	text := func(s string) *DataMessage { return &DataMessage{Message: s} }
	with := func(atts []Attachment) *DataMessage { return &DataMessage{Attachments: atts} }
	reaction := &DataMessage{Reaction: &Reaction{Emoji: "👍", TargetAuthor: sender, TargetSentTimestamp: 1700000000000}}
	owner := bindingResolver{zone: contacts.ZoneAdmin, owner: true}

	tests := []struct {
		name         string
		resolver     ContactResolver
		noStore      bool
		wake         *DataMessage
		pulls        [][]*DataMessage
		requester    string // "ok", "fail", or "" for no requester on the pull context
		wantRequests int
		wantSummary  string // final attachment_tools ("" when no decision)
		wantMidTurn  bool
		wantWarn     bool
	}{
		{name: "owner sends a photo mid-turn", resolver: owner, wake: text("look"), pulls: [][]*DataMessage{{with(image)}}, requester: "ok", wantRequests: 1, wantSummary: "granted", wantMidTurn: true},
		{name: "household voice note mid-turn", resolver: bindingResolver{zone: contacts.ZoneHousehold}, wake: text("hi"), pulls: [][]*DataMessage{{with(voice)}}, requester: "ok", wantRequests: 1, wantSummary: "granted", wantMidTurn: true},
		{name: "text pull then photo pull", resolver: bindingResolver{zone: contacts.ZoneAdmin}, wake: text("hi"), pulls: [][]*DataMessage{{text("one sec")}, {with(image)}}, requester: "ok", wantRequests: 1, wantSummary: "granted", wantMidTurn: true},
		{name: "two photo pulls decide once", resolver: owner, wake: text("hi"), pulls: [][]*DataMessage{{with(image)}, {with(image)}}, requester: "ok", wantRequests: 1, wantSummary: "granted", wantMidTurn: true},
		{name: "trusted", resolver: bindingResolver{zone: contacts.ZoneTrusted}, wake: text("hi"), pulls: [][]*DataMessage{{with(image)}}, requester: "ok", wantSummary: "withheld", wantMidTurn: true},
		{name: "known", resolver: bindingResolver{zone: contacts.ZoneKnown}, wake: text("hi"), pulls: [][]*DataMessage{{with(image)}}, requester: "ok", wantSummary: "withheld", wantMidTurn: true},
		{name: "contact without a zone", resolver: bindingResolver{}, wake: text("hi"), pulls: [][]*DataMessage{{with(image)}}, requester: "ok", wantSummary: "withheld", wantMidTurn: true},
		{name: "unknown sender", wake: text("hi"), pulls: [][]*DataMessage{{with(image)}}, requester: "ok", wantSummary: "withheld", wantMidTurn: true},
		{name: "no attachment store", resolver: owner, noStore: true, wake: text("hi"), pulls: [][]*DataMessage{{with(image)}}, requester: "ok"},
		{name: "text-only pull", resolver: owner, wake: text("hi"), pulls: [][]*DataMessage{{text("more")}}, requester: "ok"},
		{name: "reaction-only pull", resolver: owner, wake: text("hi"), pulls: [][]*DataMessage{{reaction}}, requester: "ok"},
		{name: "waking photo already granted", resolver: owner, wake: with(image), pulls: [][]*DataMessage{{with(image)}}, requester: "ok", wantSummary: "granted"},
		{name: "waking photo already withheld", resolver: bindingResolver{zone: contacts.ZoneTrusted}, wake: with(image), pulls: [][]*DataMessage{{with(image)}}, requester: "ok", wantSummary: "withheld"},
		{name: "requester refuses", resolver: owner, wake: text("hi"), pulls: [][]*DataMessage{{with(image)}}, requester: "fail", wantRequests: 1, wantSummary: "grant_failed", wantMidTurn: true, wantWarn: true},
		{name: "no requester on the pull context", resolver: owner, wake: text("hi"), pulls: [][]*DataMessage{{with(image)}}, wantSummary: "grant_failed", wantMidTurn: true, wantWarn: true},
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

			seq := int64(0)
			item := func(dm *DataMessage) loop.MailboxItem {
				seq++
				ts := 1700000000000 + seq*1000
				copied := *dm
				copied.Timestamp = ts
				payload, err := json.Marshal(&Envelope{Source: sender, Timestamp: ts, DataMessage: &copied})
				if err != nil {
					t.Fatal(err)
				}
				return loop.MailboxItem{ID: fmt.Sprintf("signal:%d", ts), Payload: payload}
			}

			turn, err := bridge.prepareSignalMailboxTurn(context.Background(), sender, []loop.MailboxItem{item(tt.wake)}, nil)
			if err != nil || turn == nil || turn.PullRender == nil {
				t.Fatalf("prepareSignalMailboxTurn = %+v, %v", turn, err)
			}
			_, wakeDecided := turn.Summary["attachment_tools"]

			var requests []string
			ctx := context.Background()
			switch tt.requester {
			case "ok":
				ctx = agentctx.WithTagRequester(ctx, func(tag string) error { requests = append(requests, tag); return nil })
			case "fail":
				ctx = agentctx.WithTagRequester(ctx, func(tag string) error { requests = append(requests, tag); return errors.New("refused") })
			}
			ctx = logging.WithRequestID(ctx, "req-1")
			for _, pull := range tt.pulls {
				items := make([]loop.MailboxItem, 0, len(pull))
				for _, dm := range pull {
					items = append(items, item(dm))
				}
				if got := turn.PullRender(ctx, items); len(got) == 0 {
					t.Fatalf("pull rendered no messages")
				}
			}

			if len(requests) != tt.wantRequests || (tt.wantRequests > 0 && requests[0] != attachmentsTag) {
				t.Errorf("tag requests = %v, want %d of %q", requests, tt.wantRequests, attachmentsTag)
			}
			got, _ := turn.Summary["attachment_tools"].(string)
			if got != tt.wantSummary {
				t.Errorf("summary attachment_tools = %q, want %q", got, tt.wantSummary)
			}
			if mid, _ := turn.Summary["attachment_tools_mid_turn"].(bool); mid != tt.wantMidTurn {
				t.Errorf("summary attachment_tools_mid_turn = %v, want %v", mid, tt.wantMidTurn)
			}
			if tt.wantMidTurn {
				decision := tt.wantSummary
				if decision == "grant_failed" {
					decision = "granted"
				}
				found := false
				for _, line := range strings.Split(logs.String(), "\n") {
					if strings.Contains(line, `msg="signal attachment tools `+decision+`"`) && strings.Contains(line, "request_id=req-1") && strings.Contains(line, "mid_turn=true") {
						found = true
					}
				}
				if !found {
					t.Errorf("mid-turn decision %q not logged with the run's request id:\n%s", decision, logs.String())
				}
			}
			if warned := strings.Contains(logs.String(), `msg="signal attachment tools grant failed"`); warned != tt.wantWarn {
				t.Errorf("grant-failed warning logged = %v, want %v", warned, tt.wantWarn)
			}
			if wakeDecided && tt.wantMidTurn {
				t.Error("the waking messages decided, yet the pull decided again")
			}
		})
	}
}
