package agent

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// Context-window arithmetic. These estimates answer two different questions
// and are deliberately not the same number: how much context a request will
// occupy (messages and the visible tool schemas, used to pick a model), and
// how large a window to ask a runner to load (which also leaves room to
// generate into).

const estimatedImageContextTokens = 1536

// reservedOutputContextTokens is the generation headroom folded into any
// context window Thane asks a runner to load. Thane sends no max_tokens, so
// this is a judgement call rather than a derived bound: enough room for a
// long answer or a burst of tool calls, small enough to stay well inside the
// advertised maximum of the local models this applies to.
const reservedOutputContextTokens = 4096

// requestEstimate is one model call's estimated size, split by where the
// tokens come from. The split is what makes a gap against the provider's
// reported input_tokens diagnosable: a gap that stays constant across
// iterations points at a fixed component such as the tool schemas, one
// that grows points at the conversation.
type requestEstimate struct {
	// Messages counts the message text, attached images, and the id a
	// tool result carries to name the call it answers.
	Messages int

	// ToolCalls counts the tool calls assistant messages carry: each
	// call's id, function name and JSON-encoded arguments.
	ToolCalls int

	// Tools counts the tool schemas sent beside the messages.
	Tools int
}

// total is the whole request: everything the runner tokenizes as input.
func (e requestEstimate) total() int {
	return e.Messages + e.ToolCalls + e.Tools
}

// estimateRequest estimates one model call from exactly what is sent: the
// messages and the tool definitions offered with them.
func estimateRequest(msgs []llm.Message, toolDefs []map[string]any) requestEstimate {
	var e requestEstimate
	for _, msg := range msgs {
		e.Messages += roughTokenCount(msg.Content)
		e.Messages += len(msg.Images) * estimatedImageContextTokens
		e.Messages += roughTokenCount(msg.ToolCallID)
		e.ToolCalls += estimateToolCallsTokens(msg.ToolCalls)
	}
	e.Tools = estimateToolDefsContextTokens(toolDefs)
	return e
}

func estimateLLMMessagesContextTokens(msgs []llm.Message) int {
	e := estimateRequest(msgs, nil)
	return e.Messages + e.ToolCalls
}

// estimateToolCallsTokens counts the tool calls an assistant message
// carries besides its text. Providers serialize a call's id, function name and JSON
// arguments into the message itself, so a conversation several
// tool-calling iterations deep occupies considerably more of the window
// than its prose suggests.
func estimateToolCallsTokens(calls []llm.ToolCall) int {
	total := 0
	for _, tc := range calls {
		total += roughTokenCount(tc.ID)
		total += roughTokenCount(tc.Function.Name)
		if len(tc.Function.Arguments) == 0 {
			continue
		}
		encoded, err := json.Marshal(tc.Function.Arguments)
		if err != nil {
			continue
		}
		total += roughTokenCount(string(encoded))
	}
	return total
}

// estimateToolDefsContextTokens approximates what the tool schemas cost in
// the context window. They are marshalled the way they travel on the wire
// because that is what the runner tokenizes: a full agent tool surface is
// tens of kilobytes of JSON schema, large enough to overrun a window sized
// from the messages alone.
func estimateToolDefsContextTokens(toolDefs []map[string]any) int {
	if len(toolDefs) == 0 {
		return 0
	}
	encoded, err := json.Marshal(toolDefs)
	if err != nil {
		return 0
	}
	return roughTokenCount(string(encoded))
}

// estimateRequestContextTokens estimates what a request will occupy: the
// messages, and the tool schemas that travel beside them. This is the size a
// deployment has to be able to hold for the request to be servable at all,
// and so it is the size to judge a deployment's compatibility against.
func estimateRequestContextTokens(msgs []llm.Message, toolDefs []map[string]any) int {
	return estimateRequest(msgs, toolDefs).total()
}

// systemSectionBytes reports the size in bytes of each named section of
// the system prompt, in prompt order, as one log attribute. Sizes are
// bytes rather than estimated tokens because they are exact: they say
// which section grew between two deploys without depending on the
// estimator. A name that appears twice is summed under its first
// position. It reports false when the request carries no sectioned
// system prompt.
func systemSectionBytes(msgs []llm.Message) (slog.Attr, bool) {
	if len(msgs) == 0 || msgs[0].Role != "system" || len(msgs[0].Sections) == 0 {
		return slog.Attr{}, false
	}
	var order []string
	sizes := make(map[string]int, len(msgs[0].Sections))
	for _, section := range msgs[0].Sections {
		if _, seen := sizes[section.Name]; !seen {
			order = append(order, section.Name)
		}
		sizes[section.Name] += len(section.Content)
	}
	attrs := make([]slog.Attr, 0, len(order))
	for _, name := range order {
		attrs = append(attrs, slog.Int(name, sizes[name]))
	}
	return slog.Attr{Key: "system_section_bytes", Value: slog.GroupValue(attrs...)}, true
}

// desiredLoadContextTokens turns the size a request requires into the size
// worth loading for it: room to generate an answer as well, and never more
// than the deployment can hold.
//
// Headroom is a preference, not a requirement. Prompt and completion share
// one loaded window, so a window sized to the prompt exactly is full at the
// first generated token — but a model that can hold the prompt and less than
// the full headroom can still answer, and asking for headroom must never be
// what makes such a request look impossible. That is why this is applied
// when choosing a load size and not when judging compatibility.
func desiredLoadContextTokens(requiredTokens, maxContextWindow int) int {
	desired := requiredTokens + reservedOutputContextTokens
	if maxContextWindow > 0 && desired > maxContextWindow {
		return maxContextWindow
	}
	return desired
}

// growLoadContextTokens sizes a reload after the runner has rejected a
// request the estimate said would fit.
//
// The estimate cannot be trusted here — the runner has just demonstrated it
// was low — so it serves only as a floor, and the growth comes from doubling
// the window that actually failed. That is deliberately not a jump straight
// to the advertised maximum: on local runners a window costs load time and
// resident memory in proportion to its size, and the maximum can be twenty
// times what the request needs. Doubling recovers from an estimate that was
// somewhat wrong without paying for one that was catastrophically wrong.
func growLoadContextTokens(requiredTokens, loadedWindow, maxContextWindow int) int {
	target := desiredLoadContextTokens(requiredTokens, maxContextWindow)
	if doubled := loadedWindow * 2; doubled > target {
		target = doubled
	}
	if maxContextWindow > 0 && target > maxContextWindow {
		target = maxContextWindow
	}
	return target
}

func roughTokenCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}
