package api

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func tinyPNGDataURL(t *testing.T) string {
	t.Helper()
	return "data:image/png;base64," + validTinyPNGBase64()
}

func validTinyPNGBase64() string {
	return "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aW3cAAAAASUVORK5CYII="
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

func TestChatCompletionRequest_AgentMessages_RejectsInvalidImageData(t *testing.T) {
	req := ChatCompletionRequest{
		Messages: []chatCompletionRequestMessage{
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"image_url","image_url":{"url":"data:image/png;base64,Zm9v"}}
				]`),
			},
		},
	}

	_, err := req.AgentMessages()
	if err == nil {
		t.Fatal("AgentMessages() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "not an image") {
		t.Fatalf("error = %q, want invalid-image guidance", err)
	}
}

func TestParseOllamaImages_DetectsMediaType(t *testing.T) {
	_, encoded, _ := strings.Cut(tinyPNGDataURL(t), ",")

	out, err := parseOllamaImages([]string{encoded})
	if err != nil {
		t.Fatalf("parseOllamaImages() error = %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if out[0].MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", out[0].MediaType)
	}
}

func TestParseOllamaImages_RejectsInvalidImageData(t *testing.T) {
	_, err := parseOllamaImages([]string{base64.StdEncoding.EncodeToString([]byte("not an image"))})
	if err == nil {
		t.Fatal("parseOllamaImages() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "not an image") {
		t.Fatalf("error = %q, want invalid-image guidance", err)
	}
}
