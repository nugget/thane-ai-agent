package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

type fakeLoopReg struct {
	statuses []looppkg.Status
	byID     map[string]looppkg.Status
}

func (f fakeLoopReg) Statuses() []looppkg.Status { return f.statuses }
func (f fakeLoopReg) StatusByID(id string) (looppkg.Status, bool) {
	st, ok := f.byID[id]
	return st, ok
}

// fakeLogQuerier returns canned entries keyed by conversation ID, or an error.
type fakeLogQuerier struct {
	byConv map[string][]logging.LogEntry
	err    error
}

func (q fakeLogQuerier) Query(p logging.QueryParams) ([]logging.LogEntry, error) {
	if q.err != nil {
		return nil, q.err
	}
	return q.byConv[p.ConversationID], nil
}

func quietServer(reg LoopStatusReader) *Server {
	return &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		loopRegistry: reg,
	}
}

// at returns a deterministic UTC timestamp for ordering assertions.
func at(sec int) time.Time { return time.Unix(int64(sec), 0).UTC() }

func TestHandleLoops(t *testing.T) {
	s := quietServer(fakeLoopReg{statuses: []looppkg.Status{
		{ID: "a", Name: "alpha", State: looppkg.StateSleeping},
		{ID: "b", Name: "beta", State: looppkg.StateError},
	}})

	rr := httptest.NewRecorder()
	s.handleLoops(rr, httptest.NewRequest(http.MethodGet, "/v1/loops", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var all []looppkg.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("unfiltered loops = %d, want 2", len(all))
	}

	rr2 := httptest.NewRecorder()
	s.handleLoops(rr2, httptest.NewRequest(http.MethodGet, "/v1/loops?state=sleeping", nil))
	var filtered []looppkg.Status
	if err := json.Unmarshal(rr2.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "a" {
		t.Errorf("?state=sleeping = %+v, want exactly [a]", filtered)
	}
}

func TestHandleLoops_IncludesLoopView(t *testing.T) {
	s := quietServer(fakeLoopReg{statuses: []looppkg.Status{
		{ID: "p", Name: "parent", State: looppkg.StateSleeping},
		{ID: "c", Name: "child", State: looppkg.StateSleeping, ParentID: "p",
			ContextWindow: 200000, LastInputTokens: 100000},
	}})

	rr := httptest.NewRecorder()
	s.handleLoops(rr, httptest.NewRequest(http.MethodGet, "/v1/loops", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var rows []struct {
		ID   string `json:"id"`
		View struct {
			ParentName     *string `json:"parent_name"`
			ChildCount     int     `json:"child_count"`
			ContextFillPct *int    `json:"context_fill_pct"`
			PolicyState    string  `json:"policy_state"`
		} `json:"view"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]int{}
	for i, r := range rows {
		byID[r.ID] = i
	}
	parent, child := rows[byID["p"]], rows[byID["c"]]

	// Resolver runs over the full batch, so graph joins are populated.
	if parent.View.ChildCount != 1 {
		t.Errorf("parent child_count = %d, want 1", parent.View.ChildCount)
	}
	if child.View.ParentName == nil || *child.View.ParentName != "parent" {
		t.Errorf("child parent_name = %v, want \"parent\"", child.View.ParentName)
	}
	// Precomputed so the client never divides.
	if child.View.ContextFillPct == nil || *child.View.ContextFillPct != 50 {
		t.Errorf("child context_fill_pct = %v, want 50", child.View.ContextFillPct)
	}
	// No definition registry wired on quietServer ⇒ ephemeral, not a misleading default.
	if child.View.PolicyState != "ephemeral" {
		t.Errorf("child policy_state = %q, want ephemeral", child.View.PolicyState)
	}
}

func TestHandleLoops_Unconfigured(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rr := httptest.NewRecorder()
	s.handleLoops(rr, httptest.NewRequest(http.MethodGet, "/v1/loops", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when registry unset", rr.Code)
	}
}

func TestHandleLoop_NotFound(t *testing.T) {
	s := quietServer(fakeLoopReg{})
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/nope", nil)
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	s.handleLoop(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestParseLogLimit(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", defaultLogLimit},
		{"10", 10},
		{"200", 200},
		{"0", defaultLogLimit},
		{"-5", defaultLogLimit},
		{"abc", defaultLogLimit},
		{"999", 200}, // capped
	}
	for _, c := range cases {
		if got := parseLogLimit(c.in); got != c.want {
			t.Errorf("parseLogLimit(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMergeLoopLogs_NewestFirstAcrossConvIDs(t *testing.T) {
	s := quietServer(fakeLoopReg{})
	s.logQuerier = fakeLogQuerier{byConv: map[string][]logging.LogEntry{
		"c1": {{ID: 1, Timestamp: at(100)}, {ID: 2, Timestamp: at(200)}},
		"c2": {{ID: 3, Timestamp: at(300)}},
	}}
	got := s.mergeLoopLogs([]string{"c1", "c2"}, "", 50)
	if len(got) != 3 {
		t.Fatalf("merged = %d entries, want 3", len(got))
	}
	if got[0].ID != 3 || got[1].ID != 2 || got[2].ID != 1 {
		t.Errorf("order = [%d,%d,%d], want newest-first [3,2,1]", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestMergeLoopLogs_TruncatesToNewest(t *testing.T) {
	s := quietServer(fakeLoopReg{})
	s.logQuerier = fakeLogQuerier{byConv: map[string][]logging.LogEntry{
		"c1": {{ID: 1, Timestamp: at(100)}, {ID: 2, Timestamp: at(200)}, {ID: 3, Timestamp: at(300)}},
	}}
	got := s.mergeLoopLogs([]string{"c1"}, "", 2)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 2 {
		t.Errorf("got %+v, want the 2 newest [3,2]", got)
	}
}

func TestMergeLoopLogs_SkipsErrorsAndIsNonNil(t *testing.T) {
	s := quietServer(fakeLoopReg{})
	s.logQuerier = fakeLogQuerier{err: errors.New("boom")}
	got := s.mergeLoopLogs([]string{"c1", "c2"}, "", 50)
	if got == nil {
		t.Fatal("result is nil; want a non-nil empty slice so it encodes as []")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 when every query errors", len(got))
	}
}

func TestHandleLoopLogs_BareArrayNewestFirst(t *testing.T) {
	s := quietServer(fakeLoopReg{byID: map[string]looppkg.Status{
		"a": {ID: "a", RecentConvIDs: []string{"c1"}},
	}})
	s.logQuerier = fakeLogQuerier{byConv: map[string][]logging.LogEntry{
		"c1": {{ID: 1, Timestamp: at(100)}, {ID: 2, Timestamp: at(200)}},
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/a/logs", nil)
	req.SetPathValue("id", "a")
	rr := httptest.NewRecorder()
	s.handleLoopLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Must be a bare array per native.yaml, not a {entries,count} object.
	if b := strings.TrimSpace(rr.Body.String()); !strings.HasPrefix(b, "[") {
		t.Fatalf("body = %s, want a bare JSON array", b)
	}
	var entries []logging.LogEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 || entries[0].ID != 2 {
		t.Errorf("entries = %+v, want newest-first [2,1]", entries)
	}
}

func TestHandleLoopLogs_EmptyIsArrayNotObject(t *testing.T) {
	s := quietServer(fakeLoopReg{byID: map[string]looppkg.Status{
		"a": {ID: "a"}, // no RecentConvIDs
	}})
	s.logQuerier = fakeLogQuerier{}
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/a/logs", nil)
	req.SetPathValue("id", "a")
	rr := httptest.NewRecorder()
	s.handleLoopLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if b := strings.TrimSpace(rr.Body.String()); b != "[]" {
		t.Errorf("empty body = %q, want %q", b, "[]")
	}
}

func TestHandleLoopLogs_Gating(t *testing.T) {
	// logQuerier unset -> 503
	s := quietServer(fakeLoopReg{})
	req := httptest.NewRequest(http.MethodGet, "/v1/loops/a/logs", nil)
	req.SetPathValue("id", "a")
	rr := httptest.NewRecorder()
	s.handleLoopLogs(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("no querier: status = %d, want 503", rr.Code)
	}

	// querier set but loop not found -> 404
	s2 := quietServer(fakeLoopReg{})
	s2.logQuerier = fakeLogQuerier{}
	req2 := httptest.NewRequest(http.MethodGet, "/v1/loops/missing/logs", nil)
	req2.SetPathValue("id", "missing")
	rr2 := httptest.NewRecorder()
	s2.handleLoopLogs(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Errorf("missing loop: status = %d, want 404", rr2.Code)
	}
}

func TestHandleLoopEvents_Snapshot(t *testing.T) {
	s := quietServer(fakeLoopReg{statuses: []looppkg.Status{
		{ID: "a", Name: "alpha", State: looppkg.StateSleeping},
	}})
	s.eventBus = events.New()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/loops/events", s.handleLoopEvents)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/loops/events")
	if err != nil {
		t.Fatalf("GET /v1/loops/events: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	buf := make([]byte, 4096)
	done := make(chan string, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		done <- string(buf[:n])
	}()
	select {
	case data := <-done:
		if !strings.Contains(data, "event: snapshot") {
			t.Errorf("first event = %q, want a snapshot event", data)
		}
		if !strings.Contains(data, "alpha") {
			t.Errorf("snapshot = %q, want it to include the loop name", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for snapshot event")
	}
}
