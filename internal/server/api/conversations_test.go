package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func newConvTestServer(t *testing.T) (*Server, *memory.SQLiteStore) {
	t.Helper()
	store, err := memory.NewSQLiteStore(t.TempDir()+"/memory.db", 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &Server{logger: testAPILogger()}
	s.memoryStore = store
	return s, store
}

func addConv(t *testing.T, store *memory.SQLiteStore, id string, messages int, binding *memory.ChannelBinding) {
	t.Helper()
	if _, err := store.GetOrCreateConversation(id); err != nil {
		t.Fatalf("GetOrCreateConversation %s: %v", id, err)
	}
	for i := 0; i < messages; i++ {
		if err := store.AddMessage(id, "user", "hello"); err != nil {
			t.Fatalf("AddMessage %s: %v", id, err)
		}
	}
	if binding != nil {
		if err := store.BindConversationChannel(id, binding); err != nil {
			t.Fatalf("BindConversationChannel %s: %v", id, err)
		}
	}
}

func doConvList(t *testing.T, s *Server, rawquery string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/conversations?"+rawquery, nil)
	rr := httptest.NewRecorder()
	s.handleConversationList(rr, req)
	var body map[string]any
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (raw=%s)", err, rr.Body.String())
		}
	}
	return rr, body
}

func TestHandleConversationListValidation(t *testing.T) {
	s, _ := newConvTestServer(t)
	manyIDs := make([]string, 201) // 201 DISTINCT ids (splitCSV dedups identical ones)
	for i := range manyIDs {
		manyIDs[i] = fmt.Sprintf("id%d", i)
	}
	cases := []struct {
		name  string
		query string
	}{
		{"bad_sort", "sort=bogus"},
		{"bad_order", "order=sideways"},
		{"bad_min_messages", "min_messages=abc"},
		{"negative_min", "min_messages=-1"},
		{"min_gt_max", "min_messages=5&max_messages=2"},
		{"bad_kind", "kind=Signal!"},
		{"bad_time", "updated_after=notatime"},
		{"too_many_ids", "ids=" + strings.Join(manyIDs, ",")},
		{"bad_cursor", "cursor=!!!not-base64!!!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr, _ := doConvList(t, s, tc.query)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandleConversationListNotConfigured(t *testing.T) {
	s := &Server{logger: testAPILogger()} // no memory store
	rr := httptest.NewRecorder()
	s.handleConversationList(rr, httptest.NewRequest(http.MethodGet, "/v1/conversations", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestHandleConversationListPaging(t *testing.T) {
	s, store := newConvTestServer(t)
	addConv(t, store, "alpha", 1, nil)
	addConv(t, store, "beta", 2, nil)
	addConv(t, store, "gamma", 3, nil)

	// Full page (limit < total) → opaque next_cursor.
	rr, body := doConvList(t, s, "limit=2")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := int(body["count"].(float64)); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	if got := int(body["total"].(float64)); got != 3 {
		t.Fatalf("total = %d, want 3", got)
	}
	cursor, ok := body["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("next_cursor = %v, want non-empty string", body["next_cursor"])
	}

	// Following the cursor returns the remainder with a null terminal cursor.
	rr2, body2 := doConvList(t, s, "limit=2&cursor="+cursor)
	if rr2.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d", rr2.Code)
	}
	if got := int(body2["count"].(float64)); got != 1 {
		t.Fatalf("page 2 count = %d, want 1", got)
	}
	if body2["next_cursor"] != nil {
		t.Fatalf("page 2 next_cursor = %v, want null", body2["next_cursor"])
	}
}

func TestHandleConversationListCursorMismatch(t *testing.T) {
	s, store := newConvTestServer(t)
	addConv(t, store, "alpha", 1, nil)
	addConv(t, store, "beta", 1, nil)
	addConv(t, store, "gamma", 1, nil)

	_, body := doConvList(t, s, "sort=updated_at&limit=2")
	cursor := body["next_cursor"].(string)

	// Reusing the cursor under a different sort must be a 400, not silent drift.
	rr, _ := doConvList(t, s, "sort=created_at&limit=2&cursor="+cursor)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 on sort mismatch", rr.Code)
	}
}

func TestHandleConversationListMalformedCursor(t *testing.T) {
	s, store := newConvTestServer(t)
	addConv(t, store, "alpha", 1, nil)

	// A structurally-valid token with an empty sort value must be a 400, not a
	// 500 (an empty V would otherwise reach strconv.Atoi("") in the store).
	token := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"s":"message_count","d":"desc","v":"","i":"alpha"}`))
	rr, _ := doConvList(t, s, "sort=message_count&cursor="+token)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty-value cursor (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestHandleConversationListIDsAndBinding(t *testing.T) {
	s, store := newConvTestServer(t)
	addConv(t, store, "signal-+15551234567", 4, &memory.ChannelBinding{
		Channel:     "signal",
		Address:     "+15551234567",
		ContactName: "Alice",
	})
	addConv(t, store, "loop-noise", 1, nil)

	// The '+' in the id must be percent-encoded (a raw '+' decodes to space),
	// mirroring the frontend's encodeURIComponent per id.
	_, body := doConvList(t, s, "ids="+url.QueryEscape("signal-+15551234567"))
	convs := body["conversations"].([]any)
	if len(convs) != 1 {
		t.Fatalf("conversations len = %d, want 1", len(convs))
	}
	got := convs[0].(map[string]any)
	if got["id"] != "signal-+15551234567" {
		t.Fatalf("id = %v", got["id"])
	}
	if int(got["message_count"].(float64)) != 4 {
		t.Fatalf("message_count = %v, want 4", got["message_count"])
	}
	binding, ok := got["channel_binding"].(map[string]any)
	if !ok || binding["contact_name"] != "Alice" {
		t.Fatalf("channel_binding = %v, want contact_name Alice", got["channel_binding"])
	}
}

func TestHandleConversationListRelativeTime(t *testing.T) {
	s, store := newConvTestServer(t)
	addConv(t, store, "recent", 1, nil)
	// Just-created conv is within the last hour.
	rr, body := doConvList(t, s, "updated_after=1h")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if int(body["total"].(float64)) != 1 {
		t.Fatalf("total = %v, want 1 (conv created within 1h)", body["total"])
	}
}
