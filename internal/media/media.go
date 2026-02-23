package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// Config holds settings for the media transcript client.
type Config struct {
	// YtDlpPath is the path to the yt-dlp binary. If empty, the binary
	// is located via exec.LookPath.
	YtDlpPath string

	// CookiesFile is an optional path to a Netscape-format cookie file
	// for accessing auth-required content.
	CookiesFile string

	// SubtitleLanguage is the preferred subtitle language code (default "en").
	SubtitleLanguage string

	// MaxTranscriptChars limits the transcript text returned in-context.
	// Longer transcripts are truncated. Default: 50000.
	MaxTranscriptChars int

	// WhisperModel is the Ollama model name for audio transcription
	// fallback when no subtitles are available (default "large-v3").
	WhisperModel string

	// TranscriptDir is the directory for durable transcript storage.
	// Each transcript is saved as a markdown file with YAML frontmatter.
	// If empty, transcripts are returned in-context only.
	TranscriptDir string

	// OllamaURL is the base URL for Ollama API calls (Whisper fallback).
	OllamaURL string
}

// Client retrieves and cleans media transcripts.
type Client struct {
	cfg    Config
	logger *slog.Logger
	http   *http.Client
}

// Result holds the fetched transcript and associated metadata.
type Result struct {
	Title          string `json:"title"`
	Channel        string `json:"channel,omitempty"`
	Duration       string `json:"duration,omitempty"`
	UploadDate     string `json:"upload_date,omitempty"`
	Description    string `json:"description,omitempty"`
	Transcript     string `json:"transcript"`
	Source         string `json:"source"`
	ID             string `json:"id"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
}

// New creates a media transcript client. The yt-dlp binary path is
// resolved via Config.YtDlpPath or exec.LookPath.
func New(cfg Config, logger *slog.Logger) *Client {
	if cfg.SubtitleLanguage == "" {
		cfg.SubtitleLanguage = "en"
	}
	if cfg.MaxTranscriptChars == 0 {
		cfg.MaxTranscriptChars = 50000
	}
	if cfg.WhisperModel == "" {
		cfg.WhisperModel = "large-v3"
	}

	// Resolve yt-dlp path.
	if cfg.YtDlpPath == "" {
		if p, err := exec.LookPath("yt-dlp"); err == nil {
			cfg.YtDlpPath = p
		}
	}

	return &Client{
		cfg:    cfg,
		logger: logger,
		http: httpkit.NewClient(
			httpkit.WithTimeout(5 * time.Minute),
		),
	}
}

// ytdlpJSON is the subset of yt-dlp --print-json output we parse.
type ytdlpJSON struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Channel     string  `json:"channel"`
	Uploader    string  `json:"uploader"`
	Duration    float64 `json:"duration"`
	UploadDate  string  `json:"upload_date"`
	Description string  `json:"description"`
	Extractor   string  `json:"extractor_key"`
}

// GetTranscript fetches the transcript for the given media URL.
// It prefers manual subtitles over auto-generated, and falls back to
// Whisper transcription via Ollama when no subtitles are available.
func (c *Client) GetTranscript(ctx context.Context, rawURL, language string) (*Result, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("media_transcript: url is required")
	}
	if language == "" {
		language = c.cfg.SubtitleLanguage
	}
	if c.cfg.YtDlpPath == "" {
		return nil, fmt.Errorf("media_transcript: yt-dlp not found (install yt-dlp or set media.yt_dlp_path)")
	}

	// Create temp dir for subtitle files.
	tmpDir, err := os.MkdirTemp("", "thane-media-*")
	if err != nil {
		return nil, fmt.Errorf("media_transcript: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Run yt-dlp to fetch metadata and subtitles.
	meta, err := c.runYtDlp(ctx, rawURL, language, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("media_transcript: yt-dlp: %w", err)
	}

	// Find subtitle file in tmpDir.
	transcript, err := c.findAndCleanSubtitles(tmpDir, meta.ID, language)
	if err != nil {
		c.logger.Warn("no subtitles found, attempting whisper fallback",
			"url", rawURL, "error", err)

		// Whisper fallback: not yet implemented in Phase 1.
		// Return a clear error so the agent knows what happened.
		return nil, fmt.Errorf("media_transcript: no subtitles available for %q (whisper fallback not yet implemented)", rawURL)
	}

	// Build result.
	source, id := extractSource(rawURL)
	if id == "" {
		id = meta.ID
	}

	desc := meta.Description
	if len(desc) > 500 {
		desc = desc[:500]
	}

	result := &Result{
		Title:       meta.Title,
		Channel:     firstNonEmpty(meta.Channel, meta.Uploader),
		Duration:    formatDuration(meta.Duration),
		UploadDate:  formatDate(meta.UploadDate),
		Description: desc,
		Source:      source,
		ID:          id,
	}

	// Truncate transcript if needed.
	if len(transcript) > c.cfg.MaxTranscriptChars {
		transcript = transcript[:c.cfg.MaxTranscriptChars]
		result.Truncated = true
	}
	result.Transcript = transcript

	// Save transcript to disk if configured.
	if c.cfg.TranscriptDir != "" {
		path, saveErr := c.saveTranscript(result, rawURL)
		if saveErr != nil {
			c.logger.Warn("failed to save transcript",
				"error", saveErr, "url", rawURL)
		} else {
			result.TranscriptPath = path
		}
	}

	return result, nil
}

// runYtDlp executes yt-dlp and returns parsed metadata.
func (c *Client) runYtDlp(ctx context.Context, rawURL, language, tmpDir string) (*ytdlpJSON, error) {
	args := []string{
		"--write-sub",
		"--write-auto-sub",
		"--sub-lang", language,
		"--skip-download",
		"--print-json",
		"--no-warnings",
		"-o", filepath.Join(tmpDir, "%(id)s"),
		rawURL,
	}

	if c.cfg.CookiesFile != "" {
		args = append([]string{"--cookies", c.cfg.CookiesFile}, args...)
	}

	c.logger.Info("running yt-dlp",
		"url", rawURL,
		"language", language,
	)

	cmd := exec.CommandContext(ctx, c.cfg.YtDlpPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errOutput := stderr.String()
		if len(errOutput) > 500 {
			errOutput = errOutput[:500]
		}
		return nil, fmt.Errorf("%w: %s", err, errOutput)
	}

	var meta ytdlpJSON
	if err := json.Unmarshal(stdout.Bytes(), &meta); err != nil {
		return nil, fmt.Errorf("parse yt-dlp output: %w", err)
	}

	return &meta, nil
}

// findAndCleanSubtitles looks for VTT subtitle files in the temp directory,
// preferring manual subs over auto-generated ones.
func (c *Client) findAndCleanSubtitles(tmpDir, videoID, _ string) (string, error) {
	// yt-dlp names files like: {id}.{lang}.vtt (manual) or {id}.{lang}.vtt (auto)
	// With both --write-sub and --write-auto-sub, manual subs take the plain
	// name and auto-subs may not be written if manual exists.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", fmt.Errorf("read temp dir: %w", err)
	}

	var vttFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".vtt") {
			vttFiles = append(vttFiles, filepath.Join(tmpDir, e.Name()))
		}
	}

	if len(vttFiles) == 0 {
		return "", fmt.Errorf("no .vtt files found for %s", videoID)
	}

	// Read the first VTT file (yt-dlp prefers manual when available).
	raw, err := os.ReadFile(vttFiles[0])
	if err != nil {
		return "", fmt.Errorf("read subtitle file: %w", err)
	}

	cleaned := CleanVTTWithParagraphs(string(raw))
	if strings.TrimSpace(cleaned) == "" {
		return "", fmt.Errorf("subtitle file empty after cleaning")
	}

	return cleaned, nil
}

// saveTranscript writes the transcript to disk as a markdown file with
// YAML frontmatter. Returns the absolute path to the saved file.
func (c *Client) saveTranscript(r *Result, originalURL string) (string, error) {
	dir := c.cfg.TranscriptDir

	// Expand ~ to home directory.
	if strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			dir = filepath.Join(home, dir[2:])
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create transcript dir: %w", err)
	}

	filename := sanitizeFilename(r.Source + "-" + r.ID)
	path := filepath.Join(dir, filename+".md")

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.WriteString(fmt.Sprintf("title: %q\n", r.Title))
	if r.Channel != "" {
		buf.WriteString(fmt.Sprintf("channel: %q\n", r.Channel))
	}
	buf.WriteString(fmt.Sprintf("url: %s\n", originalURL))
	buf.WriteString(fmt.Sprintf("source: %s\n", r.Source))
	if r.UploadDate != "" {
		buf.WriteString(fmt.Sprintf("date: %s\n", r.UploadDate))
	}
	if r.Duration != "" {
		buf.WriteString(fmt.Sprintf("duration: %q\n", r.Duration))
	}
	buf.WriteString(fmt.Sprintf("fetched_at: %s\n", time.Now().UTC().Format(time.RFC3339)))
	buf.WriteString("---\n\n")
	buf.WriteString(r.Transcript)
	buf.WriteString("\n")

	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		return "", fmt.Errorf("write transcript: %w", err)
	}

	return path, nil
}

// extractSource parses a URL to determine the source platform and video ID.
func extractSource(rawURL string) (source, id string) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "unknown", ""
	}

	host := strings.ToLower(u.Hostname())

	switch {
	case strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be"):
		source = "youtube"
		if strings.Contains(host, "youtu.be") {
			id = strings.TrimPrefix(u.Path, "/")
		} else {
			id = u.Query().Get("v")
		}
	case strings.Contains(host, "vimeo.com"):
		source = "vimeo"
		id = strings.TrimPrefix(u.Path, "/")
	case strings.Contains(host, "twitch.tv"):
		source = "twitch"
		id = strings.TrimPrefix(u.Path, "/videos/")
	default:
		source = strings.TrimPrefix(host, "www.")
		// Use last path segment as ID.
		parts := strings.Split(strings.TrimRight(u.Path, "/"), "/")
		if len(parts) > 0 {
			id = parts[len(parts)-1]
		}
	}

	return source, id
}

// sanitizeFilename replaces characters that are unsafe in filenames.
var unsafeFilenameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func sanitizeFilename(s string) string {
	return unsafeFilenameRe.ReplaceAllString(s, "_")
}

// formatDuration converts seconds to "H:MM:SS" or "MM:SS" format.
func formatDuration(seconds float64) string {
	total := int(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60

	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// formatDate converts yt-dlp's "YYYYMMDD" date format to "YYYY-MM-DD".
func formatDate(yyyymmdd string) string {
	if len(yyyymmdd) != 8 {
		return yyyymmdd
	}
	return yyyymmdd[:4] + "-" + yyyymmdd[4:6] + "-" + yyyymmdd[6:8]
}

// firstNonEmpty returns the first non-empty string from the arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
