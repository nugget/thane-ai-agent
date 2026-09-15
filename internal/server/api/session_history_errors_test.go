package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestSessionHistoryReadFailuresReturnErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		empty     bool
		closeDB   bool
		corrupt   bool
		cancel    bool
		wantError bool
	}{
		{name: "stored history"},
		{name: "new conversation", empty: true},
		{name: "closed database", closeDB: true, wantError: true},
		{name: "invalid stored timestamp", corrupt: true, wantError: true},
		{name: "canceled request", cancel: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store := newConvTestServer(t)
			for _, message := range []struct{ role, content string }{
				{"user", "Preserve this earlier question."},
				{"assistant", "Preserve this earlier answer."},
				{"system", "Internal note."},
			} {
				if tc.empty {
					break
				}
				if err := store.AddMessage("default", message.role, message.content, memory.OriginChannel); err != nil {
					t.Fatal(err)
				}
			}
			loop, err := agent.NewLoop(agent.LoopOptions{
				Logger: testAPILogger(), Memory: store, LLM: refusingLLM{}, Model: "test-model",
			})
			if err != nil {
				t.Fatal(err)
			}
			s.loop = loop
			if tc.closeDB {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.corrupt {
				if _, err := store.DB().Exec(`UPDATE messages SET timestamp = 'invalid' WHERE role = 'assistant'`); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			rr := httptest.NewRecorder()
			s.handleSessionHistory(rr, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/sessions/history", nil))
			var body struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid response: %v; body = %s", err, rr.Body.String())
			}
			if tc.wantError {
				if rr.Code != http.StatusInternalServerError || body.Error == nil || body.Error.Code != http.StatusInternalServerError || body.Error.Message != "session history unavailable" || len(body.Messages) != 0 {
					t.Fatalf("read failure returned status %d, body = %s; want an error without partial history", rr.Code, rr.Body.String())
				}
				return
			}
			wantMessages := 2
			if tc.empty {
				wantMessages = 0
			}
			if rr.Code != http.StatusOK || body.Error != nil || len(body.Messages) != wantMessages {
				t.Fatalf("history changed: status %d, body = %s", rr.Code, rr.Body.String())
			}
			if tc.empty {
				return
			}
			if body.Messages[0].Role != "user" || body.Messages[0].Content != "Preserve this earlier question." || body.Messages[1].Role != "assistant" || body.Messages[1].Content != "Preserve this earlier answer." {
				t.Fatalf("history order or content changed: %s", rr.Body.String())
			}
		})
	}
}

func TestConversationGetDistinguishesReadFailureFromAbsence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		closeDB bool
		corrupt bool
		cancel  bool
		code    int
	}{
		{name: "stored conversation", id: "known", code: http.StatusOK},
		{name: "missing conversation", id: "missing", code: http.StatusNotFound},
		{name: "closed database", id: "known", closeDB: true, code: http.StatusInternalServerError},
		{name: "invalid stored timestamp", id: "known", corrupt: true, code: http.StatusInternalServerError},
		{name: "canceled request", id: "known", cancel: true, code: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store := newConvTestServer(t)
			addConv(t, store, "known", 1, nil)
			if tc.closeDB {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.corrupt {
				if _, err := store.DB().Exec(`UPDATE messages SET timestamp = 'invalid'`); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			rr := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/conversations/"+tc.id, nil)
			req.SetPathValue("id", tc.id)
			s.handleConversationGet(rr, req)
			if rr.Code != tc.code {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, tc.code, rr.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.code != http.StatusOK {
				if body["error"] == nil || body["messages"] != nil {
					t.Fatalf("failed read presented history: %s", rr.Body.String())
				}
			} else if body["id"] == nil || body["messages"] == nil || body["error"] != nil {
				t.Fatalf("successful read lost conversation: %s", rr.Body.String())
			}
		})
	}
}
