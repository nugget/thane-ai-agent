package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFiles embed.FS

// templateFuncs provides helper functions available in all templates.
var templateFuncs = template.FuncMap{
	"formatDuration": formatDuration,
	"formatTokens":   formatTokens,
	"int64":          func(n int) int64 { return int64(n) },
	"formatTime":     formatTime,
	"timeAgo":        timeAgo,
	"truncate":       truncate,
	"joinStrings":    joinStrings,
	"confidence":     confidence,
	"lower":          strings.ToLower,
	"shortID":        shortID,
}

// loadTemplates parses the layout and each page template. Each page
// template is a clone of the layout with the page-specific blocks
// overridden. Panics on syntax errors so that startup fails fast.
func loadTemplates() map[string]*template.Template {
	layout := template.Must(
		template.New("layout.html").Funcs(templateFuncs).ParseFS(templateFiles, "templates/layout.html"),
	)

	pages := []string{
		"dashboard.html",
		"chat.html",
		"contacts.html",
		"contact_detail.html",
		"facts.html",
		"tasks.html",
		"task_detail.html",
		"anticipations.html",
		"sessions.html",
		"session_detail.html",
	}
	result := make(map[string]*template.Template, len(pages))

	for _, page := range pages {
		t := template.Must(layout.Clone())
		template.Must(t.ParseFS(templateFiles, "templates/"+page))
		result[page] = t
	}

	return result
}

// render executes a named template into a buffer and writes the result
// only on success. If the request has the HX-Request header (htmx
// partial), only the "content" block is rendered. Otherwise the full
// layout is rendered. Buffering prevents partial HTML from being sent
// to the client when a template error occurs.
func (s *WebServer) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	t, ok := s.templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}

	block := "layout.html"
	if r.Header.Get("HX-Request") == "true" {
		block = "content"
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		s.logger.Error("template render failed", "template", name, "block", block, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// renderBlock renders a specific named template block (e.g., a tbody
// partial for htmx table updates). Returns false if the block is not
// found so the caller can fall back to full-page rendering.
func (s *WebServer) renderBlock(w http.ResponseWriter, name, block string, data any) bool {
	t, ok := s.templates[name]
	if !ok {
		return false
	}
	// Check if the block exists by looking it up.
	if t.Lookup(block) == nil {
		return false
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		s.logger.Error("template block render failed", "template", name, "block", block, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return true // error was handled
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
	return true
}

// formatDuration renders a time.Duration as a human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}

// formatTokens renders a token count with thousands separators.
func formatTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

// formatTime renders a time as "2006-01-02 15:04". It accepts both
// time.Time and *time.Time for template convenience.
func formatTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return "—"
		}
		return t.Format("2006-01-02 15:04")
	case *time.Time:
		if t == nil || t.IsZero() {
			return "—"
		}
		return t.Format("2006-01-02 15:04")
	default:
		return "—"
	}
}

// timeAgo renders a relative time string like "3h ago" or "2d ago".
func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		days := int(math.Round(d.Hours() / 24))
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// truncate shortens a string to n runes, adding "..." if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}

// joinStrings joins a string slice with the given separator.
func joinStrings(ss []string, sep string) string {
	return strings.Join(ss, sep)
}

// confidence formats a float64 (0-1) as a percentage string like "85%".
func confidence(f float64) string {
	if f <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

// shortID truncates an ID string to 8 characters for compact display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
