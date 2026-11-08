package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// ollamaReplyScript is one agent run as the /api/chat handler sees it:
// the events the run reports to its stream, in order, and the reply it
// returns.
type ollamaReplyScript struct {
	events []agent.StreamEvent
	reply  string
}

// ollamaScriptRunner is a [loop.Runner] that plays the script queued
// by next: it reports the script's events to the run's stream, when the
// run has one, and returns the script's reply. A run with no script
// queued fails, so a request that runs twice or not at all is caught.
type ollamaScriptRunner struct {
	mu     sync.Mutex
	queued *ollamaReplyScript
}

func (r *ollamaScriptRunner) next(script ollamaReplyScript) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queued = &script
}

func (r *ollamaScriptRunner) pending() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queued != nil
}

func (r *ollamaScriptRunner) Run(_ context.Context, _ loop.Request, stream loop.StreamCallback) (*loop.Response, error) {
	r.mu.Lock()
	script := r.queued
	r.queued = nil
	r.mu.Unlock()
	if script == nil {
		return nil, fmt.Errorf("no script queued for this run")
	}
	if stream != nil {
		for _, event := range script.events {
			stream(event)
		}
	}
	return &loop.Response{Content: script.reply, Model: "test-model"}, nil
}

// ollamaReplyListener is one way the code serves /api/chat.
type ollamaReplyListener struct {
	name string
	srv  *httptest.Server
}

// ollamaReplyListeners serves /api/chat both ways the code registers
// it, the dedicated Ollama listener and the single-port routes, each
// dispatching through one OWU tracker whose conversation loops run
// runner.
func ollamaReplyListeners(t *testing.T, runner loop.Runner) []ollamaReplyListener {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	registry := loop.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		registry.ShutdownAll(shutdownCtx)
	})

	logger := testAPILogger()
	tracker, err := NewOWUTracker(ctx, registry, events.New(), runner, logger)
	if err != nil {
		t.Fatalf("NewOWUTracker: %v", err)
	}

	// The handler reads the router's quality floor off the loop and
	// otherwise dispatches through the tracker, so a zero-value loop
	// is all either registration needs.
	ollama := NewOllamaServer("", 0, "", nil, &agent.Loop{}, logger)
	ollama.SetOWUTracker(tracker)
	single := &Server{loop: &agent.Loop{}, owuTracker: tracker, logger: logger}
	mux := http.NewServeMux()
	single.RegisterOllamaRoutes(mux, "")

	listeners := []ollamaReplyListener{
		{name: "ollama listener", srv: httptest.NewServer(ollama.Handler())},
		{name: "single-port routes", srv: httptest.NewServer(mux)},
	}
	for _, l := range listeners {
		t.Cleanup(l.srv.Close)
	}
	return listeners
}

// postOllamaChat sends one user message to /api/chat on srv and
// returns the response; the caller closes its body.
func postOllamaChat(t *testing.T, srv *httptest.Server, user string, stream bool) *http.Response {
	t.Helper()
	body, err := json.Marshal(OllamaChatRequest{
		Model:    "thane:latest",
		Messages: []OllamaChatMessage{{Role: "user", Content: user}},
		Stream:   &stream,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := srv.Client().Post(srv.URL+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("POST /api/chat status = %d, want 200", resp.StatusCode)
	}
	return resp
}

// ollamaUnstreamedReply returns the message content of an unstreamed
// /api/chat answer to user.
func ollamaUnstreamedReply(t *testing.T, srv *httptest.Server, user string) string {
	t.Helper()
	resp := postOllamaChat(t, srv, user, false)
	defer resp.Body.Close()
	var got OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode unstreamed reply: %v", err)
	}
	return got.Message.Content
}

// ollamaStreamedChunks returns the message content of every chunk a
// streamed /api/chat answer to user writes between the empty chunk
// that opens the stream and the done chunk that ends it, failing when
// the stream is not framed that way.
func ollamaStreamedChunks(t *testing.T, srv *httptest.Server, user string) []string {
	t.Helper()
	resp := postOllamaChat(t, srv, user, true)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("Content-Type = %q, want application/x-ndjson", ct)
	}

	var chunks []OllamaChatResponse
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var chunk OllamaChatResponse
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			t.Fatalf("stream line %d is not a chunk: %v (%q)", len(chunks), err, scanner.Text())
		}
		chunks = append(chunks, chunk)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}

	if len(chunks) < 2 {
		t.Fatalf("stream wrote %d chunks, want an opening chunk and a done chunk at least", len(chunks))
	}
	if open := chunks[0]; open.Done || open.Message.Content != "" {
		t.Fatalf("opening chunk = %+v, want empty and not done", open)
	}
	done := chunks[len(chunks)-1]
	if !done.Done || done.DoneReason != "stop" || done.Message.Content != "" {
		t.Fatalf("last chunk = %+v, want done with reason stop and no content", done)
	}
	middle := make([]string, 0, len(chunks)-2)
	for i, chunk := range chunks[1 : len(chunks)-1] {
		if chunk.Done {
			t.Fatalf("chunk %d is done before the end of the stream: %+v", i+1, chunk)
		}
		if chunk.Message.Role != "assistant" {
			t.Fatalf("chunk %d role = %q, want assistant", i+1, chunk.Message.Role)
		}
		middle = append(middle, chunk.Message.Content)
	}
	return middle
}

// ollamaReplyCase is one run the stream contract is checked against.
type ollamaReplyCase struct {
	name   string
	script ollamaReplyScript
	// wantChunks is the content of every chunk the stream writes
	// between its opening chunk and its done chunk.
	wantChunks []string
	// unstreamedText marks a run whose stream must deliver exactly the
	// text an unstreamed request for the same run delivers.
	unstreamedText bool
}

// checkOllamaReplyCase runs c through srv unstreamed and then streamed,
// and checks each answer against the case.
func checkOllamaReplyCase(t *testing.T, srv *httptest.Server, runner *ollamaScriptRunner, user string, c ollamaReplyCase) {
	t.Helper()

	runner.next(c.script)
	unstreamed := ollamaUnstreamedReply(t, srv, user)
	if runner.pending() {
		t.Fatal("unstreamed request never ran its script")
	}
	if unstreamed != c.script.reply {
		t.Fatalf("unstreamed reply = %q, want the run's reply %q", unstreamed, c.script.reply)
	}

	runner.next(c.script)
	chunks := ollamaStreamedChunks(t, srv, user)
	if runner.pending() {
		t.Fatal("streamed request never ran its script")
	}
	if !slices.Equal(chunks, c.wantChunks) {
		t.Errorf("streamed chunks = %q, want %q", chunks, c.wantChunks)
	}
	if c.unstreamedText {
		if got := strings.Join(chunks, ""); got != unstreamed {
			t.Errorf("streamed text = %q, want the unstreamed reply %q", got, unstreamed)
		}
	}
}

// ollamaTokenings returns every way to cut s into non-empty tokens at
// rune boundaries. The empty string has one tokening, with no tokens.
func ollamaTokenings(s string) [][]string {
	if s == "" {
		return [][]string{nil}
	}
	var out [][]string
	for end := 1; end <= len(s); end++ {
		if end < len(s) && !utf8.RuneStart(s[end]) {
			continue
		}
		for _, rest := range ollamaTokenings(s[end:]) {
			out = append(out, append([]string{s[:end]}, rest...))
		}
	}
	return out
}

// ollamaRunes returns s cut into one token per rune.
func ollamaRunes(s string) []string {
	var out []string
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}

// ollamaRunePrefix returns the first k runes of s.
func ollamaRunePrefix(s string, k int) string {
	return string([]rune(s)[:k])
}

// ollamaScriptEvents frames tokens and tool calls the way the agent
// loop reports a model call: an LLM-start event first and the
// response and done events last, which the handler must ignore.
func ollamaScriptEvents(body ...agent.StreamEvent) []agent.StreamEvent {
	events := []agent.StreamEvent{{Kind: agent.KindLLMStart}}
	events = append(events, body...)
	return append(events, agent.StreamEvent{Kind: agent.KindLLMResponse}, agent.StreamEvent{Kind: agent.KindDone})
}

// ollamaTokenEvents returns one token event per token.
func ollamaTokenEvents(tokens []string) []agent.StreamEvent {
	events := make([]agent.StreamEvent, 0, len(tokens))
	for _, token := range tokens {
		events = append(events, agent.StreamEvent{Kind: agent.KindToken, Token: token})
	}
	return events
}

// ollamaToolCallEvents returns the start and done events of one tool call.
func ollamaToolCallEvents() []agent.StreamEvent {
	return []agent.StreamEvent{{Kind: agent.KindToolCallStart}, {Kind: agent.KindToolCallDone, ToolName: "get_state"}}
}

// ollamaTwoCuts returns every way to cut s into two non-empty tokens
// at a rune boundary.
func ollamaTwoCuts(s string) [][]string {
	var out [][]string
	for i := range s {
		if i > 0 {
			out = append(out, []string{s[:i], s[i:]})
		}
	}
	return out
}

// ollamaStripTools is the tool a provider is offered when it strips a
// tool-call payload from the end of a reply.
var ollamaStripTools = []string{"get_state"}

// ollamaTrailingPayloads are tool calls a model writes as text after
// its reply, each with the separator it writes before them: a fenced
// call and flat calls. A provider streams them with the reply and then
// strips them, and the whitespace around them, from the reply it
// returns.
var ollamaTrailingPayloads = []string{
	"\n\n```json\n{\"name\":\"get_state\",\"arguments\":{\"entity_id\":\"light.garage\"}}\n```",
	" {\"name\":\"get_state\"}",
	"\n\n{\"name\":\"get_state\"}\n",
}

// ollamaGeneratedReplyCases generates the runs the stream contract
// covers: replies no token carried (the greeting fast path, the
// runtime's fallback and timeout replies, an empty reply), a reply
// streamed in every chunking and cut short after every rune, retried
// and abandoned streams that diverge from the reply, replies a
// provider stripped a streamed tool-call payload from, a tool call at
// every position in the token stream, narration before a tool call
// followed by every kind of reply, and empty tokens at every position.
func ollamaGeneratedReplyCases(t *testing.T) []ollamaReplyCase {
	t.Helper()
	const reply = "Done ✓"
	unstreamedReplies := []string{
		prompts.InteractiveEmptyResponseFallback,
		prompts.EmptyResponseFallback,
		fmt.Sprintf(prompts.TimeoutRecoveryFallback, 0, "none"),
		prompts.TimeoutRecoveryEmpty,
	}
	runeCount := utf8.RuneCountInString(reply)
	var cases []ollamaReplyCase
	seen := make(map[string]bool)
	add := func(c ollamaReplyCase) {
		// Two tokenings of a one-rune text are the same run.
		if !seen[c.name] {
			seen[c.name] = true
			cases = append(cases, c)
		}
	}

	// Replies no token carried.
	for _, text := range append([]string{"Hey! What can I help you with?", ""}, unstreamedReplies...) {
		var want []string
		if text != "" {
			want = []string{text}
		}
		add(ollamaReplyCase{
			name:           fmt.Sprintf("nothing streamed, reply %q", text),
			script:         ollamaReplyScript{events: ollamaScriptEvents(), reply: text},
			wantChunks:     want,
			unstreamedText: true,
		})
	}

	// The reply streamed live in every chunking, whole or cut short
	// after any rune: the rest goes out as one chunk.
	for k := 0; k <= runeCount; k++ {
		prefix := ollamaRunePrefix(reply, k)
		for _, tokens := range ollamaTokenings(prefix) {
			want := slices.Clone(tokens)
			if rest := reply[len(prefix):]; rest != "" {
				want = append(want, rest)
			}
			add(ollamaReplyCase{
				name:           fmt.Sprintf("streamed %q of %q", tokens, reply),
				script:         ollamaReplyScript{events: ollamaScriptEvents(ollamaTokenEvents(tokens)...), reply: reply},
				wantChunks:     want,
				unstreamedText: true,
			})
		}
	}

	// A call abandoned after a few runes and retried in full: the
	// stream already ends with the reply.
	for k := 1; k < runeCount; k++ {
		abandoned := ollamaRunePrefix(reply, k)
		for _, first := range [][]string{{abandoned}, ollamaRunes(abandoned)} {
			for _, retried := range [][]string{{reply}, ollamaRunes(reply)} {
				tokens := slices.Concat(first, retried)
				add(ollamaReplyCase{
					name:       fmt.Sprintf("abandoned %q then retried %q", first, retried),
					script:     ollamaReplyScript{events: ollamaScriptEvents(ollamaTokenEvents(tokens)...), reply: reply},
					wantChunks: tokens,
				})
			}
		}
	}

	// A stream cut short whose run ends on a reply no token carried:
	// the reply follows the abandoned text after a blank line. An empty
	// reply needs nothing more.
	for k := 1; k <= runeCount; k++ {
		abandoned := ollamaRunePrefix(reply, k)
		for _, tokens := range [][]string{{abandoned}, ollamaRunes(abandoned)} {
			for _, text := range append([]string{""}, unstreamedReplies...) {
				want := slices.Clone(tokens)
				if text != "" {
					want = append(want, "\n\n"+text)
				}
				add(ollamaReplyCase{
					name:       fmt.Sprintf("abandoned %q then reply %q", tokens, text),
					script:     ollamaReplyScript{events: ollamaScriptEvents(ollamaTokenEvents(tokens)...), reply: text},
					wantChunks: want,
				})
			}
		}
	}

	// The reply streamed with a tool-call payload after it, which the
	// provider stripped from the reply it returned, in every chunking a
	// cut can make: the tokens already hold the reply, so nothing more
	// is written.
	for _, lead := range []string{"", "\n"} {
		for _, payload := range ollamaTrailingPayloads {
			text := lead + reply + payload
			if stripped := llm.StripTrailingToolCallText(text, ollamaStripTools, llm.DefaultToolCallTextProfile()); stripped != reply {
				t.Fatalf("StripTrailingToolCallText(%q) = %q, want %q: the payload is not one a provider strips", text, stripped, reply)
			}
			chunkings := append([][]string{{text}, ollamaRunes(text)}, ollamaTwoCuts(text)...)
			for _, tokens := range chunkings {
				add(ollamaReplyCase{
					name:       fmt.Sprintf("streamed %q, provider stripped it to %q", tokens, reply),
					script:     ollamaReplyScript{events: ollamaScriptEvents(ollamaTokenEvents(tokens)...), reply: reply},
					wantChunks: tokens,
				})
			}
		}
	}

	// A tool call at every position in the token stream. First, it
	// drops the tokens and the reply goes out whole; after a token, the
	// tokens are written live and a reply they do not carry follows
	// them after a blank line.
	for _, tokens := range [][]string{{reply}, ollamaRunes(reply), {"Do", "ne ✓"}} {
		for p := 0; p <= len(tokens); p++ {
			for _, text := range []string{reply, unstreamedReplies[0], ""} {
				body := slices.Concat(ollamaTokenEvents(tokens[:p]), ollamaToolCallEvents(), ollamaTokenEvents(tokens[p:]))
				var want []string
				switch {
				case p == 0:
					want = []string{text}
				case text == reply || text == "":
					want = tokens
				default:
					want = append(slices.Clone(tokens), "\n\n"+text)
				}
				add(ollamaReplyCase{
					name:           fmt.Sprintf("tool call after %q of %q, reply %q", tokens[:p], tokens, text),
					script:         ollamaReplyScript{events: ollamaScriptEvents(body...), reply: text},
					wantChunks:     want,
					unstreamedText: p == 0 || text == reply,
				})
			}
		}
	}

	// Narration streamed before a tool call, then a final iteration
	// that streams the reply, part of the run's text, or nothing, and
	// every kind of reply. The narration ends the run as deferred text
	// when the final iteration writes nothing. A reply the tokens carry
	// adds nothing; any other follows them after a blank line.
	const narration = "Checking. "
	for _, said := range [][]string{{narration}, ollamaRunes(narration)} {
		for _, final := range [][]string{nil, {reply}, ollamaRunes(reply)} {
			replies := append([]string{reply, narration, ""}, unstreamedReplies...)
			for _, text := range replies {
				body := slices.Concat(ollamaTokenEvents(said), ollamaToolCallEvents(), ollamaTokenEvents(final))
				want := slices.Concat(said, final)
				carried := text == "" || text == narration || (text == reply && len(final) > 0)
				if !carried {
					want = append(want, "\n\n"+text)
				}
				add(ollamaReplyCase{
					name:       fmt.Sprintf("narration %q, tool call, then %q, reply %q", said, final, text),
					script:     ollamaReplyScript{events: ollamaScriptEvents(body...), reply: text},
					wantChunks: want,
				})
			}
		}
	}

	// An empty token at every position carries no text: it writes no
	// chunk, and a tool call after nothing but empty tokens came first.
	for k := 0; k <= runeCount; k++ {
		prefix := ollamaRunePrefix(reply, k)
		runes := ollamaRunes(prefix)
		for p := 0; p <= len(runes); p++ {
			tokens := slices.Concat(runes[:p], []string{""}, runes[p:])
			want := slices.Clone(runes)
			if rest := reply[len(prefix):]; rest != "" {
				want = append(want, rest)
			}
			add(ollamaReplyCase{
				name:           fmt.Sprintf("streamed %q of %q with an empty token", tokens, reply),
				script:         ollamaReplyScript{events: ollamaScriptEvents(ollamaTokenEvents(tokens)...), reply: reply},
				wantChunks:     want,
				unstreamedText: true,
			})
		}
	}
	for _, empties := range [][]string{{""}, {"", ""}} {
		for _, after := range [][]string{nil, {reply}, ollamaRunes(reply)} {
			for _, text := range []string{reply, unstreamedReplies[0], ""} {
				body := slices.Concat(ollamaTokenEvents(empties), ollamaToolCallEvents(), ollamaTokenEvents(after))
				add(ollamaReplyCase{
					name:           fmt.Sprintf("tool call after empty tokens %q, then %q, reply %q", empties, after, text),
					script:         ollamaReplyScript{events: ollamaScriptEvents(body...), reply: text},
					wantChunks:     []string{text},
					unstreamedText: true,
				})
			}
		}
	}
	return cases
}

// TestOllamaChatStream_ReplyContract checks the stream contract of
// handleOllamaStreamingChatShared over a generated class of runs, each
// sent as an ordinary chat and as an Open WebUI auxiliary request,
// through both registrations of /api/chat.
func TestOllamaChatStream_ReplyContract(t *testing.T) {
	runner := &ollamaScriptRunner{}
	listeners := ollamaReplyListeners(t, runner)
	users := []struct {
		name, message string
	}{
		{"chat", "turn on the garage lights"},
		{"auxiliary", "Generate a concise, 3-5 word title with an emoji summarizing the chat history."},
	}
	cases := ollamaGeneratedReplyCases(t)

	for _, l := range listeners {
		for _, u := range users {
			if got := isOWUAuxiliaryRequest([]agent.Message{{Role: "user", Content: u.message}}); got != (u.name == "auxiliary") {
				t.Fatalf("isOWUAuxiliaryRequest(%q) = %v, the %s user message is mislabeled", u.message, got, u.name)
			}
			for _, c := range cases {
				t.Run(l.name+"/"+u.name+"/"+c.name, func(t *testing.T) {
					checkOllamaReplyCase(t, l.srv, runner, u.message, c)
				})
			}
		}
	}
}

// TestOllamaChatStream_NamedReplies pins the replies the stream contract
// was written for, each with the chunks a client receives.
func TestOllamaChatStream_NamedReplies(t *testing.T) {
	const (
		chat  = "turn on the garage lights"
		lit   = "The garage lights are on."
		title = "Generate a concise, 3-5 word title with an emoji summarizing the chat history."
	)
	timeout := fmt.Sprintf(prompts.TimeoutRecoveryFallback, 0, "none")
	tokens := func(tokens ...string) []agent.StreamEvent { return ollamaTokenEvents(tokens) }

	rows := []struct {
		name   string
		user   string
		events []agent.StreamEvent
		reply  string
		want   []string
		// stripped marks a reply a provider made by stripping a
		// tool-call payload from the text its tokens streamed.
		stripped bool
	}{
		{
			name:  "greeting fast path",
			user:  "hello",
			reply: "Hey! What can I help you with?",
			want:  []string{"Hey! What can I help you with?"},
		},
		{
			name:  "empty-response fallback",
			user:  chat,
			reply: prompts.InteractiveEmptyResponseFallback,
			want:  []string{prompts.InteractiveEmptyResponseFallback},
		},
		{
			name:  "timeout notice",
			user:  chat,
			reply: timeout,
			want:  []string{timeout},
		},
		{
			name:  "auxiliary title",
			user:  title,
			reply: "🏠 Garage Lights On",
			want:  []string{"🏠 Garage Lights On"},
		},
		{
			name:   "streamed reply",
			user:   chat,
			events: tokens("The garage ", "lights are on."),
			reply:  lit,
			want:   []string{"The garage ", "lights are on."},
		},
		{
			name:   "reply cut short",
			user:   chat,
			events: tokens("The garage "),
			reply:  lit,
			want:   []string{"The garage ", "lights are on."},
		},
		{
			name:   "retried reply",
			user:   chat,
			events: tokens("The gar", lit),
			reply:  lit,
			want:   []string{"The gar", lit},
		},
		{
			name:   "fallback after an abandoned stream",
			user:   chat,
			events: tokens("The garage "),
			reply:  prompts.InteractiveEmptyResponseFallback,
			want:   []string{"The garage ", "\n\n" + prompts.InteractiveEmptyResponseFallback},
		},
		{
			name:   "tool call first",
			user:   chat,
			events: slices.Concat(ollamaToolCallEvents(), tokens("Checking. ", lit)),
			reply:  lit,
			want:   []string{lit},
		},
		{
			name:   "tool call after a token",
			user:   chat,
			events: slices.Concat(tokens("Checking. "), ollamaToolCallEvents(), tokens(lit)),
			reply:  lit,
			want:   []string{"Checking. ", lit},
		},
		{
			name:     "provider stripped a fenced tool call after the reply",
			user:     chat,
			events:   tokens(lit+"\n\n", "```json\n", `{"name":"get_state","arguments":{"entity_id":"light.garage"}}`, "\n```"),
			reply:    lit,
			want:     []string{lit + "\n\n", "```json\n", `{"name":"get_state","arguments":{"entity_id":"light.garage"}}`, "\n```"},
			stripped: true,
		},
		{
			name:     "provider stripped a flat tool call after the reply",
			user:     chat,
			events:   tokens(lit, ` {"name":"get_state"}`),
			reply:    lit,
			want:     []string{lit, ` {"name":"get_state"}`},
			stripped: true,
		},
		{
			name:   "timeout notice after narration and a tool call",
			user:   chat,
			events: slices.Concat(tokens("Let me check the garage."), ollamaToolCallEvents()),
			reply:  timeout,
			want:   []string{"Let me check the garage.", "\n\n" + timeout},
		},
		{
			name:   "fallback after narration and a tool call",
			user:   chat,
			events: slices.Concat(tokens("Let me check the garage."), ollamaToolCallEvents()),
			reply:  prompts.InteractiveEmptyResponseFallback,
			want:   []string{"Let me check the garage.", "\n\n" + prompts.InteractiveEmptyResponseFallback},
		},
		{
			name:   "narration kept as the reply after a tool call",
			user:   chat,
			events: slices.Concat(tokens("Let me check the garage."), ollamaToolCallEvents()),
			reply:  "Let me check the garage.",
			want:   []string{"Let me check the garage."},
		},
		{
			name:   "empty token before a tool call",
			user:   chat,
			events: slices.Concat(tokens(""), ollamaToolCallEvents(), tokens("Checking. ")),
			reply:  lit,
			want:   []string{lit},
		},
		{
			name:  "empty reply",
			user:  chat,
			reply: "",
			want:  nil,
		},
	}

	for _, row := range rows {
		if !row.stripped {
			continue
		}
		var text strings.Builder
		for _, event := range row.events {
			text.WriteString(event.Token)
		}
		if got := llm.StripTrailingToolCallText(text.String(), ollamaStripTools, llm.DefaultToolCallTextProfile()); got != row.reply {
			t.Fatalf("%s: StripTrailingToolCallText(%q) = %q, want the row's reply %q", row.name, text.String(), got, row.reply)
		}
	}

	runner := &ollamaScriptRunner{}
	for _, l := range ollamaReplyListeners(t, runner) {
		for _, row := range rows {
			t.Run(l.name+"/"+row.name, func(t *testing.T) {
				runner.next(ollamaReplyScript{events: row.events, reply: row.reply})
				got := ollamaStreamedChunks(t, l.srv, row.user)
				if runner.pending() {
					t.Fatal("streamed request never ran its script")
				}
				if !slices.Equal(got, row.want) {
					t.Errorf("streamed chunks = %q, want %q", got, row.want)
				}
			})
		}
	}
}

// ollamaHolds reports whether reply appears anywhere in streamed,
// comparing every window of streamed against it.
func ollamaHolds(streamed, reply string) bool {
	for i := 0; i+len(reply) <= len(streamed); i++ {
		if streamed[i:i+len(reply)] == reply {
			return true
		}
	}
	return false
}

// TestOllamaStreamTail checks ollamaStreamTail over every pair of
// strings up to four letters from a two-letter alphabet.
func TestOllamaStreamTail(t *testing.T) {
	words := []string{""}
	for frontier := []string{""}; len(frontier[0]) < 4; {
		var grown []string
		for _, w := range frontier {
			grown = append(grown, w+"a", w+"b")
		}
		words = append(words, grown...)
		frontier = grown
	}

	for _, streamed := range words {
		for _, reply := range words {
			tail := ollamaStreamTail(streamed, reply)
			assembled := streamed + tail
			if !ollamaHolds(assembled, reply) {
				t.Errorf("ollamaStreamTail(%q, %q) = %q: %q does not hold the reply", streamed, reply, tail, assembled)
			}
			switch {
			case strings.HasPrefix(reply, streamed):
				if assembled != reply {
					t.Errorf("ollamaStreamTail(%q, %q) = %q, want the rest of the reply", streamed, reply, tail)
				}
			case ollamaHolds(streamed, reply):
				if tail != "" {
					t.Errorf("ollamaStreamTail(%q, %q) = %q, want nothing", streamed, reply, tail)
				}
			default:
				if tail != "\n\n"+reply {
					t.Errorf("ollamaStreamTail(%q, %q) = %q, want a blank line and the reply", streamed, reply, tail)
				}
			}
		}
	}
}
