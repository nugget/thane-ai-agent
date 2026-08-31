package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// This file owns reading one OpenAI-compatible SSE stream: turning a
// sequence of frames into a completion, and refusing to turn a sequence
// of frames into a completion that never happened. The request that
// produced the stream is assembled in openaicompat.go.

// streamTrace carries what the caller already knows about a request
// into the streaming reader, so the completion line can report the whole
// call rather than only the part the reader witnessed.
type streamTrace struct {
	log        *slog.Logger
	started    time.Time
	upstreamID string
}

func (c *OpenAICompatClient) handleStreaming(ctx context.Context, requestedModel string, validToolNames []string, body io.Reader, callback llm.StreamCallback, trace streamTrace) (*llm.ChatResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var (
		eventLines     []string
		contentBuilder strings.Builder
		model          = requestedModel
		role           = "assistant"
		createdAt      time.Time
		usage          openAICompatUsage
		toolAcc        = make(map[int]*openAICompatToolAccumulator)
		done           bool
		chunks         int
		reasoningChars int
		finishReason   string
		upstreamID     string
		firstToken     time.Time
	)

	processEvent := func(data string) error {
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			return io.EOF
		}
		// An error frame decodes cleanly into openAICompatChatResponse — every
		// field it carries is unknown to that type — so it would otherwise
		// pass through as a chunk contributing nothing, turning an upstream
		// failure into a silently empty completion. Check for it first.
		if errText := openAICompatStreamErrorText(data); errText != "" {
			return fmt.Errorf("stream error: %s", errText)
		}

		var chunk openAICompatChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}
		chunks++
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.ID != "" {
			upstreamID = chunk.ID
		}
		if chunk.Created > 0 {
			createdAt = time.Unix(chunk.Created, 0).UTC()
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			// The terminating frame carries finish_reason with an empty
			// delta, so read it before the delta guard skips the choice.
			// This is the provider's own termination signal — "length",
			// "tool_calls", "stop", and the pause_turn family — which
			// the iteration layer records and operators read to tell a
			// truncated answer from a complete one.
			if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
				finishReason = strings.TrimSpace(*choice.FinishReason)
			}
			if choice.Delta == nil {
				continue
			}
			if choice.Delta.Role != "" {
				role = choice.Delta.Role
			}
			// The thinking channel counts as generation for timing (the
			// model is producing tokens, so prefill and queueing are
			// over) and for the emptiness verdict below — but it is not
			// content, so it neither accumulates nor streams to the
			// callback.
			if r := choice.Delta.reasoningText(); r != "" {
				if firstToken.IsZero() {
					firstToken = time.Now()
				}
				reasoningChars += len(r)
			}
			if choice.Delta.Content != "" {
				// First content, not first frame: the role-only opener
				// says the server accepted the request, while this says
				// it started answering. The gap between the request and
				// this instant is prefill plus queueing — which is the
				// measurement that separates a runner that is slow from
				// one that is merely busy, and the one nothing recorded
				// when the fleet's throughput was last in question.
				if firstToken.IsZero() {
					firstToken = time.Now()
				}
				contentBuilder.WriteString(choice.Delta.Content)
				callback(llm.StreamEvent{Kind: llm.KindToken, Token: choice.Delta.Content})
			}
			for _, tc := range choice.Delta.ToolCalls {
				acc := toolAcc[tc.Index]
				if acc == nil {
					acc = &openAICompatToolAccumulator{}
					toolAcc[tc.Index] = acc
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					acc.Args.WriteString(tc.Function.Arguments)
				}
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if len(eventLines) == 0 {
				continue
			}
			err := processEvent(strings.Join(eventLines, "\n"))
			eventLines = eventLines[:0]
			if err == io.EOF {
				done = true
				break
			}
			if err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "data:"):
			eventLines = append(eventLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	if len(eventLines) > 0 {
		if err := processEvent(strings.Join(eventLines, "\n")); err != nil && err != io.EOF {
			return nil, err
		}
	}

	toolCalls, err := decodeOpenAICompatToolCalls(toolAcc)
	if err != nil {
		return nil, err
	}

	// Frame count is not evidence of a completion. A role-only opening
	// frame or a usage-only trailer both increment it, so a stream that
	// carried nothing but scaffolding and then closed cleanly would pass
	// a chunks>0 check and return Done:true with no content, no tool
	// calls, and zero tokens — the fabricated success this client exists
	// to refuse. Judge the same thing the non-streaming path judges:
	// whether an assistant actually said or did anything. A stream that
	// carried only reasoning is still a refusal — there is no answer to
	// return — but it is a different diagnosis: the model was working
	// (production saw 814-frame streams reported as "empty") and the
	// answer never reached the content channel, either because the
	// token budget died mid-think or because the runner's template
	// routes the final answer into the reasoning field. Sizes only in
	// the error; reasoning text stays out of logs.
	if strings.TrimSpace(contentBuilder.String()) == "" && len(toolCalls) == 0 {
		if reasoningChars > 0 {
			return nil, fmt.Errorf("%s produced only reasoning for model %q (%d chars over %d frames, finish_reason=%q): the answer never reached the content channel — token budget exhausted mid-think, or a runner template routing the final answer into the reasoning field",
				c.provider, requestedModel, reasoningChars, chunks, finishReason)
		}
		return nil, fmt.Errorf("%s returned an empty stream for model %q (frames=%d)", c.provider, requestedModel, chunks)
	}

	// Content alone does not mean the answer finished. A proxy or runner
	// that dies mid-generation closes the connection cleanly, so the
	// scanner sees an ordinary EOF and everything received so far looks
	// like a completion — a truncated answer presented as a whole one,
	// which is the same lie as a fabricated success wearing better
	// clothes. Require the server to have said it was done: either the
	// [DONE] sentinel or a finish_reason on some choice. Servers vary in
	// which they send, so either suffices; neither is silence.
	if !done && finishReason == "" {
		return nil, fmt.Errorf("%s ended the stream for model %q without a terminal marker after %d frames (truncated response)", c.provider, requestedModel, chunks)
	}

	result := &llm.ChatResponse{
		Model:             model,
		CreatedAt:         createdAt,
		Done:              true,
		StopReason:        finishReason,
		UpstreamRequestID: upstreamID,
		InputTokens:       usage.PromptTokens,
		OutputTokens:      usage.CompletionTokens,
		TotalDuration:     0,
	}
	if trace.upstreamID != "" {
		result.UpstreamRequestID = trace.upstreamID
	}
	result.Message.Role = normalizeOpenAICompatMessageRole(role)
	result.Message.Content = contentBuilder.String()
	result.Message.ToolCalls = toolCalls
	if err := applyTextToolFallback(result, validToolNames); err != nil {
		return nil, err
	}

	attrs := []any{
		"model", result.Model,
		"input_tokens", result.InputTokens,
		"output_tokens", result.OutputTokens,
		"content_len", len(result.Message.Content),
		"tool_calls", len(result.Message.ToolCalls),
		"finish_reason", result.StopReason,
		"upstream_request_id", result.UpstreamRequestID,
		"total_ms", time.Since(trace.started).Milliseconds(),
	}
	// Reported separately, and only when content actually arrived: a
	// turn that produced nothing but tool calls has no first token, and
	// a zero would read as an instant one. Time-to-first-token is what
	// distinguishes a queued request from a slow generation — the two
	// look identical in total duration, and only one of them is fixed by
	// a faster model.
	if !firstToken.IsZero() {
		attrs = append(attrs,
			"first_token_ms", firstToken.Sub(trace.started).Milliseconds(),
			"generation_ms", time.Since(firstToken).Milliseconds(),
		)
	}
	trace.log.Debug("stream complete", attrs...)
	trace.log.Log(ctx, llm.LevelTrace, "stream final content", "content", result.Message.Content)
	return result, nil
}
