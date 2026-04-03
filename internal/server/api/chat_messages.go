package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/llm"
)

type chatCompletionRequestMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatCompletionContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL json.RawMessage `json:"image_url,omitempty"`
}

type chatCompletionImageURL struct {
	URL string `json:"url"`
}

func (r ChatCompletionRequest) AgentMessages() ([]agent.Message, error) {
	out := make([]agent.Message, 0, len(r.Messages))
	for i, msg := range r.Messages {
		converted, err := msg.AgentMessage()
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		out = append(out, converted)
	}
	return out, nil
}

func (m chatCompletionRequestMessage) AgentMessage() (agent.Message, error) {
	out := agent.Message{Role: m.Role}
	raw := bytes.TrimSpace(m.Content)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return out, nil
	}

	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &out.Content); err != nil {
			return agent.Message{}, fmt.Errorf("decode string content: %w", err)
		}
		return out, nil
	}

	if raw[0] != '[' {
		return agent.Message{}, fmt.Errorf("content must be a string or an array of content parts")
	}

	var parts []chatCompletionContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return agent.Message{}, fmt.Errorf("decode content parts: %w", err)
	}

	var texts []string
	for i, part := range parts {
		switch strings.TrimSpace(part.Type) {
		case "text":
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		case "image_url":
			img, err := parseChatCompletionImageURL(part.ImageURL)
			if err != nil {
				return agent.Message{}, fmt.Errorf("content[%d]: %w", i, err)
			}
			out.Images = append(out.Images, img)
		default:
			return agent.Message{}, fmt.Errorf("unsupported content part type %q", part.Type)
		}
	}
	out.Content = strings.Join(texts, "\n")
	return out, nil
}

func parseChatCompletionImageURL(raw json.RawMessage) (llm.ImageContent, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return llm.ImageContent{}, fmt.Errorf("image_url is required")
	}

	var url string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &url); err != nil {
			return llm.ImageContent{}, fmt.Errorf("decode image_url string: %w", err)
		}
	} else {
		var block chatCompletionImageURL
		if err := json.Unmarshal(raw, &block); err != nil {
			return llm.ImageContent{}, fmt.Errorf("decode image_url object: %w", err)
		}
		url = block.URL
	}
	return parseImageDataURL(url)
}

func parseImageDataURL(raw string) (llm.ImageContent, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return llm.ImageContent{}, fmt.Errorf("image_url.url is required")
	}
	if !strings.HasPrefix(raw, "data:") {
		return llm.ImageContent{}, fmt.Errorf("only data:image/...;base64 URLs are supported today")
	}

	meta, data, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
	if !ok {
		return llm.ImageContent{}, fmt.Errorf("invalid data URL")
	}
	if !strings.Contains(meta, ";base64") {
		return llm.ImageContent{}, fmt.Errorf("image data URL must be base64-encoded")
	}

	mediaType := strings.TrimSpace(strings.TrimSuffix(meta, ";base64"))
	if !strings.HasPrefix(mediaType, "image/") {
		return llm.ImageContent{}, fmt.Errorf("image data URL must declare an image media type")
	}

	data = strings.TrimSpace(data)
	if data == "" {
		return llm.ImageContent{}, fmt.Errorf("image data URL is empty")
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return llm.ImageContent{}, fmt.Errorf("invalid base64 image data: %w", err)
	}

	return llm.ImageContent{
		Data:      data,
		MediaType: mediaType,
	}, nil
}

func parseOllamaImages(raw []string) []llm.ImageContent {
	if len(raw) == 0 {
		return nil
	}
	out := make([]llm.ImageContent, 0, len(raw))
	for _, data := range raw {
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		out = append(out, llm.ImageContent{
			Data:      data,
			MediaType: detectBase64ImageMediaType(data),
		})
	}
	return out
}

func detectBase64ImageMediaType(data string) string {
	data = strings.TrimSpace(data)
	if data == "" {
		return "application/octet-stream"
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) == 0 {
		return "application/octet-stream"
	}
	sniffLen := len(decoded)
	if sniffLen > 512 {
		sniffLen = 512
	}
	mediaType := http.DetectContentType(decoded[:sniffLen])
	if mediaType == "" {
		return "application/octet-stream"
	}
	return mediaType
}
