package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// ModelCallContent captures one logical provider call before and after model
// inference. It is retained only when request content retention is enabled.
// Tool execution is deliberately outside this record: the response is the
// model's decision, while later tool messages remain inputs to the next call.
type ModelCallContent struct {
	// Iteration is the zero-based agent-loop iteration that made this call.
	Iteration int `json:"iteration"`
	// Model is the resolved deployment used for the successful response.
	Model string `json:"model"`
	// Messages is the exact provider-neutral message history supplied to the model.
	Messages []llm.Message `json:"messages"`
	// Tools is the exact OpenAI-style tool-definition surface supplied to the model.
	Tools []map[string]any `json:"tools"`
	// Response is the model decision returned before any tool executes.
	Response llm.Message `json:"response"`
	// StopReason is the provider's normalized termination signal.
	StopReason string `json:"stop_reason,omitempty"`
	// InputTokens is the provider-reported input-token count for this call.
	InputTokens int `json:"input_tokens,omitempty"`
	// OutputTokens is the provider-reported output-token count for this call.
	OutputTokens int `json:"output_tokens,omitempty"`
	// DurationMS is elapsed wall time for the logical call, including provider recovery.
	DurationMS int64 `json:"duration_ms,omitempty"`

	startedAt time.Time
	discarded bool
}

// UseAttempt replaces the captured input with the exact physical attempt now
// being made by retry or recovery logic. It preserves the original start time
// so DurationMS continues to describe the complete logical call.
func (c *ModelCallContent) UseAttempt(model string, messages []llm.Message, tools []map[string]any) {
	if c == nil {
		return
	}
	c.Model = model
	c.Messages = cloneLLMMessages(messages)
	c.Tools = cloneToolDefinitions(tools)
}

// Discard excludes a logical call whose apparent response was synthesized by
// runtime recovery rather than returned by a model provider.
func (c *ModelCallContent) Discard() {
	if c != nil {
		c.discarded = true
	}
}

// NewModelCallContent takes a detached snapshot of one provider call's inputs.
// The returned value can be completed later with [ModelCallContent.Complete].
func NewModelCallContent(iteration int, model string, messages []llm.Message, tools []map[string]any) ModelCallContent {
	return ModelCallContent{
		Iteration: iteration,
		Model:     model,
		Messages:  cloneLLMMessages(messages),
		Tools:     cloneToolDefinitions(tools),
		startedAt: time.Now(),
	}
}

// Complete records the successful provider response for a captured call.
// A nil response leaves the call incomplete and is ignored.
func (c *ModelCallContent) Complete(response *llm.ChatResponse) {
	if c == nil || response == nil {
		return
	}
	if response.Model != "" {
		c.Model = response.Model
	}
	c.Response = cloneLLMMessage(response.Message)
	c.StopReason = response.StopReason
	c.InputTokens = response.InputTokens
	c.OutputTokens = response.OutputTokens
	if !c.startedAt.IsZero() {
		c.DurationMS = time.Since(c.startedAt).Milliseconds()
	}
}

// ModelCallDetail is the retained, JSON-facing representation of one model
// call. System-message content is resolved through PromptHash when read from
// SQLite, allowing repeated prompts to stay content-addressed on disk.
type ModelCallDetail struct {
	Iteration      int                   `json:"iteration"`
	Model          string                `json:"model"`
	PromptHash     string                `json:"prompt_hash,omitempty"`
	PromptSections []PromptSectionDetail `json:"prompt_sections,omitempty"`
	Messages       []MessageDetail       `json:"messages"`
	Tools          []map[string]any      `json:"tools"`
	Response       MessageDetail         `json:"response"`
	StopReason     string                `json:"stop_reason,omitempty"`
	InputTokens    int                   `json:"input_tokens,omitempty"`
	OutputTokens   int                   `json:"output_tokens,omitempty"`
	DurationMS     int64                 `json:"duration_ms,omitempty"`
}

// PromptSectionDetail locates one semantic system-prompt section inside the
// resolved prompt. The offsets let an evaluator replace only model-family
// contracts while preserving the production context around them.
type PromptSectionDetail struct {
	Name     string `json:"name"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	CacheTTL string `json:"cache_ttl,omitempty"`
}

func retainedModelCalls(calls []ModelCallContent, maxLen int, preserveSystem bool) ([]ModelCallDetail, map[string]string) {
	details := make([]ModelCallDetail, 0, len(calls))
	prompts := make(map[string]string)
	for _, call := range calls {
		if call.discarded || call.Response.Role == "" {
			continue
		}
		messages := retainedMessages(call.Messages, maxLen)
		detail := ModelCallDetail{
			Iteration:    call.Iteration,
			Model:        call.Model,
			Messages:     messages,
			Tools:        cloneToolDefinitions(call.Tools),
			Response:     retainedMessages([]llm.Message{call.Response}, maxLen)[0],
			StopReason:   call.StopReason,
			InputTokens:  call.InputTokens,
			OutputTokens: call.OutputTokens,
			DurationMS:   call.DurationMS,
		}
		for i := range messages {
			if messages[i].Role != "system" {
				continue
			}
			original := call.Messages[i].Content
			detail.PromptHash = hashPrompt(original)
			prompts[detail.PromptHash] = original
			detail.PromptSections = locatePromptSections(original, call.Messages[i].Sections)
			if preserveSystem {
				messages[i].Content = original
				messages[i].ContentTruncated = false
			} else {
				messages[i].Content = ""
				messages[i].ContentTruncated = false
			}
			break
		}
		details = append(details, detail)
	}
	return details, prompts
}

func marshalRetainedModelCalls(calls []ModelCallContent, maxLen int) (string, map[string]string, error) {
	details, prompts := retainedModelCalls(calls, maxLen, false)
	data, err := json.Marshal(details)
	if err != nil {
		return "", nil, err
	}
	return string(data), prompts, nil
}

func resolveModelCallPrompts(ctx context.Context, db *sql.DB, calls []ModelCallDetail) error {
	for i := range calls {
		if calls[i].PromptHash == "" {
			continue
		}
		var content string
		if err := db.QueryRowContext(ctx, `SELECT content FROM log_prompts WHERE hash = ?`, calls[i].PromptHash).Scan(&content); err != nil {
			return fmt.Errorf("resolve model-call prompt %s: %w", calls[i].PromptHash, err)
		}
		for j := range calls[i].Messages {
			if calls[i].Messages[j].Role == "system" {
				calls[i].Messages[j].Content = content
				calls[i].Messages[j].ContentTruncated = false
				break
			}
		}
	}
	return nil
}

func cloneLLMMessages(src []llm.Message) []llm.Message {
	if src == nil {
		return nil
	}
	dst := make([]llm.Message, len(src))
	for i := range src {
		dst[i] = cloneLLMMessage(src[i])
	}
	return dst
}

func cloneLLMMessage(src llm.Message) llm.Message {
	dst := src
	dst.Images = append([]llm.ImageContent(nil), src.Images...)
	dst.Sections = append([]llm.PromptSection(nil), src.Sections...)
	if src.ToolCalls != nil {
		dst.ToolCalls = make([]llm.ToolCall, len(src.ToolCalls))
		for i := range src.ToolCalls {
			dst.ToolCalls[i] = src.ToolCalls[i]
			dst.ToolCalls[i].Function.Arguments = cloneAnyMap(src.ToolCalls[i].Function.Arguments)
		}
	}
	return dst
}

func cloneToolDefinitions(src []map[string]any) []map[string]any {
	if src == nil {
		return nil
	}
	dst := make([]map[string]any, len(src))
	for i := range src {
		dst[i] = cloneAnyMap(src[i])
	}
	return dst
}

func cloneModelCallDetails(src []ModelCallDetail) []ModelCallDetail {
	if src == nil {
		return nil
	}
	dst := make([]ModelCallDetail, len(src))
	for i := range src {
		dst[i] = src[i]
		dst[i].PromptSections = append([]PromptSectionDetail(nil), src[i].PromptSections...)
		dst[i].Messages = cloneMessageDetails(src[i].Messages)
		dst[i].Tools = cloneToolDefinitions(src[i].Tools)
		dst[i].Response = cloneMessageDetails([]MessageDetail{src[i].Response})[0]
	}
	return dst
}

func locatePromptSections(prompt string, sections []llm.PromptSection) []PromptSectionDetail {
	if prompt == "" || len(sections) == 0 {
		return nil
	}
	cursor := 0
	out := make([]PromptSectionDetail, 0, len(sections))
	for _, section := range sections {
		if section.Content == "" {
			continue
		}
		relative := strings.Index(prompt[cursor:], section.Content)
		if relative < 0 {
			return nil
		}
		start := cursor + relative
		end := start + len(section.Content)
		out = append(out, PromptSectionDetail{
			Name:     section.Name,
			Start:    start,
			End:      end,
			CacheTTL: section.CacheTTL,
		})
		cursor = end
	}
	return out
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = cloneAnyValue(value)
	}
	return dst
}

func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = cloneAnyValue(typed[i])
		}
		return out
	case []map[string]any:
		return cloneToolDefinitions(typed)
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
