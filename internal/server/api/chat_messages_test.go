package api

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func tinyPNGDataURL(t *testing.T) string {
	t.Helper()
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func TestChatCompletionRequest_AgentMessages_StringContent(t *testing.T) {
	req := ChatCompletionRequest{
		Model: "thane:latest",
		Messages: []chatCompletionRequestMessage{
			{Role: "user", Content: json.RawMessage(`"hello"`)}},
	}

	msgs, err := req.AgentMessages()
	if err != nil {
		t.Fatalf("AgentMessages() error = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "hello" {
		t.Fatalf("Content = %q, want %q", msgs[0].Content, "hello")
	}
	if len(msgs[0].Images) != 0 {
		t.Fatalf("Images = %d, want 0", len(msgs[0].Images))
	}
}

func TestChatCompletionRequest_AgentMessages_MultimodalParts(t *testing.T) {
	req := ChatCompletionRequest{
		Model: "thane:latest",
		Messages: []chatCompletionRequestMessage{
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"text","text":"what is in this image?"},
					{"type":"image_url","image_url":{"url":"` + tinyPNGDataURL(t) + `"}}
				]`),
			},
		},
	}

	msgs, err := req.AgentMessages()
	if err != nil {
		t.Fatalf("AgentMessages() error = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "what is in this image?" {
		t.Fatalf("Content = %q", msgs[0].Content)
	}
	if len(msgs[0].Images) != 1 {
		t.Fatalf("Images = %d, want 1", len(msgs[0].Images))
	}
	if msgs[0].Images[0].MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", msgs[0].Images[0].MediaType)
	}
}

func TestChatCompletionRequest_AgentMessages_RejectsExternalImageURL(t *testing.T) {
	req := ChatCompletionRequest{
		Messages: []chatCompletionRequestMessage{
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}
				]`),
			},
		},
	}

	_, err := req.AgentMessages()
	if err == nil {
		t.Fatal("AgentMessages() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "data:image") {
		t.Fatalf("error = %q, want data URL guidance", err)
	}
}

func TestParseOllamaImages_DetectsMediaType(t *testing.T) {
	dataURL := tinyPNGDataURL(t)
	_, encoded, _ := strings.Cut(dataURL, ",")

	out := parseOllamaImages([]string{encoded})
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if out[0].MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", out[0].MediaType)
	}
}
