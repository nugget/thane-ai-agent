package app

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

type recordingChannelSender struct {
	recipient string
	message   string
}

func (s *recordingChannelSender) SendMessage(_ context.Context, recipient, message string) error {
	s.recipient = recipient
	s.message = message
	return nil
}

func TestCompileLoopAgentRequest(t *testing.T) {
	req := looppkg.Request{
		Model:          "spark/gpt-oss:20b",
		ConversationID: "conv-123",
		ChannelBinding: &memory.ChannelBinding{
			Channel:     "signal",
			Address:     "+15551234567",
			ContactName: "Alice Smith",
		},
		Messages: []looppkg.Message{
			{Role: "system", Content: "stay focused", Images: []llm.ImageContent{{MediaType: "image/png", Data: "abc123"}}},
			{Role: "user", Content: "summarize this"},
		},
		SkipContext:     true,
		AllowedTools:    []string{"alpha", "beta"},
		ExcludeTools:    []string{"gamma"},
		SkipTagFilter:   true,
		Hints:           map[string]string{"mission": "automation"},
		InitialTags:     []string{"monitoring"},
		RuntimeTags:     []string{"message_channel"},
		MaxIterations:   7,
		MaxOutputTokens: 321,
		ToolTimeout:     2 * time.Second,
		UsageRole:       "delegate",
		UsageTaskName:   "spec-probe",
		FallbackContent: "fallback reply",
		SystemPrompt:    "custom prompt",
		PromptMode:      agentctx.PromptModeTask,
	}

	got := compileLoopAgentRequest(req)
	if got.Model != req.Model {
		t.Fatalf("Model = %q, want %q", got.Model, req.Model)
	}
	if got.ConversationID != req.ConversationID {
		t.Fatalf("ConversationID = %q, want %q", got.ConversationID, req.ConversationID)
	}
	if got.ChannelBinding == nil || got.ChannelBinding.Channel != "signal" || got.ChannelBinding.ContactName != "Alice Smith" {
		t.Fatalf("ChannelBinding = %#v", got.ChannelBinding)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Content != "summarize this" {
		t.Fatalf("Messages = %#v", got.Messages)
	}
	if len(got.Messages[0].Images) != 1 || got.Messages[0].Images[0].MediaType != "image/png" {
		t.Fatalf("Images = %#v", got.Messages[0].Images)
	}
	if !got.SkipContext || !got.SkipTagFilter {
		t.Fatalf("Skip flags = %#v", got)
	}
	if got.MaxIterations != 7 || got.MaxOutputTokens != 321 {
		t.Fatalf("Iteration/output limits = %#v", got)
	}
	if got.ToolTimeout != 2*time.Second {
		t.Fatalf("ToolTimeout = %v", got.ToolTimeout)
	}
	if got.UsageRole != "delegate" || got.UsageTaskName != "spec-probe" {
		t.Fatalf("Usage fields = role %q task %q", got.UsageRole, got.UsageTaskName)
	}
	if got.SystemPrompt != "custom prompt" {
		t.Fatalf("SystemPrompt = %q", got.SystemPrompt)
	}
	if got.FallbackContent != "fallback reply" {
		t.Fatalf("FallbackContent = %q", got.FallbackContent)
	}
	if got.PromptMode != agentctx.PromptModeTask {
		t.Fatalf("PromptMode = %q, want task", got.PromptMode)
	}

	got.AllowedTools[0] = "changed"
	got.ExcludeTools[0] = "changed"
	got.Hints["mission"] = "changed"
	got.InitialTags[0] = "changed"
	got.RuntimeTags[0] = "changed"
	got.Messages[0].Images[0].MediaType = "changed"
	got.ChannelBinding.ContactName = "changed"

	if req.AllowedTools[0] != "alpha" {
		t.Fatalf("AllowedTools mutated = %#v", req.AllowedTools)
	}
	if req.ExcludeTools[0] != "gamma" {
		t.Fatalf("ExcludeTools mutated = %#v", req.ExcludeTools)
	}
	if req.Hints["mission"] != "automation" {
		t.Fatalf("Hints mutated = %#v", req.Hints)
	}
	if req.InitialTags[0] != "monitoring" {
		t.Fatalf("InitialTags mutated = %#v", req.InitialTags)
	}
	if req.RuntimeTags[0] != "message_channel" {
		t.Fatalf("RuntimeTags mutated = %#v", req.RuntimeTags)
	}
	if req.Messages[0].Images[0].MediaType != "image/png" {
		t.Fatalf("Images mutated = %#v", req.Messages[0].Images)
	}
	if req.ChannelBinding.ContactName != "Alice Smith" {
		t.Fatalf("ChannelBinding mutated = %#v", req.ChannelBinding)
	}
}

func TestConversationSystemInjector(t *testing.T) {
	mem := memory.NewStore(10)
	tmpDir := t.TempDir()
	workingStore, err := memory.NewSQLiteStore(tmpDir+"/working.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = workingStore.Close() })
	archiveStore, err := memory.NewArchiveStoreFromDB(workingStore.DB(), nil, nil)
	if err != nil {
		t.Fatalf("NewArchiveStoreFromDB: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	archiver := memory.NewArchiveAdapter(archiveStore, workingStore, workingStore, logger)
	if _, err := archiver.StartSession("conv-1"); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	inj := &conversationSystemInjector{
		mem:      mem,
		archiver: archiver,
	}

	if !inj.IsSessionAlive("conv-1") {
		t.Fatal("IsSessionAlive(conv-1) = false, want true")
	}
	if inj.IsSessionAlive("conv-2") {
		t.Fatal("IsSessionAlive(conv-2) = true, want false")
	}

	if err := inj.InjectSystemMessage("conv-1", "hello from callback"); err != nil {
		t.Fatalf("InjectSystemMessage: %v", err)
	}
	channelSender := &recordingChannelSender{}
	channelRouter := newLoopChannelDeliveryRouter(inj)
	channelRouter.ConfigureSignalSender(channelSender.SendMessage)
	dispatcher := newDetachedLoopCompletionDispatcher(inj, channelRouter)
	plan := dispatcher.plan(looppkg.CompletionDelivery{
		Mode:           looppkg.CompletionConversation,
		ConversationID: "conv-1",
		Content:        "hello from detached loop",
	})
	if plan.Mode != looppkg.CompletionConversation || plan.ConversationID != "conv-1" || plan.Content != "hello from detached loop" {
		t.Fatalf("plan = %#v", plan)
	}
	presented, err := dispatcher.present(context.Background(), plan)
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if presented.Mode != looppkg.CompletionConversation || presented.ConversationID != "conv-1" || presented.Content != "hello from detached loop" {
		t.Fatalf("presented = %#v", presented)
	}
	if err := dispatcher.dispatch(context.Background(), presented); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	msgs := mem.GetMessages("conv-1")
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "hello from callback" {
		t.Fatalf("first message = %#v", msgs[0])
	}
	if msgs[1].Role != "system" || msgs[1].Content != "hello from detached loop" {
		t.Fatalf("second message = %#v", msgs[1])
	}

	channelPlan := dispatcher.plan(looppkg.CompletionDelivery{
		Mode: looppkg.CompletionChannel,
		Channel: &looppkg.CompletionChannelTarget{
			Channel:        "signal",
			Recipient:      "+15551234567",
			ConversationID: "conv-1",
		},
		Content: "hello from signal completion",
	})
	presentedChannel, err := dispatcher.present(context.Background(), channelPlan)
	if err != nil {
		t.Fatalf("present channel: %v", err)
	}
	if err := dispatcher.dispatch(context.Background(), presentedChannel); err != nil {
		t.Fatalf("dispatch channel: %v", err)
	}
	if channelSender.recipient != "+15551234567" || channelSender.message != "hello from signal completion" {
		t.Fatalf("channel send = %#v", channelSender)
	}

	msgs = mem.GetMessages("conv-1")
	if len(msgs) != 3 {
		t.Fatalf("messages len after signal channel delivery = %d, want 3", len(msgs))
	}
	if msgs[2].Role != "assistant" || msgs[2].Content != "hello from signal completion" {
		t.Fatalf("third message = %#v", msgs[2])
	}

	owuPlan := dispatcher.plan(looppkg.CompletionDelivery{
		Mode: looppkg.CompletionChannel,
		Channel: &looppkg.CompletionChannelTarget{
			Channel:        "owu",
			ConversationID: "conv-1",
		},
		Content: "hello from owu completion",
	})
	presentedOWU, err := dispatcher.present(context.Background(), owuPlan)
	if err != nil {
		t.Fatalf("present owu: %v", err)
	}
	if err := dispatcher.dispatch(context.Background(), presentedOWU); err != nil {
		t.Fatalf("dispatch owu: %v", err)
	}

	msgs = mem.GetMessages("conv-1")
	if len(msgs) != 4 {
		t.Fatalf("messages len after owu channel delivery = %d, want 4", len(msgs))
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "hello from owu completion" {
		t.Fatalf("fourth message = %#v", msgs[3])
	}
}

func TestDetachedLoopCompletionDispatcherRequiresConfiguredTargets(t *testing.T) {
	dispatcher := newDetachedLoopCompletionDispatcher(nil, nil)

	err := dispatcher.dispatch(context.Background(), loopCompletionPresentation{
		Mode:           looppkg.CompletionConversation,
		ConversationID: "conv-1",
		Content:        "hello",
	})
	if err == nil || err.Error() != "conversation completion delivery is not configured" {
		t.Fatalf("conversation dispatch error = %v", err)
	}

	err = dispatcher.dispatch(context.Background(), loopCompletionPresentation{
		Mode: looppkg.CompletionChannel,
		Channel: &looppkg.CompletionChannelTarget{
			Channel:   "signal",
			Recipient: "+15551234567",
		},
		Content: "hello",
	})
	if err == nil || err.Error() != "channel completion delivery is not configured" {
		t.Fatalf("channel dispatch error = %v", err)
	}
}

func TestDetachedLoopCompletionDispatcherRequiresConversationID(t *testing.T) {
	t.Parallel()

	dispatcher := newDetachedLoopCompletionDispatcher(&conversationSystemInjector{}, nil)

	err := dispatcher.dispatch(context.Background(), loopCompletionPresentation{
		Mode:    looppkg.CompletionConversation,
		Content: "hello",
	})
	if err == nil || err.Error() != "conversation completion delivery requires a non-empty conversation ID" {
		t.Fatalf("conversation dispatch error = %v", err)
	}
}

func TestContactChannelBindingResolverCachesConfiguredOwnerContact(t *testing.T) {
	store, err := contacts.NewStore(t.TempDir()+"/contacts.db", slog.Default())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tools := contacts.NewTools(store)
	if _, err := tools.SaveContact(`{"name":"Aimee","kind":"individual","trust_zone":"admin","facts":{"email":"aimee@example.com"}}`); err != nil {
		t.Fatalf("SaveContact: %v", err)
	}

	resolver := &contactChannelBindingResolver{
		store:            store,
		ownerContactName: "Aimee",
	}

	first := resolver.cachedOwnerContactID()
	if first == uuid.Nil {
		t.Fatal("cachedOwnerContactID() returned zero UUID")
	}

	resolver.ownerContactName = "Nobody"
	second := resolver.cachedOwnerContactID()
	if second != first {
		t.Fatalf("cachedOwnerContactID() after rename = %v, want cached %v", second, first)
	}
}
