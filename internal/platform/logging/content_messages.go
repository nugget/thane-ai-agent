package logging

import (
	"encoding/json"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func marshalRetainedMessages(messages []llm.Message, maxLen int) (string, error) {
	retained := retainedMessages(messages, maxLen)
	data, err := json.Marshal(retained)
	if err != nil {
		return "", fmt.Errorf("marshal retained messages: %w", err)
	}
	return string(data), nil
}

func retainedMessages(messages []llm.Message, maxLen int) []MessageDetail {
	retained := make([]MessageDetail, 0, len(messages))
	for i, msg := range messages {
		content, truncated := truncateRetainedContentWithFlag(msg.Content, maxLen)
		detail := MessageDetail{
			Index:            i,
			Role:             msg.Role,
			Content:          content,
			ContentTruncated: truncated,
			ToolCallID:       msg.ToolCallID,
		}
		if len(msg.ToolCalls) > 0 {
			detail.ToolCalls = retainedToolCalls(msg.ToolCalls, maxLen)
		}
		if len(msg.Images) > 0 {
			detail.Images = retainedImages(msg.Images)
		}
		retained = append(retained, detail)
	}
	return retained
}

func retainedToolCalls(calls []llm.ToolCall, maxLen int) []MessageToolCallDetail {
	retained := make([]MessageToolCallDetail, 0, len(calls))
	for _, call := range calls {
		argsJSON, err := json.Marshal(call.Function.Arguments)
		args := ""
		if err != nil {
			args = fmt.Sprintf("failed to marshal arguments: %v", err)
		} else {
			args = truncateRetainedContent(string(argsJSON), maxLen)
		}
		retained = append(retained, MessageToolCallDetail{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: args,
		})
	}
	return retained
}

func retainedImages(images []llm.ImageContent) []MessageImageDetail {
	retained := make([]MessageImageDetail, 0, len(images))
	for _, image := range images {
		retained = append(retained, MessageImageDetail{MediaType: image.MediaType})
	}
	return retained
}

func cloneMessageDetails(src []MessageDetail) []MessageDetail {
	if src == nil {
		return []MessageDetail{}
	}
	dst := make([]MessageDetail, len(src))
	for i := range src {
		dst[i] = src[i]
		dst[i].ToolCalls = append([]MessageToolCallDetail(nil), src[i].ToolCalls...)
		dst[i].Images = append([]MessageImageDetail(nil), src[i].Images...)
	}
	return dst
}
