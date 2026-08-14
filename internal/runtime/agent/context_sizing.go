package agent

import (
	"encoding/json"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// Context-window arithmetic. These estimates answer two different questions
// and are deliberately not the same number: how much context a request will
// occupy (used to pick a model and to report usage), and how large a window
// to ask a runner to load for it (which must also cover the tool schemas and
// leave room to generate into).

const estimatedImageContextTokens = 1536

// reservedOutputContextTokens is the generation headroom folded into any
// context window Thane asks a runner to load. Thane sends no max_tokens, so
// this is a judgement call rather than a derived bound: enough room for a
// long answer or a burst of tool calls, small enough to stay well inside the
// advertised maximum of the local models this applies to.
const reservedOutputContextTokens = 4096

func estimateLLMMessagesContextTokens(msgs []llm.Message) int {
	total := 0
	for _, msg := range msgs {
		total += roughTokenCount(msg.Content)
		total += len(msg.Images) * estimatedImageContextTokens
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

// estimateLoadContextTokens sizes the context window to ask a runner for
// when the runner loads a model at a caller-chosen size, as LM Studio does.
//
// It counts everything that has to fit: the messages, the tool schemas sent
// beside them, and headroom to generate into. Prompt and completion share
// one loaded window, so a window sized to the prompt alone is already full
// when the first token is generated.
func estimateLoadContextTokens(msgs []llm.Message, toolDefs []map[string]any) int {
	return estimateLLMMessagesContextTokens(msgs) +
		estimateToolDefsContextTokens(toolDefs) +
		reservedOutputContextTokens
}

func roughTokenCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}
