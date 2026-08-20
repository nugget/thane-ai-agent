package modeleval

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

// SnapshotSchemaVersion is the current on-disk production snapshot contract.
const SnapshotSchemaVersion = 1

var (
	// ErrTruncatedInput means content retention clipped part of a model input.
	ErrTruncatedInput = errors.New("model input was truncated during retention")
	// ErrImageInput means a call depended on image bytes that retention omits.
	ErrImageInput = errors.New("model input contains omitted image data")
)

// Snapshot is a local-only collection of production-derived model decisions.
// It intentionally carries an explicit sensitivity marker because automatic
// redaction cannot make arbitrary prompts or tool results safe to publish.
type Snapshot struct {
	SchemaVersion          int    `json:"schema_version"`
	CreatedAt              string `json:"created_at"`
	ContainsProductionData bool   `json:"contains_production_data"`
	Notice                 string `json:"notice"`
	Cases                  []Case `json:"cases"`
}

// Case captures the complete input and reference output for one logical model
// call. Replaying a case never executes the recorded tools.
type Case struct {
	ID                  string           `json:"id"`
	SourceRequestID     string           `json:"source_request_id"`
	Iteration           int              `json:"iteration"`
	ReferenceModel      string           `json:"reference_model"`
	Messages            []llm.Message    `json:"messages"`
	Tools               []map[string]any `json:"tools"`
	PromptSections      []PromptSection  `json:"prompt_sections,omitempty"`
	Expected            llm.Message      `json:"expected"`
	ReferenceStopReason string           `json:"reference_stop_reason,omitempty"`
	ReferenceDurationMS int64            `json:"reference_duration_ms,omitempty"`
}

// PromptSection identifies one semantic range inside the system prompt.
type PromptSection struct {
	Name     string `json:"name"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	CacheTTL string `json:"cache_ttl,omitempty"`
}

// CaseFromModelCall converts retained logging detail into a replayable case.
// It refuses truncated or image-bearing inputs because replaying either would
// silently evaluate a different request from the one production sent.
func CaseFromModelCall(requestID string, call logging.ModelCallDetail) (Case, error) {
	messages, err := messagesFromDetails(call.Messages)
	if err != nil {
		return Case{}, fmt.Errorf("input messages: %w", err)
	}
	expected, err := messageFromDetail(call.Response)
	if err != nil {
		return Case{}, fmt.Errorf("reference response: %w", err)
	}
	sections := make([]PromptSection, len(call.PromptSections))
	for i, section := range call.PromptSections {
		sections[i] = PromptSection{
			Name:     section.Name,
			Start:    section.Start,
			End:      section.End,
			CacheTTL: section.CacheTTL,
		}
	}
	return Case{
		ID:                  fmt.Sprintf("%s/%d", requestID, call.Iteration),
		SourceRequestID:     requestID,
		Iteration:           call.Iteration,
		ReferenceModel:      call.Model,
		Messages:            messages,
		Tools:               cloneToolDefinitions(call.Tools),
		PromptSections:      sections,
		Expected:            expected,
		ReferenceStopReason: call.StopReason,
		ReferenceDurationMS: call.DurationMS,
	}, nil
}

// Validate checks the snapshot's version and each case's required replay data.
func (s Snapshot) Validate() error {
	if s.SchemaVersion != SnapshotSchemaVersion {
		return fmt.Errorf("snapshot schema_version = %d, want %d", s.SchemaVersion, SnapshotSchemaVersion)
	}
	if len(s.Cases) == 0 {
		return fmt.Errorf("snapshot contains no cases")
	}
	seen := make(map[string]struct{}, len(s.Cases))
	for i, c := range s.Cases {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("case %d has no id", i)
		}
		if _, duplicate := seen[c.ID]; duplicate {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		if len(c.Messages) == 0 {
			return fmt.Errorf("case %q has no messages", c.ID)
		}
	}
	return nil
}

func messagesFromDetails(details []logging.MessageDetail) ([]llm.Message, error) {
	messages := make([]llm.Message, len(details))
	for i, detail := range details {
		message, err := messageFromDetail(detail)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		messages[i] = message
	}
	return messages, nil
}

func messageFromDetail(detail logging.MessageDetail) (llm.Message, error) {
	if detail.ContentTruncated {
		return llm.Message{}, ErrTruncatedInput
	}
	if len(detail.Images) > 0 {
		return llm.Message{}, ErrImageInput
	}
	message := llm.Message{
		Role:       detail.Role,
		Content:    detail.Content,
		ToolCallID: detail.ToolCallID,
	}
	if len(detail.ToolCalls) > 0 {
		message.ToolCalls = make([]llm.ToolCall, len(detail.ToolCalls))
		for i, retained := range detail.ToolCalls {
			if retained.ArgumentsTruncated {
				return llm.Message{}, ErrTruncatedInput
			}
			arguments, err := decodeArguments(retained.Arguments)
			if err != nil {
				return llm.Message{}, fmt.Errorf("tool call %q arguments: %w", retained.Name, err)
			}
			message.ToolCalls[i].ID = retained.ID
			message.ToolCalls[i].Function.Name = retained.Name
			message.ToolCalls[i].Function.Arguments = arguments
		}
	}
	return message, nil
}

func decodeArguments(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
		return nil, err
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return arguments, nil
}

func cloneToolDefinitions(src []map[string]any) []map[string]any {
	if src == nil {
		return nil
	}
	dst := make([]map[string]any, len(src))
	for i := range src {
		dst[i] = cloneJSONMap(src[i])
	}
	return dst
}

func cloneJSONMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = cloneJSONValue(value)
	}
	return dst
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = cloneJSONValue(typed[i])
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
