package web

import (
	"net/http"

	"github.com/nugget/thane-ai-agent/internal/facts"
)

// FactsData is the template context for the facts list page.
type FactsData struct {
	PageData
	Facts    []*factRow
	Query    string
	Category string
}

// factRow is a display-friendly wrapper around a fact for the list view.
type factRow struct {
	Category   string
	Key        string
	Value      string
	Subjects   []string
	Confidence float64
	Source     string
	UpdatedAt  string
}

// handleFacts renders the facts list page with optional search and category filtering.
func (s *WebServer) handleFacts(w http.ResponseWriter, r *http.Request) {
	if s.factStore == nil {
		http.Error(w, "fact store not configured", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query().Get("q")
	category := r.URL.Query().Get("category")

	data := FactsData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "facts",
		},
		Query:    query,
		Category: category,
	}

	switch {
	case query != "":
		results, err := s.factStore.Search(query)
		if err != nil {
			s.logger.Error("fact search failed", "query", query, "error", err)
			http.Error(w, "search failed", http.StatusInternalServerError)
			return
		}
		data.Facts = factsToRows(results)
	case category != "":
		results, err := s.factStore.GetByCategory(facts.Category(category))
		if err != nil {
			s.logger.Error("fact category filter failed", "category", category, "error", err)
			http.Error(w, "filter failed", http.StatusInternalServerError)
			return
		}
		data.Facts = factsToRows(results)
	default:
		results, err := s.factStore.GetAll()
		if err != nil {
			s.logger.Error("fact list failed", "error", err)
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}
		data.Facts = factsToRows(results)
	}

	// For htmx table-body-only updates, render just the rows.
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "facts-tbody" {
		if s.renderBlock(w, "facts.html", "facts-tbody", data) {
			return
		}
	}

	s.render(w, r, "facts.html", data)
}

func factsToRows(ff []*facts.Fact) []*factRow {
	rows := make([]*factRow, 0, len(ff))
	for _, f := range ff {
		rows = append(rows, &factRow{
			Category:   string(f.Category),
			Key:        f.Key,
			Value:      f.Value,
			Subjects:   f.Subjects,
			Confidence: f.Confidence,
			Source:     f.Source,
			UpdatedAt:  timeAgo(f.UpdatedAt),
		})
	}
	return rows
}
