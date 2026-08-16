package providers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// OpenAICompatModelInfo describes one model from /v1/models. The OpenAI
// schema defines only id/object/owned_by; every other field here is an
// extension some server offers and most omit — LM Studio's native
// inventory fills the state and context fields, vLLM reports
// max_model_len, and a plain OpenAI-compatible server sends none of it.
// Zero means "not reported", never "zero".
type OpenAICompatModelInfo struct {
	ID string `json:"id"`
	// MaxModelLen is vLLM's spelling of the context ceiling.
	// MaxContextLength is LM Studio's. Read them through
	// [OpenAICompatModelInfo.ContextCeiling] rather than picking one.
	MaxModelLen         int    `json:"max_model_len,omitempty"`
	Object              string `json:"object,omitempty"`
	OwnedBy             string `json:"owned_by,omitempty"`
	Type                string `json:"type,omitempty"`
	Publisher           string `json:"publisher,omitempty"`
	Arch                string `json:"arch,omitempty"`
	CompatibilityType   string `json:"compatibility_type,omitempty"`
	Quantization        string `json:"quantization,omitempty"`
	State               string `json:"state,omitempty"`
	MaxContextLength    int    `json:"max_context_length,omitempty"`
	LoadedContextLength int    `json:"loaded_context_length,omitempty"`
	LoadedInstanceID    string `json:"loaded_instance_id,omitempty"`
	Vision              bool   `json:"vision,omitempty"`
	TrainedForToolUse   bool   `json:"trained_for_tool_use,omitempty"`
}

// LMStudioModelInfo is the name this type carried when LM Studio was its
// only source. Retained so LM Studio call sites keep reading naturally.
type LMStudioModelInfo = OpenAICompatModelInfo

// ContextCeiling returns the largest context the server said this model
// supports, reconciling the two spellings different servers use, or 0
// when none reported one.
func (m OpenAICompatModelInfo) ContextCeiling() int {
	if m.MaxContextLength > 0 {
		return m.MaxContextLength
	}
	return m.MaxModelLen
}

type lmStudioLoadRequest struct {
	Model          string `json:"model"`
	ContextLength  int    `json:"context_length,omitempty"`
	EchoLoadConfig bool   `json:"echo_load_config,omitempty"`
}

// lmStudioUnloadRequest releases one loaded instance. LM Studio identifies
// the instance rather than the model, since a model may have several.
type lmStudioUnloadRequest struct {
	InstanceID string `json:"instance_id"`
}

type LMStudioLoadResponse struct {
	Type            string         `json:"type,omitempty"`
	InstanceID      string         `json:"instance_id,omitempty"`
	LoadTimeSeconds float64        `json:"load_time_seconds,omitempty"`
	Status          string         `json:"status,omitempty"`
	LoadConfig      map[string]any `json:"load_config,omitempty"`
}

type openAICompatChatRequest struct {
	Model         string                     `json:"model"`
	Messages      []openAICompatMessage      `json:"messages"`
	Stream        bool                       `json:"stream,omitempty"`
	Tools         []map[string]any           `json:"tools,omitempty"`
	TTL           int                        `json:"ttl,omitempty"`
	StreamOptions *openAICompatStreamOptions `json:"stream_options,omitempty"`
}

type openAICompatStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type openAICompatMessage struct {
	Role       string                    `json:"role"`
	Content    any                       `json:"content,omitempty"`
	ToolCallID string                    `json:"tool_call_id,omitempty"`
	ToolCalls  []openAICompatToolCallReq `json:"tool_calls,omitempty"`
}

type openAICompatContentPart struct {
	Type     string                     `json:"type"`
	Text     string                     `json:"text,omitempty"`
	ImageURL *openAICompatImageURLBlock `json:"image_url,omitempty"`
}

type openAICompatImageURLBlock struct {
	URL string `json:"url"`
}

type openAICompatToolCallReq struct {
	ID       string                        `json:"id,omitempty"`
	Type     string                        `json:"type"`
	Function openAICompatToolFunctionDelta `json:"function"`
}

type openAICompatToolFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAICompatChatResponse struct {
	ID      string                   `json:"id,omitempty"`
	Object  string                   `json:"object,omitempty"`
	Created int64                    `json:"created,omitempty"`
	Model   string                   `json:"model,omitempty"`
	Choices []openAICompatChatChoice `json:"choices"`
	Usage   *openAICompatUsage       `json:"usage,omitempty"`
}

// openAICompatStreamErrorText returns the human-readable failure LM Studio
// encoded in an SSE data frame, or "" when the frame is an ordinary chunk.
//
// A streaming request that LM Studio cannot serve does not fail the way its
// non-streaming sibling does. The non-streaming call answers 4xx with the
// reason in the body; the streaming call answers HTTP 200, opens the stream,
// and delivers the reason as an `event: error` frame. Both shapes of the
// `error` field are accepted: a bare string (as the 4xx body uses) and an
// object carrying `message` (as the stream frame uses).
func openAICompatStreamErrorText(data string) string {
	var probe struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal([]byte(data), &probe); err != nil {
		return ""
	}
	if len(probe.Error) == 0 || string(probe.Error) == "null" {
		return ""
	}

	var text string
	if err := json.Unmarshal(probe.Error, &text); err == nil && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}

	var object struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(probe.Error, &object); err == nil && strings.TrimSpace(object.Message) != "" {
		return strings.TrimSpace(object.Message)
	}
	if strings.TrimSpace(probe.Message) != "" {
		return strings.TrimSpace(probe.Message)
	}
	return "the endpoint reported an unspecified stream error"
}

type openAICompatChatChoice struct {
	Index        int                          `json:"index"`
	Message      *openAICompatMessageResponse `json:"message,omitempty"`
	Delta        *openAICompatChatDelta       `json:"delta,omitempty"`
	FinishReason *string                      `json:"finish_reason,omitempty"`
}

type openAICompatMessageResponse struct {
	Role      string                      `json:"role,omitempty"`
	Content   any                         `json:"content,omitempty"`
	ToolCalls []openAICompatToolCallDelta `json:"tool_calls,omitempty"`
}

type openAICompatChatDelta struct {
	Role      string                      `json:"role,omitempty"`
	Content   string                      `json:"content,omitempty"`
	ToolCalls []openAICompatToolCallDelta `json:"tool_calls,omitempty"`
}

type openAICompatToolCallDelta struct {
	Index    int                           `json:"index,omitempty"`
	ID       string                        `json:"id,omitempty"`
	Type     string                        `json:"type,omitempty"`
	Function openAICompatToolFunctionDelta `json:"function,omitempty"`
}

type openAICompatUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type openAICompatModelsResponse struct {
	Data []LMStudioModelInfo `json:"data"`
}

type openAICompatV1ModelsResponse struct {
	Models []openAICompatV1ModelInfo `json:"models"`
}

type openAICompatV1ModelInfo struct {
	Type             string                           `json:"type,omitempty"`
	Publisher        string                           `json:"publisher,omitempty"`
	Key              string                           `json:"key,omitempty"`
	Architecture     string                           `json:"architecture,omitempty"`
	Quantization     *openAICompatV1Quantization      `json:"quantization,omitempty"`
	ParamsString     string                           `json:"params_string,omitempty"`
	LoadedInstances  []openAICompatV1LoadedInstance   `json:"loaded_instances,omitempty"`
	MaxContextLength int                              `json:"max_context_length,omitempty"`
	Format           string                           `json:"format,omitempty"`
	Capabilities     *openAICompatV1ModelCapabilities `json:"capabilities,omitempty"`
}

type openAICompatV1Quantization struct {
	Name string `json:"name,omitempty"`
}

type openAICompatV1LoadedInstance struct {
	ID     string                   `json:"id,omitempty"`
	Config openAICompatV1LoadConfig `json:"config"`
}

type openAICompatV1LoadConfig struct {
	ContextLength int `json:"context_length,omitempty"`
}

type openAICompatV1ModelCapabilities struct {
	Vision            bool `json:"vision,omitempty"`
	TrainedForToolUse bool `json:"trained_for_tool_use,omitempty"`
}

func (m openAICompatV1ModelInfo) toModelInfo() LMStudioModelInfo {
	loadedContext := 0
	loadedInstanceID := ""
	state := ""
	for _, inst := range m.LoadedInstances {
		if inst.Config.ContextLength > loadedContext {
			loadedContext = inst.Config.ContextLength
			loadedInstanceID = inst.ID
		}
	}
	if len(m.LoadedInstances) > 0 {
		state = "loaded"
	}

	info := LMStudioModelInfo{
		ID:                  m.Key,
		Type:                m.Type,
		Publisher:           m.Publisher,
		Arch:                m.Architecture,
		CompatibilityType:   m.Format,
		MaxContextLength:    m.MaxContextLength,
		LoadedContextLength: loadedContext,
		LoadedInstanceID:    loadedInstanceID,
		State:               state,
	}
	if m.Quantization != nil {
		info.Quantization = m.Quantization.Name
	}
	if m.Capabilities != nil {
		info.Vision = m.Capabilities.Vision
		info.TrainedForToolUse = m.Capabilities.TrainedForToolUse
	}
	return info
}

type openAICompatToolAccumulator struct {
	ID   string
	Name string
	Args strings.Builder
}

func toOpenAICompatMessages(msgs []llm.Message) ([]openAICompatMessage, error) {
	out := make([]openAICompatMessage, 0, len(msgs))
	for _, m := range msgs {
		wire := openAICompatMessage{
			Role:       m.Role,
			ToolCallID: m.ToolCallID,
		}
		switch {
		case len(m.Images) > 0:
			parts := make([]openAICompatContentPart, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, openAICompatContentPart{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, openAICompatContentPart{
					Type: "image_url",
					ImageURL: &openAICompatImageURLBlock{
						URL: "data:" + img.MediaType + ";base64," + img.Data,
					},
				})
			}
			wire.Content = parts
		default:
			// Always emit content as a string, even when empty. Leaving it
			// unset makes `content,omitempty` drop the field, and strict
			// OpenAI-compatible servers such as LM Studio then reject the
			// message with "content field must be a string or an array of
			// objects" — which bites assistant messages that carry only
			// tool_calls and tool results with empty output. The ollama
			// adapter already always sets content; this keeps parity.
			wire.Content = m.Content
		}
		if len(m.ToolCalls) > 0 {
			wire.ToolCalls = make([]openAICompatToolCallReq, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				argsJSON, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					return nil, fmt.Errorf("marshal tool call arguments for %q: %w", tc.Function.Name, err)
				}
				wire.ToolCalls = append(wire.ToolCalls, openAICompatToolCallReq{
					ID:   tc.ID,
					Type: "function",
					Function: openAICompatToolFunctionDelta{
						Name:      tc.Function.Name,
						Arguments: string(argsJSON),
					},
				})
			}
		}
		out = append(out, wire)
	}
	return out, nil
}

func normalizeOpenAICompatMessageRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "assistant"
	}
	return role
}

func decodeOpenAICompatToolCalls(accs map[int]*openAICompatToolAccumulator) ([]llm.ToolCall, error) {
	if len(accs) == 0 {
		return nil, nil
	}
	indexes := make([]int, 0, len(accs))
	for idx := range accs {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)

	out := make([]llm.ToolCall, 0, len(indexes))
	for _, idx := range indexes {
		acc := accs[idx]
		if acc == nil || acc.Name == "" {
			continue
		}
		args, err := parseOpenAICompatToolArguments(acc.Name, acc.Args.String())
		if err != nil {
			return nil, err
		}
		callID, err := ensureOpenAICompatToolCallID(acc.ID)
		if err != nil {
			return nil, err
		}
		call := llm.ToolCall{ID: callID}
		call.Function.Name = acc.Name
		call.Function.Arguments = args
		out = append(out, call)
	}
	return out, nil
}

// ensureOpenAICompatToolCallID repairs a runner omission before the assistant
// message enters iteration history. LM Studio rejects that same historical
// tool call on the next request when its id is empty, even though its Qwen
// parser can produce tool calls without assigning one.
func ensureOpenAICompatToolCallID(id string) (string, error) {
	if strings.TrimSpace(id) != "" {
		return id, nil
	}
	generated, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate fallback LM Studio tool call ID: %w", err)
	}
	return "call_" + generated.String(), nil
}

func decodeOpenAICompatToolCallsFromSlice(in []openAICompatToolCallDelta) ([]llm.ToolCall, error) {
	if len(in) == 0 {
		return nil, nil
	}
	accs := make(map[int]*openAICompatToolAccumulator, len(in))
	for i, tc := range in {
		idx := tc.Index
		if idx == 0 && tc.ID == "" && tc.Function.Name == "" && tc.Function.Arguments == "" && len(in) == 1 {
			idx = i
		}
		acc := accs[idx]
		if acc == nil {
			acc = &openAICompatToolAccumulator{}
			accs[idx] = acc
		}
		acc.ID = tc.ID
		acc.Name = tc.Function.Name
		acc.Args.WriteString(tc.Function.Arguments)
	}
	return decodeOpenAICompatToolCalls(accs)
}

func parseOpenAICompatToolArguments(name, raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("decode tool arguments for %q: %w", name, err)
	}
	return args, nil
}

func openAICompatContentText(v any) string {
	switch content := v.(type) {
	case nil:
		return ""
	case string:
		return content
	case []any:
		var b strings.Builder
		for _, part := range content {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := partMap["type"].(string); kind == "text" {
				if text, _ := partMap["text"].(string); text != "" {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func applyTextToolFallback(resp *llm.ChatResponse, validToolNames []string) error {
	if resp == nil {
		return nil
	}
	llm.ApplyTextToolCallFallback(resp, validToolNames, llm.DefaultToolCallTextProfile())
	for i := range resp.Message.ToolCalls {
		id, err := ensureOpenAICompatToolCallID(resp.Message.ToolCalls[i].ID)
		if err != nil {
			return err
		}
		resp.Message.ToolCalls[i].ID = id
	}
	return nil
}
