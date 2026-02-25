package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

// mockSessionStore is a test double for SessionStore.
type mockSessionStore struct {
	sessions   []*memory.Session
	transcript []memory.ArchivedMessage
	toolCalls  []memory.ArchivedToolCall
}

func (m *mockSessionStore) ListSessions(conversationID string, limit int) ([]*memory.Session, error) {
	if conversationID != "" {
		var filtered []*memory.Session
		for _, s := range m.sessions {
			if s.ConversationID == conversationID {
				filtered = append(filtered, s)
			}
		}
		return filtered, nil
	}
	return m.sessions, nil
}

func (m *mockSessionStore) GetSession(sessionID string) (*memory.Session, error) {
	for _, s := range m.sessions {
		if s.ID == sessionID {
			return s, nil
		}
	}
	return nil, nil
}

func (m *mockSessionStore) GetSessionTranscript(sessionID string) ([]memory.ArchivedMessage, error) {
	return m.transcript, nil
}

func (m *mockSessionStore) GetSessionToolCalls(sessionID string) ([]memory.ArchivedToolCall, error) {
	return m.toolCalls, nil
}

func (m *mockSessionStore) ListChildSessions(parentSessionID string) ([]*memory.Session, error) {
	var children []*memory.Session
	for _, s := range m.sessions {
		if s.ParentSessionID == parentSessionID {
			children = append(children, s)
		}
	}
	return children, nil
}

func newSessionTestServer(store SessionStore) *WebServer {
	return NewWebServer(Config{
		SessionStore: store,
		Logger:       slog.Default(),
	})
}

func testSessions() []*memory.Session {
	now := time.Now()
	ended := now.Add(-1 * time.Hour)
	return []*memory.Session{
		{
			ID:             "01234567-abcd-efgh-0000-000000000001",
			ConversationID: "conv-alpha",
			StartedAt:      now.Add(-2 * time.Hour),
			MessageCount:   15,
			Title:          "Debugging the widget",
			Metadata: &memory.SessionMetadata{
				OneLiner:    "Fixed the widget crash",
				SessionType: "debugging",
				ToolsUsed:   map[string]int{"shell_exec": 3, "web_search": 1},
				Models:      []string{"claude-sonnet"},
			},
		},
		{
			ID:             "01234567-abcd-efgh-0000-000000000002",
			ConversationID: "conv-beta",
			StartedAt:      now.Add(-3 * time.Hour),
			EndedAt:        &ended,
			EndReason:      "reset",
			MessageCount:   8,
			Title:          "Planning session",
			Metadata: &memory.SessionMetadata{
				OneLiner:     "Planned the Q3 roadmap",
				SessionType:  "planning",
				KeyDecisions: []string{"Use Go for backend"},
				Participants: []string{"Nugget"},
			},
		},
	}
}

func testTranscript() []memory.ArchivedMessage {
	now := time.Now()
	return []memory.ArchivedMessage{
		{
			Role:      "user",
			Content:   "Hello, can you help me debug this?",
			Timestamp: now.Add(-2 * time.Hour),
		},
		{
			Role:       "assistant",
			Content:    "Sure, let me take a look at the code.",
			Timestamp:  now.Add(-2*time.Hour + 30*time.Second),
			TokenCount: 42,
		},
		{
			Role:       "tool",
			Content:    "file contents here",
			Timestamp:  now.Add(-2*time.Hour + 45*time.Second),
			ToolCallID: "call_abc12345",
		},
	}
}

func testToolCalls() []memory.ArchivedToolCall {
	now := time.Now()
	return []memory.ArchivedToolCall{
		{
			ToolName:   "shell_exec",
			Arguments:  `{"command": "ls -la"}`,
			Result:     "file1.go\nfile2.go",
			StartedAt:  now.Add(-1 * time.Hour),
			DurationMs: 150,
		},
		{
			ToolName:   "web_search",
			Arguments:  `{"query": "go testing"}`,
			Error:      "timeout",
			StartedAt:  now.Add(-30 * time.Minute),
			DurationMs: 5000,
		},
	}
}

func TestSessions_NilStore(t *testing.T) {
	ws := NewWebServer(Config{Logger: slog.Default()})
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /sessions (nil store) status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestSessions_ListPage(t *testing.T) {
	store := &mockSessionStore{sessions: testSessions()}
	ws := newSessionTestServer(store)
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /sessions status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	for _, want := range []string{
		"<!DOCTYPE html>",
		"Sessions",
		"Debugging the widget",
		"Planning session",
		"conv-alp",   // short conv ID (8 chars of "conv-alpha")
		"conv-bet",   // short conv ID (8 chars of "conv-beta")
		"badge-teal", // active badge
		"badge-ok",   // completed badge
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /sessions response missing %q", want)
		}
	}
}

func TestSessions_HtmxPartial(t *testing.T) {
	store := &mockSessionStore{sessions: testSessions()}
	ws := newSessionTestServer(store)
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/sessions", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "sessions-tbody")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /sessions (htmx) status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("htmx tbody partial should not contain <!DOCTYPE html>")
	}
	if !strings.Contains(body, "Debugging the widget") {
		t.Error("htmx tbody partial should contain session title")
	}
}

func TestSessions_FilterByConversation(t *testing.T) {
	store := &mockSessionStore{sessions: testSessions()}
	ws := newSessionTestServer(store)
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/sessions?conversation_id=conv-alpha", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /sessions?conversation_id=conv-alpha status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Debugging the widget") {
		t.Error("filtered list should contain matching session")
	}
	if strings.Contains(body, "Planning session") {
		t.Error("filtered list should not contain non-matching session")
	}
}

func TestSessionDetail_Found(t *testing.T) {
	store := &mockSessionStore{
		sessions:   testSessions(),
		transcript: testTranscript(),
		toolCalls:  testToolCalls(),
	}
	ws := newSessionTestServer(store)
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/sessions/01234567-abcd-efgh-0000-000000000002", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /sessions/{id} status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	for _, want := range []string{
		"Planning session",
		"conv-beta",
		"reset",         // end reason
		"badge-ok",      // completed badge
		"planning",      // session type
		"Key Decisions", // metadata section
		"Use Go for backend",
		"Nugget",                             // participant
		"Hello, can you help me debug this?", // transcript
		"shell_exec",                         // tool call
		"web_search",                         // tool call
		"timeout",                            // tool error
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /sessions/{id} response missing %q", want)
		}
	}
}

func TestSessionDetail_NotFound(t *testing.T) {
	store := &mockSessionStore{sessions: testSessions()}
	ws := newSessionTestServer(store)
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/sessions/nonexistent-id", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /sessions/nonexistent status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestSessionDetail_NilStore(t *testing.T) {
	ws := NewWebServer(Config{Logger: slog.Default()})
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/sessions/some-id", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /sessions/{id} (nil store) status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestSessionDetail_ParentChildLinks(t *testing.T) {
	now := time.Now()
	ended := now.Add(-1 * time.Hour)
	childEnded := now.Add(-30 * time.Minute)

	parentSession := &memory.Session{
		ID:             "parent-session-00000001",
		ConversationID: "conv-main",
		StartedAt:      now.Add(-2 * time.Hour),
		EndedAt:        &ended,
		EndReason:      "reset",
		MessageCount:   10,
		Title:          "Parent session",
	}
	childSession := &memory.Session{
		ID:               "child-session-00000001",
		ConversationID:   "delegate-abcd1234",
		StartedAt:        now.Add(-1 * time.Hour),
		EndedAt:          &childEnded,
		EndReason:        "completed",
		MessageCount:     5,
		Title:            "Delegate task",
		ParentSessionID:  "parent-session-00000001",
		ParentToolCallID: "call_abc123",
	}

	store := &mockSessionStore{
		sessions: []*memory.Session{parentSession, childSession},
	}
	ws := newSessionTestServer(store)
	mux := http.NewServeMux()
	ws.RegisterRoutes(mux)

	// Child session should show parent link
	req := httptest.NewRequest("GET", "/sessions/child-session-00000001", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /sessions/child status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	for _, want := range []string{
		"Delegate task",
		"Parent Session",                    // dt label
		"parent-s",                          // short parent ID (8 chars)
		"/sessions/parent-session-00000001", // parent link
	} {
		if !strings.Contains(body, want) {
			t.Errorf("child session detail missing %q", want)
		}
	}

	// Parent session should show child sessions table
	req = httptest.NewRequest("GET", "/sessions/parent-session-00000001", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /sessions/parent status = %d, want %d", w.Code, http.StatusOK)
	}

	body = w.Body.String()
	for _, want := range []string{
		"Parent session",
		"Child Sessions",                   // section heading
		"Delegate task",                    // child title
		"/sessions/child-session-00000001", // child link
	} {
		if !strings.Contains(body, want) {
			t.Errorf("parent session detail missing %q", want)
		}
	}
}

func TestSessionStatus(t *testing.T) {
	now := time.Now()
	ended := now.Add(-1 * time.Hour)

	tests := []struct {
		name string
		sess *memory.Session
		want string
	}{
		{
			name: "active session",
			sess: &memory.Session{EndedAt: nil},
			want: "active",
		},
		{
			name: "completed session",
			sess: &memory.Session{EndedAt: &ended, EndReason: "reset"},
			want: "completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionStatus(tt.sess)
			if got != tt.want {
				t.Errorf("sessionStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShortID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"01234567-abcd-efgh", "01234567"},
		{"short", "short"},
		{"12345678", "12345678"},
		{"", ""},
	}

	for _, tt := range tests {
		got := shortID(tt.input)
		if got != tt.want {
			t.Errorf("shortID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
