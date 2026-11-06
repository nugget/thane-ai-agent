package logging

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// fixturePrompt builds a system prompt the way the agent does: sections
// joined by a blank line, the cached ones leading, the clock last.
func fixturePrompt(conditions string) llm.Message {
	sections := []llm.PromptSection{
		{Name: "IDENTITY", Content: "# Identity\nYou are the household agent for Alice and Bob.", CacheTTL: "1h"},
		{Name: "TOOL GUIDANCE", Content: "## Tools\nPrefer reading state before acting.", CacheTTL: "1h"},
		{Name: "LIVE STATE", Content: "## Live State\nThe porch light is off."},
		{Name: "CURRENT CONDITIONS", Content: "## Current Conditions\nTime: " + conditions},
	}
	var b strings.Builder
	for i, s := range sections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s.Content)
	}
	return llm.Message{Role: "system", Content: b.String(), Sections: sections}
}

func fixtureTool(name, description string) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": description,
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id": map[string]any{"type": "string", "description": "Target entity."},
				},
				"required": []any{"entity_id"},
			},
		},
	}
}

func fixtureTools() []map[string]any {
	return []map[string]any{
		fixtureTool("ha_get_state", "Read one entity's state."),
		fixtureTool("ha_call_service", "Call a Home Assistant service."),
		fixtureTool("send_message", "Send Bob a message."),
	}
}

func fixtureHistory() []llm.Message {
	call := llm.ToolCall{ID: "call_1"}
	call.Function.Name = "ha_get_state"
	call.Function.Arguments = map[string]any{"entity_id": "light.porch"}
	return []llm.Message{
		fixturePrompt("2026-01-02T03:04:05Z"),
		{Role: "user", Content: "Is the porch light on?"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
		{Role: "tool", ToolCallID: "call_1", Content: `{"state":"off"}`},
	}
}

func fixtureCall() ModelCall {
	return ModelCall{
		LoopName:   "porch_watch",
		Iteration:  1,
		Kind:       "iteration",
		Deployment: "local/qwen",
		Profile: ServingProfile{
			Provider: "openai_compat", Resource: "runner", UpstreamModel: "qwen",
			ChatTemplateKwargs: map[string]any{"enable_thinking": false},
		},
		Messages: fixtureHistory(),
		Tools:    fixtureTools(),
		Response: &llm.Message{Role: "assistant", Content: "The porch light is off."},
		At:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

// diffCount counts positions at which a and b differ, including any
// length difference.
func diffCount(a, b []string) int {
	n := max(len(a), len(b)) - min(len(a), len(b))
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

func segmentHashes(segs []PromptSegment) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.Hash
	}
	return out
}

func TestHashModelCall_ChangeLocality(t *testing.T) {
	tests := []struct {
		name           string
		mutate         func(*ModelCall)
		wantSegDiff    int
		wantMsgDiff    int
		wantToolDiff   int
		wantSurface    bool // surface hash changes
		wantPrefix     bool // stable prefix hash changes
		wantProfile    bool // serving profile hash changes
		wantSegChanged string
	}{
		{
			name:   "identical inputs hash identically",
			mutate: func(*ModelCall) {},
		},
		{
			name: "one changed description changes the surface and exactly one tool",
			mutate: func(c *ModelCall) {
				c.Tools[1] = fixtureTool("ha_call_service", "Call a Home Assistant service. Never unlock doors.")
			},
			wantToolDiff: 1, wantSurface: true, wantPrefix: true,
		},
		{
			name: "current conditions changes one segment and not the stable prefix",
			mutate: func(c *ModelCall) {
				c.Messages[0] = fixturePrompt("2026-01-02T03:09:41Z")
			},
			wantSegDiff: 1, wantSegChanged: "CURRENT CONDITIONS",
		},
		{
			name: "a cached section change moves the stable prefix",
			mutate: func(c *ModelCall) {
				c.Messages[0].Sections[0].Content = "# Identity\nYou are the household agent for Carol."
				c.Messages[0].Content = strings.Replace(c.Messages[0].Content, "Alice and Bob", "Carol", 1)
			},
			wantSegDiff: 1, wantPrefix: true, wantSegChanged: "IDENTITY",
		},
		{
			name: "one changed tool result changes one message",
			mutate: func(c *ModelCall) {
				c.Messages[3].Content = `{"state":"on"}`
			},
			wantMsgDiff: 1,
		},
		{
			name: "kwargs change the serving profile only",
			mutate: func(c *ModelCall) {
				c.Profile.ChatTemplateKwargs = map[string]any{"enable_thinking": true}
			},
			wantProfile: true,
		},
		{
			name: "unknown kwargs hash apart from none sent",
			mutate: func(c *ModelCall) {
				c.Profile.ChatTemplateKwargs = nil
			},
			wantProfile: true,
		},
		{
			name: "build provenance is not part of the profile",
			mutate: func(c *ModelCall) {
				c.ThaneCommit, c.WireModel = "deadbeef", "instance-2"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := fixtureCall()
			changed := fixtureCall()
			tt.mutate(&changed)
			a, err := hashModelCall(base.Messages, base.Tools, base.Response, base.Profile)
			if err != nil {
				t.Fatal(err)
			}
			b, err := hashModelCall(changed.Messages, changed.Tools, changed.Response, changed.Profile)
			if err != nil {
				t.Fatal(err)
			}
			if a.segmentFallback || b.segmentFallback {
				t.Fatal("fixture prompt fell back to WHOLE")
			}
			if got := diffCount(segmentHashes(a.segments), segmentHashes(b.segments)); got != tt.wantSegDiff {
				t.Errorf("segment diff = %d, want %d", got, tt.wantSegDiff)
			}
			if tt.wantSegChanged != "" {
				for i := range a.segments {
					if a.segments[i].Hash != b.segments[i].Hash && a.segments[i].Name != tt.wantSegChanged {
						t.Errorf("segment %q changed, want only %q", a.segments[i].Name, tt.wantSegChanged)
					}
				}
			}
			if got := diffCount(a.messageHashes, b.messageHashes); got != tt.wantMsgDiff {
				t.Errorf("message diff = %d, want %d", got, tt.wantMsgDiff)
			}
			if got := diffCount(a.toolHashes, b.toolHashes); got != tt.wantToolDiff {
				t.Errorf("tool diff = %d, want %d", got, tt.wantToolDiff)
			}
			if got := a.toolSurfaceHash != b.toolSurfaceHash; got != tt.wantSurface {
				t.Errorf("surface changed = %v, want %v", got, tt.wantSurface)
			}
			if got := a.stablePrefixHash != b.stablePrefixHash; got != tt.wantPrefix {
				t.Errorf("stable prefix changed = %v, want %v", got, tt.wantPrefix)
			}
			if got := a.profileHash != b.profileHash; got != tt.wantProfile {
				t.Errorf("profile changed = %v, want %v", got, tt.wantProfile)
			}
		})
	}
}

func TestHashModelCall_SurfaceIsReconstructibleFromToolBlobs(t *testing.T) {
	c := fixtureCall()
	hc, err := hashModelCall(c.Messages, c.Tools, nil, c.Profile)
	if err != nil {
		t.Fatal(err)
	}
	parts := make([]string, len(hc.toolHashes))
	for i, h := range hc.toolHashes {
		parts[i] = string(hc.blobs.data[h])
	}
	rebuilt := []byte("[" + strings.Join(parts, ",") + "]")
	if got := blobHash(rebuilt); got != hc.toolSurfaceHash {
		t.Fatalf("surface hash from tool blobs = %s, want %s", got, hc.toolSurfaceHash)
	}
	var defs []map[string]any
	if err := json.Unmarshal(rebuilt, &defs); err != nil {
		t.Fatalf("rebuilt surface is not JSON: %v", err)
	}
	if desc := defs[2]["function"].(map[string]any)["description"]; desc != "Send Bob a message." {
		t.Fatalf("description = %v, want it kept verbatim", desc)
	}
}

func TestSegmentPrompt(t *testing.T) {
	sec := func(name, content, ttl string) llm.PromptSection {
		return llm.PromptSection{Name: name, Content: content, CacheTTL: ttl}
	}
	tests := []struct {
		name      string
		content   string
		sections  []llm.PromptSection
		wantOK    bool
		wantNames []string
	}{
		{
			name:      "separators become unnamed segments",
			content:   "alpha\n\nbeta\n\ngamma",
			sections:  []llm.PromptSection{sec("A", "alpha", "1h"), sec("B", "beta", ""), sec("C", "gamma", "")},
			wantOK:    true,
			wantNames: []string{"A", "", "B", "", "C"},
		},
		{
			name:      "leading and trailing text are kept",
			content:   "preamble alpha tail",
			sections:  []llm.PromptSection{sec("A", "alpha", "")},
			wantOK:    true,
			wantNames: []string{"", "A", ""},
		},
		{
			name:      "no sections is one whole segment, not a fallback",
			content:   "just a prompt",
			wantOK:    true,
			wantNames: []string{WholePromptSegment},
		},
		{
			name:      "a section missing from the text falls back to whole",
			content:   "alpha\n\nbeta",
			sections:  []llm.PromptSection{sec("A", "alpha", ""), sec("B", "delta", "")},
			wantOK:    false,
			wantNames: []string{WholePromptSegment},
		},
		{
			name:      "sections out of order fall back to whole",
			content:   "alpha\n\nbeta",
			sections:  []llm.PromptSection{sec("B", "beta", ""), sec("A", "alpha", "")},
			wantOK:    false,
			wantNames: []string{WholePromptSegment},
		},
		{
			name:      "content over 4096 characters stays whole in its segment",
			content:   "head\n\n" + strings.Repeat("x", 10000),
			sections:  []llm.PromptSection{sec("A", "head", ""), sec("BIG", strings.Repeat("x", 10000), "")},
			wantOK:    true,
			wantNames: []string{"A", "", "BIG"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segs, ok := segmentPrompt(tt.content, tt.sections)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			var names []string
			var rebuilt strings.Builder
			for _, s := range segs {
				names = append(names, s.Name)
				rebuilt.WriteString(s.Content)
			}
			if !slices.Equal(names, tt.wantNames) {
				t.Errorf("names = %q, want %q", names, tt.wantNames)
			}
			if rebuilt.String() != tt.content {
				t.Errorf("segments do not re-concatenate to the prompt byte for byte")
			}
		})
	}
}

func TestStablePrefixHash(t *testing.T) {
	seg := func(name, hash, ttl string) PromptSegment {
		return PromptSegment{Name: name, Hash: hash, CacheTTL: ttl}
	}
	digest := func(parts ...string) string { return blobHash([]byte(strings.Join(parts, "\n"))) }
	tests := []struct {
		name string
		segs []PromptSegment
		want string
	}{
		{
			name: "leading cached run with its inner separator",
			segs: []PromptSegment{seg("A", "a", "1h"), seg("", "g", ""), seg("B", "b", "5m"), seg("", "g", ""), seg("C", "c", "")},
			want: digest("surface", "a", "g", "b"),
		},
		{
			name: "first section uncached leaves the surface alone",
			segs: []PromptSegment{seg("A", "a", ""), seg("", "g", ""), seg("B", "b", "1h")},
			want: digest("surface"),
		},
		{
			name: "a cached section after an uncached one is not in the prefix",
			segs: []PromptSegment{seg("A", "a", "1h"), seg("", "g", ""), seg("B", "b", ""), seg("", "g", ""), seg("C", "c", "1h")},
			want: digest("surface", "a"),
		},
		{
			name: "no prompt",
			want: digest("surface"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stablePrefixHash("surface", tt.segs); got != tt.want {
				t.Errorf("stablePrefixHash = %s, want %s", got, tt.want)
			}
		})
	}
}

// danglingRefs lists every blob hash a stored call references that
// log_model_blobs does not hold, with its call's input_incomplete flag.
func danglingRefs(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT request_id, call_seq, input_incomplete, serving_profile_hash,
		prompt_segments, message_hashes, tool_hashes, COALESCE(response_hash, '')
		FROM log_model_calls`)
	if err != nil {
		t.Fatal(err)
	}
	type ref struct {
		hash       string
		incomplete bool
		where      string
	}
	var refs []ref
	for rows.Next() {
		var reqID, profile, segsJSON, msgsJSON, toolsJSON, resp string
		var seq int
		var incomplete bool
		if err := rows.Scan(&reqID, &seq, &incomplete, &profile, &segsJSON, &msgsJSON, &toolsJSON, &resp); err != nil {
			t.Fatal(err)
		}
		var segs []PromptSegment
		var msgs, tools []string
		for _, u := range []struct {
			src string
			dst any
		}{{segsJSON, &segs}, {msgsJSON, &msgs}, {toolsJSON, &tools}} {
			if err := json.Unmarshal([]byte(u.src), u.dst); err != nil {
				t.Fatal(err)
			}
		}
		hashes := append(append(append([]string{profile}, segmentHashes(segs)...), msgs...), tools...)
		if resp != "" {
			hashes = append(hashes, resp)
		}
		for _, h := range hashes {
			refs = append(refs, ref{hash: h, incomplete: incomplete, where: fmt.Sprintf("%s#%d", reqID, seq)})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()

	out := make(map[string]bool)
	for _, r := range refs {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM log_model_blobs WHERE hash = ?`, r.hash).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			out[r.where+":"+r.hash] = r.incomplete
		}
	}
	return out
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDanglingRefsDetectsAMissingBlob(t *testing.T) {
	db := openTestDB(t)
	s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test"})
	s.Turn("r_alice").Capture(fixtureCall())
	s.Close()
	if got := danglingRefs(t, db); len(got) != 0 {
		t.Fatalf("dangling before delete = %v", got)
	}
	if _, err := db.Exec(`DELETE FROM log_model_blobs WHERE hash IN (SELECT response_hash FROM log_model_calls)`); err != nil {
		t.Fatal(err)
	}
	if got := danglingRefs(t, db); len(got) != 1 {
		t.Fatalf("dangling after delete = %v, want exactly the response", got)
	}
}

func TestModelCallStore_DedupesIdenticalContent(t *testing.T) {
	db := openTestDB(t)
	s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test"})
	for _, req := range []string{"r_alice", "r_bob"} {
		turn := s.Turn(req)
		for range 2 {
			if !turn.Capture(fixtureCall()) {
				t.Fatal("capture not queued")
			}
		}
	}
	s.Close()

	one, err := hashModelCall(fixtureCall().Messages, fixtureTools(), fixtureCall().Response, fixtureCall().Profile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := countRows(t, db, `SELECT COUNT(*) FROM log_model_blobs`), len(one.blobs.order); got != want {
		t.Errorf("blobs = %d, want %d (one per distinct hash)", got, want)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls`); got != 4 {
		t.Errorf("calls = %d, want 4", got)
	}
	if got := countRows(t, db, `SELECT COUNT(DISTINCT message_hashes) FROM log_model_calls`); got != 1 {
		t.Errorf("distinct message hash lists = %d, want 1", got)
	}
	if got := s.Stats(); got.Enqueued != 4 || got.Dropped != 0 || got.WriteErrors != 0 {
		t.Errorf("stats = %+v", got)
	}
}

func TestModelCallStore_StoresLongContentWhole(t *testing.T) {
	db := openTestDB(t)
	s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test"})
	long := strings.Repeat("Bob's file contents. ", 600) // well past 4096 characters
	c := fixtureCall()
	c.Messages[3].Content = long
	s.Turn("r_bob").Capture(c)
	s.Close()

	var hashesJSON string
	if err := db.QueryRow(`SELECT message_hashes FROM log_model_calls`).Scan(&hashesJSON); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	if err := json.Unmarshal([]byte(hashesJSON), &hashes); err != nil {
		t.Fatal(err)
	}
	var blob []byte
	if err := db.QueryRow(`SELECT content FROM log_model_blobs WHERE hash = ?`, hashes[2]).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	var msg canonicalMessage
	if err := json.Unmarshal(blob, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Content != long {
		t.Fatalf("stored content length = %d, want %d", len(msg.Content), len(long))
	}
}

func TestModelCallStore_FullQueueDropLeavesNoDanglingReference(t *testing.T) {
	db := openTestDB(t)
	s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", QueueSize: 1})
	turn := s.Turn("r_alice")

	first := fixtureCall()
	if !turn.Capture(first) {
		t.Fatal("first capture not queued")
	}
	// The queue is full and nothing drains it: the second call, whose
	// new tool result exists nowhere else yet, is dropped.
	second := fixtureCall()
	second.Messages = append(second.Messages, llm.Message{Role: "user", Content: "And the garage door?"})
	if turn.Capture(second) {
		t.Fatal("second capture queued, want a full-queue drop")
	}
	s.start()
	// Wait for the drain to take the first item so the queue has room.
	deadline := time.Now().Add(5 * time.Second)
	for len(s.ch) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("drain did not take the queued item")
		}
		time.Sleep(time.Millisecond)
	}
	third := fixtureCall()
	third.Messages = append(third.Messages,
		llm.Message{Role: "user", Content: "And the garage door?"},
		llm.Message{Role: "assistant", Content: "Closed."})
	if !turn.Capture(third) {
		t.Fatal("third capture not queued")
	}
	s.Close()

	if got := danglingRefs(t, db); len(got) != 0 {
		t.Fatalf("dangling references after a drop: %v", got)
	}
	if got := s.Stats(); got.Dropped != 1 || got.Enqueued != 2 {
		t.Errorf("stats = %+v, want 1 dropped and 2 enqueued", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE input_incomplete = 1`); got != 0 {
		t.Errorf("input_incomplete rows = %d, want 0", got)
	}
}

func TestModelCallStore_FailedTransactionPoisonsRequest(t *testing.T) {
	db := openTestDB(t)
	// Fault injection in SQL: the first call of one request cannot be
	// inserted, so its whole transaction, blobs included, rolls back.
	if _, err := db.Exec(`CREATE TRIGGER inject_failure BEFORE INSERT ON log_model_calls
		WHEN NEW.request_id = 'r_poisoned' AND NEW.call_seq = 1
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", Logger: slog.New(slog.DiscardHandler)})
	poisoned, healthy := s.Turn("r_poisoned"), s.Turn("r_healthy")
	for i := range 3 {
		c := fixtureCall()
		c.Iteration = i
		healthy.Capture(c)
		// Content only the poisoned request carries, so its rolled-back
		// blobs are not stored by the healthy request either.
		c.Messages[1].Content = "Alice asked something only this request saw."
		poisoned.Capture(c)
	}
	poisoned.Finish(ModelTurn{Completed: true, At: time.Now()})
	s.Close()

	tests := []struct {
		request        string
		wantRows       int
		wantIncomplete int
	}{
		{request: "r_poisoned", wantRows: 2, wantIncomplete: 2},
		{request: "r_healthy", wantRows: 3, wantIncomplete: 0},
	}
	for _, tt := range tests {
		t.Run(tt.request, func(t *testing.T) {
			if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE request_id = ?`, tt.request); got != tt.wantRows {
				t.Errorf("rows = %d, want %d", got, tt.wantRows)
			}
			if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE request_id = ? AND input_incomplete = 1`, tt.request); got != tt.wantIncomplete {
				t.Errorf("input_incomplete rows = %d, want %d", got, tt.wantIncomplete)
			}
		})
	}
	dangling := danglingRefs(t, db)
	if len(dangling) == 0 {
		t.Fatal("expected later poisoned rows to reference the rolled-back blobs")
	}
	for where, incomplete := range dangling {
		if !incomplete {
			t.Errorf("dangling reference %s on a row not marked input_incomplete", where)
		}
	}
	if got := s.Stats().WriteErrors; got != 1 {
		t.Errorf("write errors = %d, want 1", got)
	}
	if len(s.poisoned) != 0 {
		t.Errorf("poisoned requests after Finish = %v, want none", s.poisoned)
	}
}

func TestModelCallStore_PoisonedRequestReleasedWhenFinishIsDropped(t *testing.T) {
	tests := []struct {
		name         string
		idle         time.Duration
		wantPoisoned int
	}{
		{name: "a recently active request stays poisoned", idle: time.Minute, wantPoisoned: 1},
		{name: "an idle request is released", idle: poisonedIdleTTL + time.Minute, wantPoisoned: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			if _, err := db.Exec(`CREATE TRIGGER inject_failure BEFORE INSERT ON log_model_calls
				WHEN NEW.request_id = 'r_poisoned' AND NEW.call_seq = 1
				BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
				t.Fatal(err)
			}
			s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test", QueueSize: 2, Logger: slog.New(slog.DiscardHandler)})
			var clock atomic.Int64
			clock.Store(time.Unix(1_800_000_000, 0).UnixNano())
			s.now = func() time.Time { return time.Unix(0, clock.Load()) }

			poisoned := s.Turn("r_poisoned")
			for range 2 {
				if !poisoned.Capture(fixtureCall()) {
					t.Fatal("capture not queued")
				}
			}
			// The queue is full, so the poisoned request's Finish is lost.
			if poisoned.Finish(ModelTurn{Completed: true, At: time.Now()}) {
				t.Fatal("Finish queued, want a full-queue drop")
			}
			s.start()
			// Wait for the second call's row to commit, not merely for the
			// queue to empty: an empty queue only means the drain has
			// received the item, and advancing the clock before it reads
			// the time would make the poisoned request look newly active.
			deadline := time.Now().Add(5 * time.Second)
			for countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE request_id = 'r_poisoned'`) < 1 {
				if time.Now().After(deadline) {
					t.Fatal("drain did not commit the second call")
				}
				time.Sleep(time.Millisecond)
			}
			clock.Add(int64(tt.idle))
			if !s.Turn("r_bob").Capture(fixtureCall()) {
				t.Fatal("later capture not queued")
			}
			s.Close()

			if got := len(s.poisoned); got != tt.wantPoisoned {
				t.Errorf("poisoned requests = %d, want %d", got, tt.wantPoisoned)
			}
			if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls WHERE request_id = 'r_poisoned' AND input_incomplete = 1`); got != 1 {
				t.Errorf("input_incomplete rows = %d, want 1", got)
			}
		})
	}
}

func TestModelCallStore_WireFactsNullWhenUnknown(t *testing.T) {
	zero, off := 0, false
	firstEvent := 250 * time.Millisecond
	tests := []struct {
		name          string
		mutate        func(*ModelCall)
		wantMaxTokens sql.NullInt64
		wantStream    sql.NullBool
		wantTTL       sql.NullInt64
		wantFirst     sql.NullInt64
	}{
		{
			name:   "unreported facts are NULL",
			mutate: func(*ModelCall) {},
		},
		{
			name: "reported zeros are stored as zero",
			mutate: func(c *ModelCall) {
				c.MaxTokens, c.Stream, c.TTL, c.FirstEvent = &zero, &off, &zero, &firstEvent
			},
			wantMaxTokens: sql.NullInt64{Valid: true},
			wantStream:    sql.NullBool{Valid: true},
			wantTTL:       sql.NullInt64{Valid: true},
			wantFirst:     sql.NullInt64{Int64: 250, Valid: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test"})
			c := fixtureCall()
			tt.mutate(&c)
			s.Turn("r_alice").Capture(c)
			s.Close()
			var maxTokens, ttl, first sql.NullInt64
			var stream sql.NullBool
			var toolChoice sql.NullString
			if err := db.QueryRow(`SELECT max_tokens, stream, ttl, first_event_ms, tool_choice FROM log_model_calls`).
				Scan(&maxTokens, &stream, &ttl, &first, &toolChoice); err != nil {
				t.Fatal(err)
			}
			if maxTokens != tt.wantMaxTokens || stream != tt.wantStream || ttl != tt.wantTTL || first != tt.wantFirst {
				t.Errorf("got max_tokens=%v stream=%v ttl=%v first_event_ms=%v", maxTokens, stream, ttl, first)
			}
			if toolChoice.Valid {
				t.Errorf("tool_choice = %q, want NULL (not sent)", toolChoice.String)
			}
		})
	}
}

func TestModelCallTurn_CaptureOwnsItsBytes(t *testing.T) {
	db := openTestDB(t)
	s := newModelCallStore(db, ModelCallStoreConfig{InstanceID: "test"})
	c := fixtureCall()
	turn := s.Turn("r_alice")
	turn.Capture(c)
	// The agent rebuilds msgs[0] and the engine repairs the response
	// after the call returns; neither may reach the queued row.
	c.Messages[0].Content = "rebuilt"
	c.Response.Content = "repaired"
	c.Tools[0]["function"].(map[string]any)["description"] = "mutated"
	s.start()
	s.Close()

	want, err := hashModelCall(fixtureCall().Messages, fixtureTools(), fixtureCall().Response, fixtureCall().Profile)
	if err != nil {
		t.Fatal(err)
	}
	var surface, response string
	if err := db.QueryRow(`SELECT tool_surface_hash, response_hash FROM log_model_calls`).Scan(&surface, &response); err != nil {
		t.Fatal(err)
	}
	if surface != want.toolSurfaceHash || response != want.responseHash {
		t.Fatal("stored hashes reflect mutations made after Capture returned")
	}
}

func TestModelCallTurn_NilStoreIsANoOp(t *testing.T) {
	var s *ModelCallStore
	turn := s.Turn("r_alice")
	if turn.Capture(fixtureCall()) || turn.Finish(ModelTurn{}) {
		t.Fatal("nil turn reported a queued write")
	}
	if got := s.Stats(); got != (ModelCallStats{}) {
		t.Fatalf("nil stats = %+v", got)
	}
	s.Close()
}

func TestModelCallStore_ImageDependentAndSegmentFallbackFlags(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(*ModelCall)
		wantImage     bool
		wantFallback  bool
		wantPrefixSet bool
	}{
		{name: "plain call", mutate: func(*ModelCall) {}, wantPrefixSet: true},
		{
			name: "image becomes a digest and marks the call",
			mutate: func(c *ModelCall) {
				c.Messages[1].Images = []llm.ImageContent{{Data: "aGVsbG8=", MediaType: "image/png"}}
			},
			wantImage: true, wantPrefixSet: true,
		},
		{
			name: "unlocatable sections store the prompt whole",
			mutate: func(c *ModelCall) {
				c.Messages[0].Content = "rewritten prompt"
			},
			wantFallback: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test"})
			c := fixtureCall()
			tt.mutate(&c)
			s.Turn("r_alice").Capture(c)
			s.Close()
			var image, fallback bool
			var prefix sql.NullString
			var segs string
			if err := db.QueryRow(`SELECT image_dependent, segment_fallback, stable_prefix_hash, prompt_segments FROM log_model_calls`).
				Scan(&image, &fallback, &prefix, &segs); err != nil {
				t.Fatal(err)
			}
			if image != tt.wantImage || fallback != tt.wantFallback || prefix.Valid != tt.wantPrefixSet {
				t.Errorf("image=%v fallback=%v prefix=%v", image, fallback, prefix.Valid)
			}
			if tt.wantFallback && !strings.Contains(segs, `"name":"WHOLE"`) {
				t.Errorf("prompt_segments = %s, want a single WHOLE segment", segs)
			}
			if got := s.Stats().SegmentFallbacks; (got == 1) != tt.wantFallback {
				t.Errorf("segment fallbacks = %d", got)
			}
			if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_blobs WHERE CAST(content AS TEXT) LIKE '%aGVsbG8=%'`); got != 0 {
				t.Errorf("image bytes stored in %d blobs, want only a digest", got)
			}
		})
	}
}

func TestModelCallStore_TurnRowCarriesLedger(t *testing.T) {
	db := openTestDB(t)
	s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test"})
	s.Turn("r_alice").Finish(ModelTurn{
		Completed: false, Exhausted: true, ExhaustReason: "max_iterations",
		ToolOutcomes: map[string]ToolOutcomeRecord{
			"doc_write": {Calls: 2, Failures: 1, Successes: 1, LastError: "refused",
				Targets: map[string]ToolTargetRecord{"alice.md": {Calls: 2, Failures: 1, Successes: 1}}},
		},
		At: time.Now(),
	})
	s.Close()
	var completed, exhausted bool
	var reason, ledger string
	if err := db.QueryRow(`SELECT completed, exhausted, exhaust_reason, tool_outcomes FROM log_model_turns WHERE request_id = 'r_alice'`).
		Scan(&completed, &exhausted, &reason, &ledger); err != nil {
		t.Fatal(err)
	}
	want := `{"doc_write":{"calls":2,"failures":1,"blocked":0,"successes":1,"last_error":"refused","targets":{"alice.md":{"calls":2,"failures":1,"blocked":0,"successes":1}}}}`
	if completed || !exhausted || reason != "max_iterations" || ledger != want {
		t.Errorf("turn = completed:%v exhausted:%v reason:%q ledger:%s", completed, exhausted, reason, ledger)
	}
}

func TestMigrate_ModelCallsIdempotentOnExistingLogsDB(t *testing.T) {
	db, err := sql.Open("sqlite-thane", "file:"+t.TempDir()+"/logs.db?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// A logs.db from before the eval store, whose log_request_content
	// carries an extra column Migrate does not know about.
	legacy := `
	CREATE TABLE log_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL,
		level TEXT NOT NULL, msg TEXT NOT NULL, request_id TEXT, session_id TEXT,
		conversation_id TEXT, subsystem TEXT, tool TEXT, model TEXT, loop_id TEXT,
		loop_name TEXT, source_file TEXT, source_line INTEGER, attrs TEXT,
		raw_file TEXT, raw_line INTEGER);
	CREATE TABLE log_prompts (hash TEXT PRIMARY KEY, content TEXT NOT NULL, first_seen TEXT NOT NULL);
	CREATE TABLE log_request_content (id INTEGER PRIMARY KEY AUTOINCREMENT,
		request_id TEXT NOT NULL UNIQUE, prompt_hash TEXT, user_content TEXT,
		assistant_content TEXT, model TEXT, iteration_count INTEGER, input_tokens INTEGER,
		output_tokens INTEGER, tools_used TEXT, exhausted BOOLEAN DEFAULT FALSE,
		exhaust_reason TEXT, created_at TEXT NOT NULL, extra_column TEXT);
	INSERT INTO log_entries (timestamp, level, msg) VALUES ('2026-01-01T00:00:00.000000000Z', 'INFO', 'before');
	INSERT INTO log_request_content (request_id, created_at) VALUES ('r_old', '2026-01-01T00:00:00.000000000Z');
	`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := Migrate(db); err != nil {
			t.Fatalf("Migrate pass %d: %v", i+1, err)
		}
	}
	for _, table := range []string{"log_model_calls", "log_model_blobs", "log_model_turns"} {
		if got := countRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); got != 1 {
			t.Errorf("table %s count = %d, want 1", table, got)
		}
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM log_entries`); got != 1 {
		t.Errorf("log_entries rows = %d, want the existing row kept", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM log_request_content WHERE request_id = 'r_old'`); got != 1 {
		t.Errorf("log_request_content rows = %d, want the existing row kept", got)
	}
	s := NewModelCallStore(db, ModelCallStoreConfig{InstanceID: "test"})
	s.Turn("r_new").Capture(fixtureCall())
	s.Close()
	if got := countRows(t, db, `SELECT COUNT(*) FROM log_model_calls`); got != 1 {
		t.Errorf("calls after migrate = %d, want 1", got)
	}
	// The eval store never writes the retained content tables.
	if got := countRows(t, db, `SELECT COUNT(*) FROM log_request_content`); got != 1 {
		t.Errorf("log_request_content rows = %d, want only the existing row", got)
	}
	for _, table := range []string{"log_prompts", "log_tool_content"} {
		if got := countRows(t, db, `SELECT COUNT(*) FROM `+table); got != 0 {
			t.Errorf("%s rows = %d, want 0", table, got)
		}
	}
}

func TestModelCallInstanceID(t *testing.T) {
	start := time.Unix(1700000000, 42)
	a := ModelCallInstanceID("alice-host", start)
	if strings.Contains(a, "alice-host") {
		t.Errorf("instance id %q leaks the host name", a)
	}
	if b := ModelCallInstanceID("bob-host", start); a == b {
		t.Error("different hosts share an instance id")
	}
	if c := ModelCallInstanceID("alice-host", start.Add(time.Second)); a == c {
		t.Error("different starts share an instance id")
	}
}

// largeFixtureCall is a long loop wake: a sectioned prompt of about
// 40KB, 60 tools, and 200 history messages with sizeable tool results.
func largeFixtureCall() ModelCall {
	c := fixtureCall()
	var sections []llm.PromptSection
	var b strings.Builder
	for i := range 20 {
		s := llm.PromptSection{Name: fmt.Sprintf("SECTION %d", i), Content: strings.Repeat(fmt.Sprintf("Guidance %d for Alice. ", i), 90)}
		if i < 12 {
			s.CacheTTL = "1h"
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s.Content)
		sections = append(sections, s)
	}
	msgs := []llm.Message{{Role: "system", Content: b.String(), Sections: sections}}
	for i := range 100 {
		call := llm.ToolCall{ID: fmt.Sprintf("call_%d", i)}
		call.Function.Name = "ha_get_state"
		call.Function.Arguments = map[string]any{"entity_id": fmt.Sprintf("sensor.room_%d", i), "attributes": []any{"temperature", "humidity"}}
		msgs = append(msgs,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
			llm.Message{Role: "tool", ToolCallID: call.ID, Content: strings.Repeat(`{"state":"21.5","unit":"C"},`, 150)},
		)
	}
	c.Messages = msgs
	c.Tools = make([]map[string]any, 60)
	for i := range c.Tools {
		c.Tools[i] = fixtureTool(fmt.Sprintf("tool_%d", i), strings.Repeat("Describes what Bob's tool does. ", 12))
	}
	return c
}

func BenchmarkHashModelCall(b *testing.B) {
	c := largeFixtureCall()
	var size int64
	for _, m := range c.Messages {
		size += int64(len(m.Content))
	}
	b.SetBytes(size)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := hashModelCall(c.Messages, c.Tools, c.Response, c.Profile); err != nil {
			b.Fatal(err)
		}
	}
}
