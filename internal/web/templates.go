package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"time"
)

//go:embed templates/*.html
var templateFiles embed.FS

// templateFuncs provides helper functions available in all templates.
var templateFuncs = template.FuncMap{
	"formatDuration": formatDuration,
	"formatTokens":   formatTokens,
	"int64":          func(n int) int64 { return int64(n) },
}

// loadTemplates parses the layout and each page template. Each page
// template is a clone of the layout with the page-specific blocks
// overridden. Panics on syntax errors so that startup fails fast.
func loadTemplates() map[string]*template.Template {
	layout := template.Must(
		template.New("layout.html").Funcs(templateFuncs).ParseFS(templateFiles, "templates/layout.html"),
	)

	pages := []string{"dashboard.html", "chat.html"}
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
