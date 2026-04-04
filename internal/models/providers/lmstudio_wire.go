package providers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

// LMStudioModelInfo describes one model from /v1/models.
type LMStudioModelInfo struct {
	ID                  string `json:"id"`
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
	Vision              bool   `json:"vision,omitempty"`
	TrainedForToolUse   bool   `json:"trained_for_tool_use,omitempty"`
}

type lmStudioLoadRequest struct {
	Model          string `json:"model"`
	ContextLength  int    `json:"context_length,omitempty"`
	EchoLoadConfig bool   `json:"echo_load_config,omitempty"`
}

type LMStudioLoadResponse struct {
	Type            string         `json:"type,omitempty"`
	InstanceID      string         `json:"instance_id,omitempty"`
	LoadTimeSeconds float64        `json:"load_time_seconds,omitempty"`
	Status          string         `json:"status,omitempty"`
	LoadConfig      map[string]any `json:"load_config,omitempty"`
}

type lmStudioChatRequest struct {
	Model         string                 `json:"model"`
	Messages      []lmStudioMessage      `json:"messages"`
	Stream        bool                   `json:"stream,omitempty"`
	Tools         []map[string]any       `json:"tools,omitempty"`
	StreamOptions *lmStudioStreamOptions `json:"stream_options,omitempty"`
}

type lmStudioStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type lmStudioMessage struct {
	Role       string                `json:"role"`
	Content    any                   `json:"content,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	ToolCalls  []lmStudioToolCallReq `json:"tool_calls,omitempty"`
}

type lmStudioContentPart struct {
	Type     string                 `json:"type"`
	Text     string                 `json:"text,omitempty"`
	ImageURL *lmStudioImageURLBlock `json:"image_url,omitempty"`
}

type lmStudioImageURLBlock struct {
	URL string `json:"url"`
}

type lmStudioToolCallReq struct {
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type"`
	Function lmStudioToolFunctionDelta `json:"function"`
}

type lmStudioToolFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type lmStudioChatResponse struct {
	ID      string               `json:"id,omitempty"`
	Object  string               `json:"object,omitempty"`
	Created int64                `json:"created,omitempty"`
	Model   string               `json:"model,omitempty"`
	Choices []lmStudioChatChoice `json:"choices"`
	Usage   *lmStudioUsage       `json:"usage,omitempty"`
}

type lmStudioChatChoice struct {
	Index        int                      `json:"index"`
	Message      *lmStudioMessageResponse `json:"message,omitempty"`
	Delta        *lmStudioChatDelta       `json:"delta,omitempty"`
	FinishReason *string                  `json:"finish_reason,omitempty"`
}

type lmStudioMessageResponse struct {
	Role      string                  `json:"role,omitempty"`
	Content   any                     `json:"content,omitempty"`
	ToolCalls []lmStudioToolCallDelta `json:"tool_calls,omitempty"`
}

type lmStudioChatDelta struct {
	Role      string                  `json:"role,omitempty"`
	Content   string                  `json:"content,omitempty"`
	ToolCalls []lmStudioToolCallDelta `json:"tool_calls,omitempty"`
}

type lmStudioToolCallDelta struct {
	Index    int                       `json:"index,omitempty"`
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type,omitempty"`
	Function lmStudioToolFunctionDelta `json:"function,omitempty"`
}

type lmStudioUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type lmStudioModelsResponse struct {
	Data []LMStudioModelInfo `json:"data"`
}

type lmStudioV1ModelsResponse struct {
	Models []lmStudioV1ModelInfo `json:"models"`
}

type lmStudioV1ModelInfo struct {
	Type             string                       `json:"type,omitempty"`
	Publisher        string                       `json:"publisher,omitempty"`
	Key              string                       `json:"key,omitempty"`
	Architecture     string                       `json:"architecture,omitempty"`
	Quantization     *lmStudioV1Quantization      `json:"quantization,omitempty"`
	ParamsString     string                       `json:"params_string,omitempty"`
	LoadedInstances  []lmStudioV1LoadedInstance   `json:"loaded_instances,omitempty"`
	MaxContextLength int                          `json:"max_context_length,omitempty"`
	Format           string                       `json:"format,omitempty"`
	Capabilities     *lmStudioV1ModelCapabilities `json:"capabilities,omitempty"`
}

type lmStudioV1Quantization struct {
	Name string `json:"name,omitempty"`
}

type lmStudioV1LoadedInstance struct {
	ID     string               `json:"id,omitempty"`
	Config lmStudioV1LoadConfig `json:"config"`
}

type lmStudioV1LoadConfig struct {
	ContextLength int `json:"context_length,omitempty"`
}

type lmStudioV1ModelCapabilities struct {
	Vision            bool `json:"vision,omitempty"`
	TrainedForToolUse bool `json:"trained_for_tool_use,omitempty"`
}

func (m lmStudioV1ModelInfo) toModelInfo() LMStudioModelInfo {
	loadedContext := 0
	state := ""
	for _, inst := range m.LoadedInstances {
		if inst.Config.ContextLength > loadedContext {
			loadedContext = inst.Config.ContextLength
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

type lmStudioToolAccumulator struct {
	ID   string
	Name string
	Args strings.Builder
}

func toLMStudioMessages(msgs []llm.Message) ([]lmStudioMessage, error) {
	out := make([]lmStudioMessage, 0, len(msgs))
	for _, m := range msgs {
		wire := lmStudioMessage{
			Role:       m.Role,
			ToolCallID: m.ToolCallID,
		}
		switch {
		case len(m.Images) > 0:
			parts := make([]lmStudioContentPart, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, lmStudioContentPart{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, lmStudioContentPart{
					Type: "image_url",
					ImageURL: &lmStudioImageURLBlock{
						URL: "data:" + img.MediaType + ";base64," + img.Data,
					},
				})
			}
			wire.Content = parts
		case m.Content != "":
			wire.Content = m.Content
		}
		if len(m.ToolCalls) > 0 {
			wire.ToolCalls = make([]lmStudioToolCallReq, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				argsJSON, err := json.Marshal(tc.Function.Arguments)
				if err != nil {
					return nil, fmt.Errorf("marshal tool call arguments for %q: %w", tc.Function.Name, err)
				}
				wire.ToolCalls = append(wire.ToolCalls, lmStudioToolCallReq{
					ID:   tc.ID,
					Type: "function",
					Function: lmStudioToolFunctionDelta{
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

func decodeLMStudioToolCalls(accs map[int]*lmStudioToolAccumulator) ([]llm.ToolCall, error) {
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
		args, err := parseLMStudioToolArguments(acc.Name, acc.Args.String())
		if err != nil {
			return nil, err
		}
		call := llm.ToolCall{ID: acc.ID}
		call.Function.Name = acc.Name
		call.Function.Arguments = args
		out = append(out, call)
	}
	return out, nil
}

func decodeLMStudioToolCallsFromSlice(in []lmStudioToolCallDelta) ([]llm.ToolCall, error) {
	if len(in) == 0 {
		return nil, nil
	}
	accs := make(map[int]*lmStudioToolAccumulator, len(in))
	for i, tc := range in {
		idx := tc.Index
		if idx == 0 && tc.ID == "" && tc.Function.Name == "" && tc.Function.Arguments == "" && len(in) == 1 {
			idx = i
		}
		acc := accs[idx]
		if acc == nil {
			acc = &lmStudioToolAccumulator{}
			accs[idx] = acc
		}
		acc.ID = tc.ID
		acc.Name = tc.Function.Name
		acc.Args.WriteString(tc.Function.Arguments)
	}
	return decodeLMStudioToolCalls(accs)
}

func parseLMStudioToolArguments(name, raw string) (map[string]any, error) {
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

func lmStudioContentText(v any) string {
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

func applyTextToolFallback(resp *llm.ChatResponse, validToolNames []string) {
	if resp == nil || len(resp.Message.ToolCalls) > 0 || resp.Message.Content == "" {
		return
	}
	if parsed := parseTextToolCalls(resp.Message.Content, validToolNames); len(parsed) > 0 {
		resp.Message.ToolCalls = parsed
		resp.Message.Content = ""
		return
	}
	if looksLikeHallucinatedToolCall(resp.Message.Content) {
		resp.Message.Content = ""
		return
	}
	resp.Message.Content = stripTrailingToolCallJSON(resp.Message.Content, validToolNames)
}
