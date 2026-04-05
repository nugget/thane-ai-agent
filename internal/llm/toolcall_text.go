package llm

import (
	"encoding/json"
	"strings"
)

// ToolCallTextProfile captures the raw-text tool-call formats the
// runtime is willing to parse for a model family.
type ToolCallTextProfile struct {
	AcceptTaggedToolCalls    bool
	AcceptMarkdownFences     bool
	AcceptConcatenatedJSON   bool
	AcceptToolNameJSONArgs   bool
	SuppressHallucinatedText bool
}

// DefaultToolCallTextProfile accepts the common raw-text tool-call
// formats emitted by local/open models behind OpenAI-compatible
// runtimes.
func DefaultToolCallTextProfile() ToolCallTextProfile {
	return ToolCallTextProfile{
		AcceptTaggedToolCalls:    true,
		AcceptMarkdownFences:     true,
		AcceptConcatenatedJSON:   true,
		AcceptToolNameJSONArgs:   true,
		SuppressHallucinatedText: true,
	}
}

// ExtractToolNames extracts tool names from the OpenAI-style tool
// definitions passed to providers.
func ExtractToolNames(tools []map[string]any) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// LooksLikeTextToolCall reports whether content appears to be a
// raw-text tool call and should be buffered until the full response is
// available.
func LooksLikeTextToolCall(content string, profile ToolCallTextProfile) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	if trimmed[0] == '{' {
		return true
	}
	if profile.AcceptTaggedToolCalls && (strings.HasPrefix(trimmed, "<tool_call>") || strings.HasPrefix(trimmed, "<tool")) {
		return true
	}
	if profile.AcceptMarkdownFences && strings.HasPrefix(trimmed, "```") {
		if _, ok := extractFencedToolPayload(trimmed); ok {
			return true
		}
	}
	return false
}

// ApplyTextToolCallFallback upgrades raw-text tool call emissions into
// structured ToolCalls, suppresses obvious hallucinated tool-call
// shapes, and strips trailing tool-call payloads from mixed responses.
func ApplyTextToolCallFallback(resp *ChatResponse, validToolNames []string, profile ToolCallTextProfile) {
	if resp == nil || len(resp.Message.ToolCalls) > 0 || resp.Message.Content == "" {
		return
	}
	if parsed := ParseTextToolCalls(resp.Message.Content, validToolNames, profile); len(parsed) > 0 {
		resp.Message.ToolCalls = parsed
		resp.Message.Content = ""
		return
	}
	if parsed := ParseTextToolCallsForRepair(resp.Message.Content, profile); len(parsed) > 0 {
		resp.Message.ToolCalls = parsed
		resp.Message.Content = ""
		return
	}
	if profile.SuppressHallucinatedText && LooksLikeHallucinatedToolCall(resp.Message.Content, profile) {
		resp.Message.Content = ""
		return
	}
	resp.Message.Content = StripTrailingToolCallText(resp.Message.Content, validToolNames, profile)
}

// LooksLikeHallucinatedToolCall reports whether content has the shape
// of a tool call but does not match any valid tool.
func LooksLikeHallucinatedToolCall(content string, profile ToolCallTextProfile) bool {
	trimmed := normalizeToolCallPayload(content, profile)
	if trimmed == "" {
		return false
	}
	if trimmed == "" || trimmed[0] != '{' {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return false
	}
	_, hasName := obj["name"]
	_, hasArgs := obj["arguments"]
	return hasName && hasArgs
}

// StripTrailingToolCallText removes trailing tool-call payloads that a
// model appended after prose.
func StripTrailingToolCallText(content string, validTools []string, profile ToolCallTextProfile) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return content
	}
	if profile.AcceptMarkdownFences {
		if cleaned, ok := stripTrailingFencedToolPayload(trimmed, validTools, profile); ok {
			return cleaned
		}
	}
	lastBrace := strings.LastIndex(trimmed, "{")
	if lastBrace <= 0 {
		return content
	}
	jsonPart := strings.TrimSpace(trimmed[lastBrace:])
	var obj struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(jsonPart), &obj); err != nil || obj.Name == "" {
		return content
	}
	cleaned := strings.TrimSpace(trimmed[:lastBrace])
	if cleaned == "" {
		return content
	}
	return cleaned
}

// ParseTextToolCalls attempts to extract structured tool calls from
// raw assistant text.
func ParseTextToolCalls(content string, validTools []string, profile ToolCallTextProfile) []ToolCall {
	content = normalizeToolCallPayload(content, profile)
	if content == "" {
		return nil
	}
	return parseNormalizedTextToolCalls(content, validTools, profile, profile.AcceptToolNameJSONArgs)
}

// ParseTextToolCallsForRepair extracts tool-shaped JSON payloads even
// when the tool names do not currently match the valid tool list. This
// lets later runtime layers repair aliases such as forge_capability or
// list_capabilities instead of dropping them as hallucinated text.
func ParseTextToolCallsForRepair(content string, profile ToolCallTextProfile) []ToolCall {
	content = normalizeToolCallPayload(content, profile)
	if content == "" {
		return nil
	}
	return parseNormalizedTextToolCalls(content, nil, profile, false)
}

func normalizeToolCallPayload(content string, profile ToolCallTextProfile) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if profile.AcceptMarkdownFences {
		if payload, ok := extractFencedToolPayload(content); ok {
			content = payload
		}
	}
	if profile.AcceptTaggedToolCalls {
		content = extractTaggedToolPayload(content)
	}
	return strings.TrimSpace(content)
}

func parseNormalizedTextToolCalls(content string, validTools []string, profile ToolCallTextProfile, allowToolNameJSONArgs bool) []ToolCall {
	if calls := parseToolCallJSONArray(content, validTools); len(calls) > 0 {
		return calls
	}
	if calls := parseSingleToolCallJSON(content, validTools); len(calls) > 0 {
		return calls
	}
	if profile.AcceptConcatenatedJSON {
		if calls := parseConcatenatedToolCallJSON(content, validTools); len(calls) > 0 {
			return calls
		}
	}
	if allowToolNameJSONArgs {
		if calls := parseToolNameJSONArgs(content, validTools); len(calls) > 0 {
			return calls
		}
	}
	return nil
}

func extractTaggedToolPayload(content string) string {
	if !strings.Contains(content, "<tool_call>") {
		return content
	}
	start := strings.Index(content, "<tool_call>")
	end := strings.Index(content, "</tool_call>")
	switch {
	case start != -1 && end > start:
		return strings.TrimSpace(content[start+len("<tool_call>") : end])
	case start != -1:
		return strings.TrimSpace(content[start+len("<tool_call>"):])
	default:
		return content
	}
}

func extractFencedToolPayload(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return "", false
	}
	newlineIdx := strings.Index(trimmed, "\n")
	if newlineIdx < 0 {
		return "", false
	}
	info := strings.TrimSpace(strings.TrimPrefix(trimmed[:newlineIdx], "```"))
	if info != "" && info != "tool" && info != "json" {
		return "", false
	}
	body := trimmed[newlineIdx+1:]
	endIdx := strings.LastIndex(body, "```")
	if endIdx < 0 {
		return "", false
	}
	payload := strings.TrimSpace(body[:endIdx])
	if payload == "" {
		return "", false
	}
	return payload, true
}

func stripTrailingFencedToolPayload(content string, validTools []string, profile ToolCallTextProfile) (string, bool) {
	endIdx := strings.LastIndex(content, "```")
	if endIdx < 0 || endIdx != len(content)-3 {
		return "", false
	}
	startIdx := strings.LastIndex(content[:endIdx], "```")
	if startIdx <= 0 {
		return "", false
	}
	payload, ok := extractFencedToolPayload(content[startIdx:])
	if !ok || len(ParseTextToolCalls(payload, validTools, profile)) == 0 {
		return "", false
	}
	cleaned := strings.TrimSpace(content[:startIdx])
	if cleaned == "" {
		return "", false
	}
	return cleaned, true
}

func parseToolCallJSONArray(content string, validTools []string) []ToolCall {
	var calls []struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(content), &calls); err != nil || len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, c := range calls {
		if c.Name == "" || !isValidToolName(c.Name, validTools) {
			continue
		}
		out = append(out, toolCallFromParts(c.Name, c.Arguments))
	}
	return out
}

func parseSingleToolCallJSON(content string, validTools []string) []ToolCall {
	var single struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(content), &single); err != nil || single.Name == "" {
		return nil
	}
	if !isValidToolName(single.Name, validTools) {
		return nil
	}
	return []ToolCall{toolCallFromParts(single.Name, single.Arguments)}
}

func parseConcatenatedToolCallJSON(content string, validTools []string) []ToolCall {
	if strings.Count(content, `"name"`) <= 1 || !strings.Contains(content, "}{") {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(content))
	var out []ToolCall
	for dec.More() {
		var tc struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := dec.Decode(&tc); err != nil {
			break
		}
		if tc.Name == "" || !isValidToolName(tc.Name, validTools) {
			continue
		}
		out = append(out, toolCallFromParts(tc.Name, tc.Arguments))
	}
	return out
}

func parseToolNameJSONArgs(content string, validTools []string) []ToolCall {
	if len(validTools) == 0 {
		return nil
	}
	for _, toolName := range validTools {
		prefix := toolName + " "
		if !strings.HasPrefix(content, prefix) {
			continue
		}
		argsJSON := strings.TrimSpace(strings.TrimPrefix(content, prefix))
		if !strings.HasPrefix(argsJSON, "{") {
			continue
		}
		depth := 0
		endIdx := -1
		for i, c := range argsJSON {
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth == 0 {
					endIdx = i + 1
					break
				}
			}
		}
		if endIdx > 0 {
			argsJSON = argsJSON[:endIdx]
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(argsJSON), &args); err == nil {
			return []ToolCall{toolCallFromParts(toolName, args)}
		}
	}
	return nil
}

func isValidToolName(name string, validTools []string) bool {
	if len(validTools) == 0 {
		return true
	}
	for _, v := range validTools {
		if v == name {
			return true
		}
	}
	return false
}

func toolCallFromParts(name string, args map[string]any) ToolCall {
	if args == nil {
		args = map[string]any{}
	}
	return ToolCall{
		Function: struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}{
			Name:      name,
			Arguments: args,
		},
	}
}
