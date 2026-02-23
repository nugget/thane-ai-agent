package web

import (
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
	"formatCost":     formatCost,
	"formatTokens":   formatTokens,
	"pct":            pct,
	"int64":          func(n int) int64 { return int64(n) },
}

// loadTemplates parses the layout and each page template. Each page
// template is a clone of the layout with the page-specific blocks
// overridden. Panics on syntax errors so that startup fails fast.
func loadTemplates() map[string]*template.Template {
	layout := template.Must(
		template.New("layout.html").Funcs(templateFuncs).ParseFS(templateFiles, "templates/layout.html"),
	)

	pages := []string{"dashboard.html"}
	result := make(map[string]*template.Template, len(pages))

	for _, page := range pages {
		t := template.Must(layout.Clone())
		template.Must(t.ParseFS(templateFiles, "templates/"+page))
		result[page] = t
	}

	return result
}

// render executes a named template. If the request has the HX-Request
// header (htmx partial), only the "content" block is rendered. Otherwise
// the full layout is rendered.
func (s *WebServer) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	t, ok := s.templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	block := "layout.html"
	if r.Header.Get("HX-Request") == "true" {
		block = "content"
	}

	if err := t.ExecuteTemplate(w, block, data); err != nil {
		s.logger.Error("template render failed", "template", name, "block", block, "error", err)
	}
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

// formatCost renders a USD cost value with appropriate precision.
func formatCost(cost float64) string {
	if cost == 0 {
		return "$0.00"
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
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

// pct computes a percentage from a numerator and denominator. Returns 0
// when the denominator is zero to avoid division by zero.
func pct(num, denom int) int {
	if denom == 0 {
		return 0
	}
	return num * 100 / denom
}
