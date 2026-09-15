package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func newArchiveMessagesTestServer(t *testing.T) (*Server, *memory.SQLiteStore) {
	t.Helper()
	s, working := newConvTestServer(t)
	archive, err := memory.NewArchiveStoreFromDB(working.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.archiveStore = archive
	return s, working
}

func TestHandleArchiveMessagesUnifiedDateBounds(t *testing.T) {
	s, working := newArchiveMessagesTestServer(t)
	if err := working.AddMessage("conv", "user", "native timestamp", ""); err != nil {
		t.Fatal(err)
	}
	// Start with the real write path and fix the clock for deterministic bounds.
	base := time.Date(2026, 9, 15, 0, 0, 0, 123456789, time.UTC)
	if _, err := working.DB().Exec(`UPDATE messages SET timestamp = ?`, base); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, conv, ts string }{
		{"earlier", "conv", "2026-09-15T00:00:00.123456788Z"},
		{"later", "conv", "2026-09-14T19:00:00.123456790-05:00"},
		{"outside", "conv", "2026-09-15T00:00:00.123456791Z"},
		{"other", "other", "2026-09-15T00:00:00.123456789Z"},
	} {
		_, err := working.DB().Exec(`INSERT INTO messages (id, conversation_id, role, content, timestamp) VALUES (?, ?, 'user', ?, ?)`, row.id, row.conv, row.id, row.ts)
		if err != nil {
			t.Fatal(err)
		}
	}
	q := url.Values{
		"from":            {"2026-09-14T19:00:00.123456789-05:00"},
		"to":              {"2026-09-15T05:30:00.123456790+05:30"},
		"conversation_id": {"conv"},
	}
	for _, limit := range []string{"", "1"} {
		q.Set("limit", limit)
		req := httptest.NewRequest(http.MethodGet, "/v1/archive/messages?"+q.Encode(), nil)
		rr := httptest.NewRecorder()
		s.handleArchiveMessages(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			Messages []memory.Message `json:"messages"`
			Count    int              `json:"count"`
			From     string           `json:"from"`
			To       string           `json:"to"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := 2
		if limit == "1" {
			want = 1
		}
		if body.Count != want || len(body.Messages) != want {
			t.Fatalf("count=%d messages=%v", body.Count, body.Messages)
		}
		if body.Messages[0].Content != "native timestamp" {
			t.Fatalf("first message=%v", body.Messages[0])
		}
		if body.From != q.Get("from") || body.To != q.Get("to") {
			t.Fatalf("echoed bounds changed: %v", body)
		}
	}
}

func TestHandleArchiveMessagesLimits(t *testing.T) {
	s, working := newArchiveMessagesTestServer(t)
	tx, err := working.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range memory.MaxArchiveRangeMessages + 1 {
		_, err := tx.Exec(`INSERT INTO messages (id, conversation_id, role, content, timestamp) VALUES (?, 'conv', 'user', 'message', '2026-09-15 00:00:00+00:00')`, fmt.Sprintf("%04d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		limit string
		want  int
	}{
		{"", 500}, {"0", 500}, {"-1", 500}, {"1", 1}, {"999999999", memory.MaxArchiveRangeMessages},
	} {
		t.Run(tc.limit, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/archive/messages?from=2026-09-15T00:00:00Z&to=2026-09-15T01:00:00Z&limit="+tc.limit, nil)
			rr := httptest.NewRecorder()
			s.handleArchiveMessages(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var body struct {
				Count    int              `json:"count"`
				Messages []memory.Message `json:"messages"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Count != tc.want || len(body.Messages) != tc.want {
				t.Fatalf("count=%d want=%d", body.Count, tc.want)
			}
			for i, msg := range body.Messages {
				if msg.ID != fmt.Sprintf("%04d", i) {
					t.Fatalf("message %d ID=%s", i, msg.ID)
				}
			}
		})
	}
}

func TestHandleArchiveMessagesErrors(t *testing.T) {
	s, _ := newArchiveMessagesTestServer(t)
	for _, query := range []string{
		"", "from=bad&to=2026-09-15T00:00:00Z", "from=2026-09-15T00:00:00Z&to=bad",
		"from=2026-09-16T00:00:00Z&to=2026-09-15T00:00:00Z",
	} {
		rr := httptest.NewRecorder()
		s.handleArchiveMessages(rr, httptest.NewRequest(http.MethodGet, "/v1/archive/messages?"+query, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%q status=%d body=%s", query, rr.Code, rr.Body.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/archive/messages?from=2026-09-15T00:00:00Z&to=2026-09-16T00:00:00Z", nil).WithContext(ctx)
	s.handleArchiveMessages(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("canceled request status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleArchiveMessagesExplicitZeroInstant(t *testing.T) {
	s, working := newArchiveMessagesTestServer(t)
	if err := working.AddMessage("conv", "user", "modern", ""); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/archive/messages?from=0001-01-01T00:00:00Z&to=0001-01-01T00:00:00Z", nil)
	s.handleArchiveMessages(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 {
		t.Fatalf("explicit year-one range returned modern messages: %s", rr.Body.String())
	}
}
