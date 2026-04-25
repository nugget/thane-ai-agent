package logging

import (
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// DefaultLiveRequestStoreSize bounds the number of recent request detail
// records kept in memory for live inspection when archival storage is
// disabled.
const DefaultLiveRequestStoreSize = 512

// RequestRecordFunc captures completed request content for later
// inspection.
type RequestRecordFunc func(ctx context.Context, rc RequestContent)

// CombineRequestRecorders fan-outs request content to every non-nil
// recorder. It returns nil when no recorders are provided.
func CombineRequestRecorders(recorders ...RequestRecordFunc) RequestRecordFunc {
	filtered := make([]RequestRecordFunc, 0, len(recorders))
	for _, recorder := range recorders {
		if recorder != nil {
			filtered = append(filtered, recorder)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return func(ctx context.Context, rc RequestContent) {
			for _, recorder := range filtered {
				recorder(ctx, rc)
			}
		}
	}
}

// LiveRequestStore keeps a bounded in-memory buffer of recent request
// details for live forensics independently of any persistent log index.
type LiveRequestStore struct {
	mu         sync.RWMutex
	maxEntries int
	maxLen     int
	order      *list.List
	entries    map[string]*list.Element
}

type liveRequestEntry struct {
	requestID string
	detail    *RequestDetail
}

// NewLiveRequestStore creates a bounded in-memory request detail store.
// maxEntries defaults to [DefaultLiveRequestStoreSize] when non-positive.
// maxLen follows the same semantics as logging max content length:
// non-positive means unlimited.
func NewLiveRequestStore(maxEntries, maxLen int) *LiveRequestStore {
	if maxEntries <= 0 {
		maxEntries = DefaultLiveRequestStoreSize
	}
	return &LiveRequestStore{
		maxEntries: maxEntries,
		maxLen:     maxLen,
		order:      list.New(),
		entries:    make(map[string]*list.Element, maxEntries),
	}
}

// WriteRequest stores the latest request detail snapshot in memory.
func (s *LiveRequestStore) WriteRequest(_ context.Context, rc RequestContent) {
	if rc.RequestID == "" {
		return
	}
	entry := &liveRequestEntry{
		requestID: rc.RequestID,
		detail:    buildLiveRequestDetail(rc, s.maxLen, time.Now().UTC()),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing := s.entries[rc.RequestID]; existing != nil {
		existing.Value = entry
		s.order.MoveToBack(existing)
		return
	}

	elem := s.order.PushBack(entry)
	s.entries[rc.RequestID] = elem
	for len(s.entries) > s.maxEntries {
		oldest := s.order.Front()
		if oldest == nil {
			break
		}
		s.order.Remove(oldest)
		oldEntry, _ := oldest.Value.(*liveRequestEntry)
		if oldEntry != nil {
			delete(s.entries, oldEntry.requestID)
		}
	}
}

// QueryRequestDetail returns a copy of the stored request detail, or nil
// when the request is no longer present in the live buffer.
func (s *LiveRequestStore) QueryRequestDetail(requestID string) (*RequestDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	elem := s.entries[requestID]
	if elem == nil {
		return nil, nil
	}
	entry, _ := elem.Value.(*liveRequestEntry)
	if entry == nil || entry.detail == nil {
		return nil, nil
	}
	return cloneRequestDetail(entry.detail), nil
}

func buildLiveRequestDetail(rc RequestContent, maxLen int, now time.Time) *RequestDetail {
	detail := &RequestDetail{
		RequestID:        rc.RequestID,
		PromptHash:       hashPrompt(rc.SystemPrompt),
		SystemPrompt:     rc.SystemPrompt,
		UserContent:      truncateRetainedContent(rc.UserContent, maxLen),
		AssistantContent: truncateRetainedContent(rc.AssistantContent, maxLen),
		Model:            rc.Model,
		IterationCount:   rc.IterationCount,
		InputTokens:      rc.InputTokens,
		OutputTokens:     rc.OutputTokens,
		Exhausted:        rc.Exhausted,
		ExhaustReason:    rc.ExhaustReason,
		CreatedAt:        now.Format(time.RFC3339Nano),
		ToolCalls:        extractToolDetails(rc.Messages, maxLen),
	}
	if len(rc.ToolsUsed) > 0 {
		detail.ToolsUsed = make(map[string]int, len(rc.ToolsUsed))
		for name, count := range rc.ToolsUsed {
			detail.ToolsUsed[name] = count
		}
	}
	return detail
}

func cloneRequestDetail(src *RequestDetail) *RequestDetail {
	if src == nil {
		return nil
	}
	dst := *src
	if len(src.ToolsUsed) > 0 {
		dst.ToolsUsed = make(map[string]int, len(src.ToolsUsed))
		for name, count := range src.ToolsUsed {
			dst.ToolsUsed[name] = count
		}
	}
	if src.ToolCalls == nil {
		dst.ToolCalls = []ToolDetail{}
	} else {
		dst.ToolCalls = append([]ToolDetail(nil), src.ToolCalls...)
	}
	return &dst
}

func extractToolDetails(messages []llm.Message, maxLen int) []ToolDetail {
	results := make(map[string]string)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			results[m.ToolCallID] = truncateRetainedContent(m.Content, maxLen)
		}
	}

	iterIdx := 0
	var details []ToolDetail
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		if len(m.ToolCalls) == 0 {
			iterIdx++
			continue
		}
		for _, tc := range m.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Function.Arguments)
			details = append(details, ToolDetail{
				ToolCallID:     tc.ID,
				ToolName:       tc.Function.Name,
				Arguments:      truncateRetainedContent(string(argsJSON), maxLen),
				Result:         results[tc.ID],
				IterationIndex: iterIdx,
			})
		}
		iterIdx++
	}
	if details == nil {
		return []ToolDetail{}
	}
	return details
}

func truncateRetainedContent(s string, maxLen int) string {
	if maxLen <= 0 {
		return s
	}
	runeCount := 0
	for i := range s {
		if runeCount == maxLen {
			return s[:i]
		}
		runeCount++
	}
	return s
}
