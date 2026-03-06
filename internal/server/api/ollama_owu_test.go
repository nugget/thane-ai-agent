package api

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/agent"
)

func TestIsOWUAuxiliaryRequest(t *testing.T) {
	tests := []struct {
		name     string
		messages []agent.Message
		want     bool
	}{
		// Title generation variants
		{
			name: "title generation - brief title",
			messages: []agent.Message{
				{Role: "user", Content: "Hello, how are you?"},
				{Role: "assistant", Content: "I'm doing well, thanks!"},
				{Role: "user", Content: "Generate a brief title for this chat."},
			},
			want: true,
		},
		{
			name: "title generation - concise 3-5 word",
			messages: []agent.Message{
				{Role: "user", Content: "Generate a concise, 3-5 word title for the conversation."},
			},
			want: true,
		},
		{
			name: "title generation - create concise title",
			messages: []agent.Message{
				{Role: "user", Content: "Create a concise title for the following conversation."},
			},
			want: true,
		},
		{
			name: "title generation - case insensitive",
			messages: []agent.Message{
				{Role: "user", Content: "GENERATE A BRIEF TITLE FOR THIS CHAT."},
			},
			want: true,
		},
		{
			name: "title generation - wrapped in longer prompt",
			messages: []agent.Message{
				{Role: "user", Content: "Here is the chat history. Please generate a brief title for this chat based on the conversation above. Only respond with the title."},
			},
			want: true,
		},

		{
			name: "title generation - generate a title for this conversation",
			messages: []agent.Message{
				{Role: "user", Content: "Generate a title for this conversation based on the messages above."},
			},
			want: true,
		},
		{
			name: "title generation - provide a brief title",
			messages: []agent.Message{
				{Role: "user", Content: "Please provide a brief title for the following exchange."},
			},
			want: true,
		},

		// Tag generation variants — one test per pattern
		{
			name: "tag generation - generate tags for this chat",
			messages: []agent.Message{
				{Role: "user", Content: "What's the weather like?"},
				{Role: "assistant", Content: "I don't have access to current weather data."},
				{Role: "user", Content: "Generate tags for this chat."},
			},
			want: true,
		},
		{
			name: "tag generation - suggest tags for this conversation",
			messages: []agent.Message{
				{Role: "user", Content: "Suggest tags for this conversation."},
			},
			want: true,
		},
		{
			name: "tag generation - generate 1-4 word tags",
			messages: []agent.Message{
				{Role: "user", Content: "Generate 1-4 word tags for this chat session."},
			},
			want: true,
		},
		{
			name: "tag generation - provide tags for this chat",
			messages: []agent.Message{
				{Role: "user", Content: "Provide tags for this chat based on the discussion."},
			},
			want: true,
		},

		// Non-auxiliary messages
		{
			name: "normal conversation",
			messages: []agent.Message{
				{Role: "user", Content: "Tell me about Go programming"},
				{Role: "assistant", Content: "Go is a statically typed language..."},
			},
			want: false,
		},
		{
			name: "mentions title but not a generation request",
			messages: []agent.Message{
				{Role: "user", Content: "What's the title of that book you recommended?"},
			},
			want: false,
		},
		{
			name: "mentions tags but not a generation request",
			messages: []agent.Message{
				{Role: "user", Content: "How do I add tags to my blog posts?"},
			},
			want: false,
		},
		{
			name:     "empty messages",
			messages: []agent.Message{},
			want:     false,
		},
		{
			name: "only system message",
			messages: []agent.Message{
				{Role: "system", Content: "You are a helpful assistant."},
			},
			want: false,
		},
		{
			name: "auxiliary pattern in non-last user message is ignored",
			messages: []agent.Message{
				{Role: "user", Content: "Generate a brief title for this chat."},
				{Role: "assistant", Content: "Chat Title"},
				{Role: "user", Content: "Now tell me about Go."},
			},
			want: false,
		},
		{
			name: "assistant message with auxiliary pattern is ignored",
			messages: []agent.Message{
				{Role: "user", Content: "What can you do?"},
				{Role: "assistant", Content: "I can generate a brief title for this chat if you want."},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOWUAuxiliaryRequest(tt.messages)
			if got != tt.want {
				t.Errorf("isOWUAuxiliaryRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}
