// Command openclaw-import migrates OpenClaw session data into Thane's archive.
//
// This is a one-time migration tool — the last thing we run on OpenClaw
// before switching to Thane for real.
//
// Usage:
//
//	openclaw-import -openclaw /path/to/.openclaw -data /path/to/thane/data
//
// It reads JSONL session files from OpenClaw's session directory, converts
// them to Thane's archive format, and writes them to Thane's archive.db.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

const sourceType = "openclaw"

func main() {
	openclawDir := flag.String("openclaw", "", "Path to .openclaw directory")
	dataDir := flag.String("data", "", "Path to Thane data directory (where archive.db will be created)")
	dryRun := flag.Bool("dry-run", false, "Parse and report without writing to database")
	purge := flag.Bool("purge", false, "Remove all previously imported OpenClaw data and re-import")
	verbose := flag.Bool("verbose", false, "Verbose output")
	flag.Parse()

	if *openclawDir == "" || *dataDir == "" {
		fmt.Fprintf(os.Stderr, "Usage: openclaw-import -openclaw /path/to/.openclaw -data /path/to/thane/data\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	sessionsDir := filepath.Join(*openclawDir, "agents", "main", "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		logger.Error("sessions directory not found", "path", sessionsDir)
		os.Exit(1)
	}

	// Find all JSONL session files
	files, err := filepath.Glob(filepath.Join(sessionsDir, "*.jsonl"))
	if err != nil {
		logger.Error("glob failed", "error", err)
		os.Exit(1)
	}

	// Filter out deleted sessions
	var activeFiles []string
	for _, f := range files {
		if !strings.Contains(filepath.Base(f), ".deleted.") {
			activeFiles = append(activeFiles, f)
		}
	}

	logger.Info("found session files",
		"total", len(files),
		"active", len(activeFiles),
		"deleted", len(files)-len(activeFiles),
	)

	// Parse all sessions
	var allSessions []parsedSession
	var totalMessages, totalToolCalls int

	for _, f := range activeFiles {
		sess, err := parseSessionFile(f, logger)
		if err != nil {
			logger.Warn("failed to parse session file", "file", filepath.Base(f), "error", err)
			continue
		}
		allSessions = append(allSessions, sess)
		totalMessages += len(sess.messages)
		totalToolCalls += len(sess.toolCalls)
	}

	logger.Info("parsed sessions",
		"sessions", len(allSessions),
		"messages", totalMessages,
		"tool_calls", totalToolCalls,
	)

	if *dryRun {
		fmt.Printf("\n=== Dry Run Summary ===\n")
		fmt.Printf("Sessions:   %d\n", len(allSessions))
		fmt.Printf("Messages:   %d\n", totalMessages)
		fmt.Printf("Tool Calls: %d\n", totalToolCalls)
		fmt.Printf("\nSessions by date:\n")
		for _, s := range allSessions {
			endStr := "active"
			if !s.endedAt.IsZero() {
				endStr = s.endedAt.Format("15:04:05")
			}
			fmt.Printf("  %s  %s → %s  %d msgs, %d tools\n",
				memory.ShortID(s.id),
				s.startedAt.Format("2006-01-02 15:04:05"),
				endStr,
				len(s.messages),
				len(s.toolCalls),
			)
		}
		return
	}

	// Create archive store
	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		logger.Error("failed to create data directory", "error", err)
		os.Exit(1)
	}

	archivePath := filepath.Join(*dataDir, "archive.db")
	store, err := memory.NewArchiveStore(archivePath, nil, logger)
	if err != nil {
		logger.Error("failed to open archive store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	// Purge previously imported data if requested
	if *purge {
		purged, err := store.PurgeImported(sourceType)
		if err != nil {
			logger.Error("purge failed", "error", err)
			os.Exit(1)
		}
		logger.Info("purged previous import", "sessions_removed", purged)
	}

	// Import each session, skipping duplicates
	imported, skipped := 0, 0
	for _, sess := range allSessions {
		already, err := store.IsImported(sess.id, sourceType)
		if err != nil {
			logger.Warn("failed to check import status", "session", memory.ShortID(sess.id), "error", err)
		}
		if already {
			logger.Debug("skipping already-imported session", "openclaw_id", memory.ShortID(sess.id))
			skipped++
			continue
		}

		if err := importSession(store, sess, logger); err != nil {
			logger.Error("failed to import session",
				"session", memory.ShortID(sess.id),
				"error", err,
			)
			continue
		}
		imported++
	}

	logger.Info("import complete",
		"imported", imported,
		"skipped", skipped,
		"failed", len(allSessions)-imported-skipped,
		"archive_path", archivePath,
	)

	// Print stats
	stats, _ := store.Stats()
	fmt.Printf("\n=== Import Complete ===\n")
	fmt.Printf("Archive: %s\n", archivePath)
	fmt.Printf("Sessions imported: %d / %d\n", imported, len(allSessions))
	fmt.Printf("Total archived messages: %v\n", stats["total_messages"])
	fmt.Printf("Total archived tool calls: %v\n", stats["total_tool_calls"])
}

// --- Parsing ---

type parsedSession struct {
	id        string
	startedAt time.Time
	endedAt   time.Time
	messages  []memory.ArchivedMessage
	toolCalls []memory.ArchivedToolCall
}

// openclawLine represents a single JSONL line from an OpenClaw session file.
type openclawLine struct {
	Type       string       `json:"type"`
	ID         string       `json:"id"`
	Timestamp  string       `json:"timestamp"`
	Message    *openclawMsg `json:"message,omitempty"`
	CustomType string       `json:"customType,omitempty"`
}

type openclawMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Timestamp  int64           `json:"timestamp"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
}

type openclawContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

func parseSessionFile(path string, logger *slog.Logger) (parsedSession, error) {
	f, err := os.Open(path)
	if err != nil {
		return parsedSession{}, err
	}
	defer f.Close()

	var sess parsedSession
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	sess.id = sessionID

	scanner := bufio.NewScanner(f)
	// Increase buffer for large JSONL lines (tool results can be huge)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()

		var entry openclawLine
		if err := json.Unmarshal(line, &entry); err != nil {
			logger.Debug("skipping malformed line",
				"file", filepath.Base(path),
				"line", lineNum,
				"error", err,
			)
			continue
		}

		switch entry.Type {
		case "session":
			// Session header — extract start time
			if t, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				sess.startedAt = t
			}

		case "message":
			if entry.Message == nil {
				continue
			}
			ts := parseTimestamp(entry.Timestamp, entry.Message.Timestamp)
			if sess.endedAt.IsZero() || ts.After(sess.endedAt) {
				sess.endedAt = ts
			}

			msgs, tcs := convertMessage(entry, sessionID, ts)
			sess.messages = append(sess.messages, msgs...)
			sess.toolCalls = append(sess.toolCalls, tcs...)
		}
	}

	if err := scanner.Err(); err != nil {
		return sess, fmt.Errorf("scan error: %w", err)
	}

	return sess, nil
}

func convertMessage(entry openclawLine, sessionID string, ts time.Time) ([]memory.ArchivedMessage, []memory.ArchivedToolCall) {
	msg := entry.Message
	var messages []memory.ArchivedMessage
	var toolCalls []memory.ArchivedToolCall

	switch msg.Role {
	case "user":
		text := extractText(msg.Content)
		if text != "" {
			messages = append(messages, memory.ArchivedMessage{
				ID:             entry.ID,
				ConversationID: "openclaw-import",
				SessionID:      sessionID,
				Role:           "user",
				Content:        text,
				Timestamp:      ts,
				TokenCount:     len(text) / 4,
				ArchiveReason:  "import",
			})
		}

	case "assistant":
		// Assistant messages can have text, tool calls, and thinking blocks
		text, tcs := extractAssistantContent(msg.Content)

		if text != "" {
			messages = append(messages, memory.ArchivedMessage{
				ID:             entry.ID,
				ConversationID: "openclaw-import",
				SessionID:      sessionID,
				Role:           "assistant",
				Content:        text,
				Timestamp:      ts,
				TokenCount:     len(text) / 4,
				ArchiveReason:  "import",
			})
		}

		// Convert tool calls
		for _, tc := range tcs {
			argsJSON, err := json.Marshal(tc.Arguments)
			if err != nil {
				argsJSON = []byte("{}")
			}
			toolCalls = append(toolCalls, memory.ArchivedToolCall{
				ID:             tc.ID,
				ConversationID: "openclaw-import",
				SessionID:      sessionID,
				ToolName:       tc.Name,
				Arguments:      string(argsJSON),
				StartedAt:      ts,
			})
		}

	case "toolResult":
		text := extractText(msg.Content)

		messages = append(messages, memory.ArchivedMessage{
			ID:             entry.ID,
			ConversationID: "openclaw-import",
			SessionID:      sessionID,
			Role:           "tool",
			Content:        text,
			Timestamp:      ts,
			TokenCount:     len(text) / 4,
			ToolCallID:     msg.ToolCallID,
			ArchiveReason:  "import",
		})

		// Update the matching tool call with the result
		// (We'll match by toolCallId in a post-processing step)
	}

	return messages, toolCalls
}

func extractText(content json.RawMessage) string {
	// Try as string first
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}

	// Try as array of content blocks
	var blocks []openclawContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}

	var texts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func extractAssistantContent(content json.RawMessage) (string, []openclawContentBlock) {
	// Try as string
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s, nil
	}

	// Try as array
	var blocks []openclawContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return "", nil
	}

	var texts []string
	var toolCalls []openclawContentBlock
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		case "toolCall":
			toolCalls = append(toolCalls, b)
			// Skip "thinking" blocks — internal reasoning, not conversation
		}
	}

	return strings.Join(texts, "\n"), toolCalls
}

// parseTimestamp tries the ISO string first, then unix milliseconds.
// Returns zero time if both fail — callers should check with IsZero().
func parseTimestamp(isoStr string, unixMs int64) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, isoStr); err == nil {
		return t
	}
	if unixMs > 0 {
		return time.UnixMilli(unixMs)
	}
	return time.Time{}
}

// --- Importing ---

func importSession(store *memory.ArchiveStore, sess parsedSession, logger *slog.Logger) error {
	// Create session with original OpenClaw timestamp
	archiveSess, err := store.StartSessionAt("openclaw-import", sess.startedAt)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	// Inject environment preamble so future searches carry provenance context.
	// This helps the agent discount tool calls, file paths, and environment
	// details that reflect the OpenClaw runtime, not the current one.
	preamble := memory.ArchivedMessage{
		ID:             fmt.Sprintf("preamble-%s", archiveSess.ID),
		ConversationID: "openclaw-import",
		SessionID:      archiveSess.ID,
		Role:           "system",
		Content: "This conversation occurred in the OpenClaw runtime (Docker on Linux arm64). " +
			"Tool calls, file paths (/home/aimee/.openclaw/workspace/), and environment " +
			"details reflect that specific environment and may not apply to the current runtime.",
		Timestamp:     sess.startedAt,
		TokenCount:    50,
		ArchiveReason: "import",
	}

	// Archive messages with preamble prepended
	allMessages := append([]memory.ArchivedMessage{preamble}, sess.messages...)
	for i := range allMessages {
		allMessages[i].SessionID = archiveSess.ID
	}
	if err := store.ArchiveMessages(allMessages); err != nil {
		return fmt.Errorf("archive messages: %w", err)
	}

	// Archive tool calls
	if len(sess.toolCalls) > 0 {
		for i := range sess.toolCalls {
			sess.toolCalls[i].SessionID = archiveSess.ID
		}

		// Match tool results to tool calls
		resultsByCallID := make(map[string]string)
		for _, m := range sess.messages {
			if m.Role == "tool" && m.ToolCallID != "" {
				resultsByCallID[m.ToolCallID] = m.Content
			}
		}
		for i, tc := range sess.toolCalls {
			if result, ok := resultsByCallID[tc.ID]; ok {
				sess.toolCalls[i].Result = result
				sess.toolCalls[i].CompletedAt = &sess.endedAt // approximate
			}
		}

		if err := store.ArchiveToolCalls(sess.toolCalls); err != nil {
			return fmt.Errorf("archive tool calls: %w", err)
		}
	}

	// End the session with the original end timestamp
	if err := store.EndSessionAt(archiveSess.ID, "import", sess.endedAt); err != nil {
		return fmt.Errorf("end session: %w", err)
	}

	// Set message count (includes preamble) and summary
	_ = store.SetSessionMessageCount(archiveSess.ID, len(sess.messages)+1)

	summary := fmt.Sprintf("[Imported from OpenClaw session %s]", memory.ShortID(sess.id))
	_ = store.SetSessionSummary(archiveSess.ID, summary)

	// Record the import mapping for idempotent re-runs
	if err := store.RecordImport(sess.id, sourceType, archiveSess.ID); err != nil {
		return fmt.Errorf("record import: %w", err)
	}

	logger.Debug("imported session",
		"openclaw_id", memory.ShortID(sess.id),
		"thane_id", memory.ShortID(archiveSess.ID),
		"started", sess.startedAt.Format(time.RFC3339),
		"messages", len(sess.messages),
		"tool_calls", len(sess.toolCalls),
	)

	return nil
}
