package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

// SessionsData is the template context for the sessions list page.
type SessionsData struct {
	PageData
	Sessions       []*sessionRow
	ConversationID string
}

// sessionRow is a display-friendly wrapper around a session for the list view.
type sessionRow struct {
	ID             string
	ShortID        string
	ConversationID string
	ShortConvID    string
	Title          string
	OneLiner       string
	SessionType    string
	MessageCount   int
	StartedAt      string
	Duration       string
	Status         string
	EndReason      string
}

// SessionDetailData is the template context for the session detail page.
type SessionDetailData struct {
	PageData
	Session               *sessionDetailView
	Messages              []*messageRow
	ToolCalls             []*toolCallRow
	ToolSummary           []*toolSummaryRow
	Iterations            []*iterationRow
	UnattributedToolCalls []*toolCallRow
}

// iterationRow is a display-friendly wrapper for an iteration in the detail view.
type iterationRow struct {
	Index        int
	Model        string
	InputTokens  int
	OutputTokens int
	ToolCalls    []*toolCallRow
	ToolCount    int
	StartedAt    string
	DurationMs   int64
	HasToolCalls bool
	BreakReason  string
}

// sessionDetailView holds the rich session data for the detail page.
type sessionDetailView struct {
	ID              string
	ShortID         string
	ConversationID  string
	ShortConvID     string
	Title           string
	Summary         string
	Detailed        string
	SessionType     string
	Status          string
	EndReason       string
	StartedAt       string
	EndedAt         string
	Duration        string
	MessageCount    int
	Tags            []string
	KeyDecisions    []string
	Participants    []string
	FilesTouched    []string
	Models          []string
	ParentSessionID string
	ParentShortID   string
	ChildSessions   []*sessionRow
}

// messageRow is a display-friendly wrapper around an archived message.
type messageRow struct {
	Role       string
	Content    string
	Timestamp  string
	TokenCount int
	ToolCallID string
	Long       bool
}

// toolCallRow is a display-friendly wrapper around an archived tool call.
type toolCallRow struct {
	ToolName   string
	Arguments  string
	Result     string
	Error      string
	StartedAt  string
	DurationMs int64
	HasError   bool
}

// toolSummaryRow is a tool name and count pair for sorted display.
type toolSummaryRow struct {
	Name  string
	Count int
}

// handleSessions renders the sessions list page.
func (s *WebServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.sessionStore == nil {
		http.Error(w, "session store not configured", http.StatusServiceUnavailable)
		return
	}

	conversationID := r.URL.Query().Get("conversation_id")

	sessions, err := s.sessionStore.ListSessions(conversationID, 100)
	if err != nil {
		s.logger.Error("session list failed", "error", err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}

	data := SessionsData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "sessions",
		},
		ConversationID: conversationID,
		Sessions:       sessionsToRows(sessions),
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "sessions-tbody" {
		if s.renderBlock(w, "sessions.html", "sessions-tbody", data) {
			return
		}
	}

	s.render(w, r, "sessions.html", data)
}

// handleSessionDetail renders the detail view for a single session.
func (s *WebServer) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	if s.sessionStore == nil {
		http.Error(w, "session store not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	sess, err := s.sessionStore.GetSession(id)
	if err != nil {
		s.logger.Error("session detail failed", "id", id, "error", err)
		http.Error(w, "load failed", http.StatusInternalServerError)
		return
	}
	if sess == nil {
		http.NotFound(w, r)
		return
	}

	messages, err := s.sessionStore.GetSessionTranscript(id)
	if err != nil {
		s.logger.Error("session transcript failed", "id", id, "error", err)
	}

	// For active sessions with no archived messages, fall back to live
	// working memory so the dashboard can show the current transcript.
	var liveRows []*messageRow
	if len(messages) == 0 && sess.EndedAt == nil && s.liveMessages != nil {
		liveRows = liveMessagesToRows(s.liveMessages.GetMessages(sess.ConversationID))
	}

	toolCalls, err := s.sessionStore.GetSessionToolCalls(id)
	if err != nil {
		s.logger.Error("session tool calls failed", "id", id, "error", err)
	}

	iterations, err := s.sessionStore.GetSessionIterations(id)
	if err != nil {
		s.logger.Error("session iterations failed", "id", id, "error", err)
	}

	detail := buildSessionDetailView(sess)

	// Populate parent link if this is a child session.
	if sess.ParentSessionID != "" {
		detail.ParentSessionID = sess.ParentSessionID
		detail.ParentShortID = shortID(sess.ParentSessionID)
	}

	// Fetch child sessions for parent→child navigation.
	children, err := s.sessionStore.ListChildSessions(id)
	if err != nil {
		s.logger.Error("session children failed", "id", id, "error", err)
	}
	if len(children) > 0 {
		detail.ChildSessions = sessionsToRows(children)
	}

	iterRows, unattributed := buildIterationRows(iterations, toolCalls)
	data := SessionDetailData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "sessions",
		},
		Session:               detail,
		Messages:              messageRowsWithFallback(messagesToRows(messages), liveRows),
		ToolCalls:             toolCallsToRows(toolCalls),
		Iterations:            iterRows,
		UnattributedToolCalls: unattributed,
	}

	if sess.Metadata != nil && len(sess.Metadata.ToolsUsed) > 0 {
		data.ToolSummary = toolsUsedToSummary(sess.Metadata.ToolsUsed)
	}

	s.render(w, r, "session_detail.html", data)
}

func sessionsToRows(sessions []*memory.Session) []*sessionRow {
	rows := make([]*sessionRow, 0, len(sessions))
	now := time.Now()
	for _, sess := range sessions {
		row := &sessionRow{
			ID:             sess.ID,
			ShortID:        shortID(sess.ID),
			ConversationID: sess.ConversationID,
			ShortConvID:    shortID(sess.ConversationID),
			MessageCount:   sess.MessageCount,
			StartedAt:      timeAgo(sess.StartedAt),
			Status:         sessionStatus(sess),
			EndReason:      sess.EndReason,
		}

		if sess.Title != "" {
			row.Title = sess.Title
		}
		if sess.Metadata != nil {
			if sess.Metadata.OneLiner != "" {
				row.OneLiner = sess.Metadata.OneLiner
			}
			row.SessionType = sess.Metadata.SessionType
		}

		if sess.EndedAt != nil {
			row.Duration = formatDuration(sess.EndedAt.Sub(sess.StartedAt))
		} else {
			row.Duration = formatDuration(now.Sub(sess.StartedAt))
		}

		rows = append(rows, row)
	}
	return rows
}

func buildSessionDetailView(sess *memory.Session) *sessionDetailView {
	now := time.Now()
	v := &sessionDetailView{
		ID:             sess.ID,
		ShortID:        shortID(sess.ID),
		ConversationID: sess.ConversationID,
		ShortConvID:    shortID(sess.ConversationID),
		Title:          sess.Title,
		Status:         sessionStatus(sess),
		EndReason:      sess.EndReason,
		StartedAt:      formatTime(sess.StartedAt),
		MessageCount:   sess.MessageCount,
		Tags:           sess.Tags,
	}

	if sess.EndedAt != nil {
		v.EndedAt = formatTime(sess.EndedAt)
		v.Duration = formatDuration(sess.EndedAt.Sub(sess.StartedAt))
	} else {
		v.EndedAt = "—"
		v.Duration = formatDuration(now.Sub(sess.StartedAt))
	}

	// Fall back to the top-level summary for older sessions that lack metadata.
	if sess.Summary != "" {
		v.Summary = sess.Summary
	}
	if sess.Metadata != nil {
		if sess.Metadata.Paragraph != "" {
			v.Summary = sess.Metadata.Paragraph
		}
		v.Detailed = sess.Metadata.Detailed
		v.SessionType = sess.Metadata.SessionType
		v.KeyDecisions = sess.Metadata.KeyDecisions
		v.Participants = sess.Metadata.Participants
		v.FilesTouched = sess.Metadata.FilesTouched
		v.Models = sess.Metadata.Models
	}

	return v
}

func messagesToRows(messages []memory.ArchivedMessage) []*messageRow {
	rows := make([]*messageRow, 0, len(messages))
	for _, m := range messages {
		rows = append(rows, &messageRow{
			Role:       m.Role,
			Content:    m.Content,
			Timestamp:  m.Timestamp.Format("15:04:05"),
			TokenCount: m.TokenCount,
			ToolCallID: m.ToolCallID,
			Long:       len(m.Content) > 500,
		})
	}
	return rows
}

// liveMessagesToRows converts live working-memory messages to display rows.
func liveMessagesToRows(messages []memory.Message) []*messageRow {
	rows := make([]*messageRow, 0, len(messages))
	for _, m := range messages {
		rows = append(rows, &messageRow{
			Role:       m.Role,
			Content:    m.Content,
			Timestamp:  m.Timestamp.Format("15:04:05"),
			ToolCallID: m.ToolCallID,
			Long:       len(m.Content) > 500,
		})
	}
	return rows
}

// messageRowsWithFallback returns archived rows if non-empty, otherwise live rows.
func messageRowsWithFallback(archived, live []*messageRow) []*messageRow {
	if len(archived) > 0 {
		return archived
	}
	return live
}

func toolCallsToRows(calls []memory.ArchivedToolCall) []*toolCallRow {
	rows := make([]*toolCallRow, 0, len(calls))
	for _, c := range calls {
		rows = append(rows, &toolCallRow{
			ToolName:   c.ToolName,
			Arguments:  c.Arguments,
			Result:     c.Result,
			Error:      c.Error,
			StartedAt:  c.StartedAt.Format("15:04:05"),
			DurationMs: c.DurationMs,
			HasError:   c.Error != "",
		})
	}
	return rows
}

func toolsUsedToSummary(toolsUsed map[string]int) []*toolSummaryRow {
	rows := make([]*toolSummaryRow, 0, len(toolsUsed))
	for name, count := range toolsUsed {
		rows = append(rows, &toolSummaryRow{Name: name, Count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Count > rows[j].Count
	})
	return rows
}

// buildIterationRows groups iteration data with their nested tool calls.
// Tool calls are matched to iterations by IterationIndex. Unattributed
// tool calls (those without an IterationIndex) are returned separately.
func buildIterationRows(iterations []memory.ArchivedIteration, toolCalls []memory.ArchivedToolCall) ([]*iterationRow, []*toolCallRow) {
	if len(iterations) == 0 {
		return nil, nil
	}

	// Index tool calls by iteration; collect unattributed ones.
	byIter := make(map[int][]*toolCallRow)
	var unattributed []*toolCallRow
	for _, c := range toolCalls {
		row := &toolCallRow{
			ToolName:   c.ToolName,
			Arguments:  c.Arguments,
			Result:     c.Result,
			Error:      c.Error,
			StartedAt:  c.StartedAt.Format("15:04:05"),
			DurationMs: c.DurationMs,
			HasError:   c.Error != "",
		}
		if c.IterationIndex == nil {
			unattributed = append(unattributed, row)
		} else {
			byIter[*c.IterationIndex] = append(byIter[*c.IterationIndex], row)
		}
	}

	rows := make([]*iterationRow, 0, len(iterations))
	for _, iter := range iterations {
		matched := byIter[iter.IterationIndex]
		toolCount := len(matched)
		if toolCount == 0 {
			toolCount = iter.ToolCallCount // fallback to stored count
		}
		rows = append(rows, &iterationRow{
			Index:        iter.IterationIndex,
			Model:        iter.Model,
			InputTokens:  iter.InputTokens,
			OutputTokens: iter.OutputTokens,
			ToolCalls:    matched,
			ToolCount:    toolCount,
			StartedAt:    iter.StartedAt.Format("15:04:05"),
			DurationMs:   iter.DurationMs,
			HasToolCalls: len(matched) > 0,
			BreakReason:  iter.BreakReason,
		})
	}
	return rows, unattributed
}

// sessionStatus derives a display status from session state.
func sessionStatus(sess *memory.Session) string {
	if sess.EndedAt == nil {
		return "active"
	}
	return "completed"
}

// timelineResponse is the JSON API response for a session's timeline data.
type timelineResponse struct {
	Session    timelineSession     `json:"session"`
	Iterations []timelineIteration `json:"iterations"`
	Children   []timelineChild     `json:"children"`
}

// timelineSession is a compact session summary for the timeline API.
type timelineSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	Duration  string `json:"duration"`
}

// timelineIteration is a single iteration for the timeline API.
type timelineIteration struct {
	Index         int                `json:"index"`
	Model         string             `json:"model"`
	InputTokens   int                `json:"input_tokens"`
	OutputTokens  int                `json:"output_tokens"`
	DurationMs    int64              `json:"duration_ms"`
	HasToolCalls  bool               `json:"has_tool_calls"`
	ToolCallCount int                `json:"tool_call_count"`
	BreakReason   string             `json:"break_reason,omitempty"`
	ToolCalls     []timelineToolCall `json:"tool_calls,omitempty"`
}

// timelineToolCall is a compact tool call for the timeline API.
type timelineToolCall struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"duration_ms"`
	HasError   bool   `json:"has_error"`
}

// timelineChild is a compact child session reference for the timeline API.
type timelineChild struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// handleTimelineAPI returns JSON timeline data for a single session.
func (s *WebServer) handleTimelineAPI(w http.ResponseWriter, r *http.Request) {
	if s.sessionStore == nil {
		http.Error(w, "session store not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	sess, err := s.sessionStore.GetSession(id)
	if err != nil {
		s.logger.Error("timeline API session failed", "id", id, "error", err)
		http.Error(w, "load failed", http.StatusInternalServerError)
		return
	}
	if sess == nil {
		http.NotFound(w, r)
		return
	}

	iterations, err := s.sessionStore.GetSessionIterations(id)
	if err != nil {
		s.logger.Error("timeline API iterations failed", "id", id, "error", err)
		http.Error(w, "iterations failed", http.StatusInternalServerError)
		return
	}

	toolCalls, err := s.sessionStore.GetSessionToolCalls(id)
	if err != nil {
		s.logger.Error("timeline API tool calls failed", "id", id, "error", err)
		// Non-fatal: continue without tool calls.
	}

	children, err := s.sessionStore.ListChildSessions(id)
	if err != nil {
		s.logger.Error("timeline API children failed", "id", id, "error", err)
		// Non-fatal: continue without children.
	}

	resp := buildTimelineResponse(sess, iterations, toolCalls, children)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("timeline API encode failed", "id", id, "error", err)
	}
}

// buildTimelineResponse assembles the JSON timeline payload from raw data.
func buildTimelineResponse(
	sess *memory.Session,
	iterations []memory.ArchivedIteration,
	toolCalls []memory.ArchivedToolCall,
	children []*memory.Session,
) timelineResponse {
	now := time.Now()

	// Session summary.
	title := sess.Title
	if title == "" && sess.Metadata != nil {
		title = sess.Metadata.OneLiner
	}
	duration := ""
	if sess.EndedAt != nil {
		duration = formatDuration(sess.EndedAt.Sub(sess.StartedAt))
	} else {
		duration = formatDuration(now.Sub(sess.StartedAt))
	}

	resp := timelineResponse{
		Session: timelineSession{
			ID:        sess.ID,
			Title:     title,
			Status:    sessionStatus(sess),
			StartedAt: formatTime(sess.StartedAt),
			Duration:  duration,
		},
	}

	// Index tool calls by iteration.
	byIter := make(map[int][]timelineToolCall)
	for _, tc := range toolCalls {
		entry := timelineToolCall{
			Name:       tc.ToolName,
			DurationMs: tc.DurationMs,
			HasError:   tc.Error != "",
		}
		if tc.IterationIndex != nil {
			byIter[*tc.IterationIndex] = append(byIter[*tc.IterationIndex], entry)
		}
	}

	// Build iteration list.
	resp.Iterations = make([]timelineIteration, 0, len(iterations))
	for _, iter := range iterations {
		matched := byIter[iter.IterationIndex]
		resp.Iterations = append(resp.Iterations, timelineIteration{
			Index:         iter.IterationIndex,
			Model:         iter.Model,
			InputTokens:   iter.InputTokens,
			OutputTokens:  iter.OutputTokens,
			DurationMs:    iter.DurationMs,
			HasToolCalls:  len(matched) > 0,
			BreakReason:   iter.BreakReason,
			ToolCalls:     matched,
			ToolCallCount: iter.ToolCallCount,
		})
	}

	// Build children list.
	resp.Children = make([]timelineChild, 0, len(children))
	for _, child := range children {
		childTitle := child.Title
		if childTitle == "" && child.Metadata != nil {
			childTitle = child.Metadata.OneLiner
		}
		resp.Children = append(resp.Children, timelineChild{
			ID:     child.ID,
			Title:  childTitle,
			Status: sessionStatus(child),
		})
	}

	return resp
}
