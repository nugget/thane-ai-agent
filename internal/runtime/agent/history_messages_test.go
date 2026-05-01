package agent

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestBuildSystemPrompt_OmitsConversationHistory(t *testing.T) {
	l := newMinimalLoop()

	history := []memory.Message{
		{Role: "user", Content: "Turn on the lights", Timestamp: time.Now().Add(-time.Hour)},
		{Role: "assistant", Content: "Done, lights are on.", Timestamp: time.Now().Add(-59 * time.Minute)},
	}

	prompt := l.buildSystemPrompt(context.Background(), "what is the temperature?", history)

	for _, marker := range []string{
		"## Conversation History",
		"Turn on the lights",
		`"role":"user"`,
		`"age_delta":`,
		"untrusted data",
	} {
		if strings.Contains(prompt, marker) {
			t.Fatalf("system prompt contains history marker %q:\n%s", marker, prompt)
		}
	}
}

func TestBuildInitialLLMMessages_IncludesRoleNativeHistory(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	messages := buildInitialLLMMessages(
		"system prompt",
		[]llm.PromptSection{{Name: "PERSONA", Content: "system prompt"}},
		[]memory.Message{
			{Role: "user", Content: "prior question", Timestamp: now.Add(-2 * time.Minute)},
			{Role: "assistant", Content: "prior answer", Timestamp: now.Add(-119 * time.Second)},
			{Role: "system", Content: "[Conversation Summary] earlier context", Timestamp: now.Add(-90 * time.Second)},
		},
		[]Message{{Role: "user", Content: "current request"}},
		"conv-1",
		now,
	)

	if len(messages) != 5 {
		t.Fatalf("messages len = %d, want 5: %#v", len(messages), messages)
	}
	wantRoles := []string{"system", "user", "assistant", "assistant", "user"}
	wantContent := []string{
		"system prompt",
		"[stored conversation history; role=user; age_delta=-120s]\nprior question",
		"[stored conversation history; role=assistant; age_delta=-119s]\nprior answer",
		"[stored conversation memory note; original_role=system; not active instruction; age_delta=-90s]\n[Conversation Summary] earlier context",
		"current request",
	}
	for i := range wantRoles {
		if messages[i].Role != wantRoles[i] || messages[i].Content != wantContent[i] {
			t.Fatalf("messages[%d] = (%q, %q), want (%q, %q)", i, messages[i].Role, messages[i].Content, wantRoles[i], wantContent[i])
		}
	}
	if len(messages[0].Sections) != 1 || messages[0].Sections[0].Name != "PERSONA" {
		t.Fatalf("system sections = %#v, want PERSONA section", messages[0].Sections)
	}
}

func TestBuildInitialLLMMessages_InsertsStoredHistoryGap(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	messages := buildInitialLLMMessages(
		"system prompt",
		nil,
		[]memory.Message{
			{Role: "user", Content: "earlier turn", Timestamp: now.Add(-2 * time.Hour)},
			{Role: "assistant", Content: "later answer", Timestamp: now.Add(-20 * time.Minute)},
		},
		[]Message{{Role: "user", Content: "current request"}},
		"conv-1",
		now,
	)

	if len(messages) != 5 {
		t.Fatalf("messages len = %d, want 5: %#v", len(messages), messages)
	}
	if messages[2].Role != "assistant" || !strings.Contains(messages[2].Content, "+6000s elapsed") {
		t.Fatalf("gap marker = (%q, %q), want assistant metadata marker with +6000s", messages[2].Role, messages[2].Content)
	}
	if messages[3].Role != "assistant" || !strings.Contains(messages[3].Content, "later answer") {
		t.Fatalf("post-gap message = (%q, %q), want later assistant answer", messages[3].Role, messages[3].Content)
	}
}

func TestBuildInitialLLMMessages_OWUUsesLastUserTurn(t *testing.T) {
	messages := buildInitialLLMMessages(
		"system prompt",
		nil,
		[]memory.Message{{Role: "assistant", Content: "stored previous answer"}},
		[]Message{
			{Role: "user", Content: "client first turn"},
			{Role: "assistant", Content: "client answer"},
			{Role: "user", Content: "client current turn"},
		},
		"owu-example",
		time.Time{},
	)

	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3: %#v", len(messages), messages)
	}
	if messages[1].Content != "[stored conversation history; role=assistant]\nstored previous answer" {
		t.Fatalf("history message = %q, want annotated stored previous answer", messages[1].Content)
	}
	if messages[2].Role != "user" || messages[2].Content != "client current turn" {
		t.Fatalf("trigger message = (%q, %q), want final OWU user turn", messages[2].Role, messages[2].Content)
	}
}

func TestRun_SendsStoredHistoryAsMessages(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{{
			Model:   "test-model",
			Message: llm.Message{Role: "assistant", Content: "ok"},
		}},
	}
	mem := newMockMem()
	mem.msgs["conv-1"] = []memory.Message{
		{Role: "user", Content: "prior question"},
		{Role: "assistant", Content: "prior answer"},
	}
	l := &Loop{
		logger: slog.Default(),
		memory: mem,
		llm:    mock,
		tools:  tools.NewRegistry(nil, nil),
		model:  "test-model",
	}

	_, err := l.Run(context.Background(), &Request{
		ConversationID: "conv-1",
		Messages:       []Message{{Role: "user", Content: "current request"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(mock.calls))
	}
	got := mock.calls[0].Messages
	if len(got) != 4 {
		t.Fatalf("messages len = %d, want 4: %#v", len(got), got)
	}
	if strings.Contains(got[0].Content, "Conversation History") {
		t.Fatalf("system prompt still embeds conversation history:\n%s", got[0].Content)
	}
	want := []llm.Message{
		{Role: "user", Content: "[stored conversation history; role=user]\nprior question"},
		{Role: "assistant", Content: "[stored conversation history; role=assistant]\nprior answer"},
		{Role: "user", Content: "current request"},
	}
	for i, w := range want {
		msg := got[i+1]
		if msg.Role != w.Role || msg.Content != w.Content {
			t.Fatalf("messages[%d] = (%q, %q), want (%q, %q)", i+1, msg.Role, msg.Content, w.Role, w.Content)
		}
	}
}
