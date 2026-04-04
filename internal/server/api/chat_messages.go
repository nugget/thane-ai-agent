package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/llm"
)

const maxDecodedImageBytes = 20 << 20

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
	return decodeBase64ImageContent(data, mediaType)
}

func parseOllamaImages(raw []string) ([]llm.ImageContent, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]llm.ImageContent, 0, len(raw))
	for i, data := range raw {
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		img, err := decodeBase64ImageContent(data, "")
		if err != nil {
			return nil, fmt.Errorf("images[%d]: %w", i, err)
		}
		out = append(out, img)
	}
	return out, nil
}

func decodeBase64ImageContent(data, declaredMediaType string) (llm.ImageContent, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return llm.ImageContent{}, fmt.Errorf("image data is empty")
	}

	if decodedLen := base64.StdEncoding.DecodedLen(len(data)); decodedLen > maxDecodedImageBytes {
		return llm.ImageContent{}, fmt.Errorf("image data exceeds %d MiB limit", maxDecodedImageBytes>>20)
	}

	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return llm.ImageContent{}, fmt.Errorf("invalid base64 image data: %w", err)
	}

	mediaType, err := validateDecodedImage(decoded, declaredMediaType)
	if err != nil {
		return llm.ImageContent{}, err
	}

	return llm.ImageContent{
		Data:      data,
		MediaType: mediaType,
	}, nil
}

func validateDecodedImage(decoded []byte, declaredMediaType string) (string, error) {
	if len(decoded) == 0 {
		return "", fmt.Errorf("image data is empty")
	}

	sniffed := detectImageMediaType(decoded)
	if !strings.HasPrefix(sniffed, "image/") {
		return "", fmt.Errorf("decoded bytes are not an image (detected %q)", sniffed)
	}

	declared := normalizeImageMediaType(declaredMediaType)
	actual := normalizeImageMediaType(sniffed)
	if actual == "" {
		actual = sniffed
	}

	if declared != "" && actual != "" && declared != actual {
		return "", fmt.Errorf("declared media type %q does not match decoded image data %q", declared, actual)
	}
	if !supportedImageMediaType(actual) {
		return "", fmt.Errorf("unsupported image media type %q; supported types are image/png, image/jpeg, image/gif, image/webp", actual)
	}

	switch actual {
	case "image/png", "image/jpeg", "image/gif":
		if _, _, err := image.DecodeConfig(bytes.NewReader(decoded)); err != nil {
			return "", fmt.Errorf("invalid %s data: %w", actual, err)
		}
	case "image/webp":
		if !looksLikeWEBP(decoded) {
			return "", fmt.Errorf("invalid image/webp data")
		}
	}

	if actual != "" {
		return actual, nil
	}
	return declared, nil
}

func detectImageMediaType(decoded []byte) string {
	if looksLikeWEBP(decoded) {
		return "image/webp"
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

func normalizeImageMediaType(mediaType string) string {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		return ""
	}
	if semi := strings.Index(mediaType, ";"); semi >= 0 {
		mediaType = strings.TrimSpace(mediaType[:semi])
	}
	switch mediaType {
	case "image/jpg":
		return "image/jpeg"
	default:
		return mediaType
	}
}

func looksLikeWEBP(decoded []byte) bool {
	return len(decoded) >= 12 &&
		string(decoded[:4]) == "RIFF" &&
		string(decoded[8:12]) == "WEBP"
}

func supportedImageMediaType(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
