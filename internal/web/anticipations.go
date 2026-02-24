package web

import (
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/anticipation"
)

// AnticipationsData is the template context for the anticipations list page.
type AnticipationsData struct {
	PageData
	Anticipations []*anticipationRow
	Filter        string // "active" or "all"
}

// anticipationRow is a display-friendly wrapper around an anticipation.
type anticipationRow struct {
	ID          string
	Description string
	Trigger     string
	Recurring   bool
	Cooldown    string
	Status      string // "active", "resolved", "expired"
	CreatedAt   string
	LastFiredAt string
}

// handleAnticipations renders the anticipations list page.
func (s *WebServer) handleAnticipations(w http.ResponseWriter, r *http.Request) {
	if s.anticipationStore == nil {
		http.Error(w, "anticipation store not configured", http.StatusServiceUnavailable)
		return
	}

	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "active"
	}

	var items []*anticipation.Anticipation
	var err error

	switch filter {
	case "all":
		items, err = s.anticipationStore.All()
	default:
		filter = "active"
		items, err = s.anticipationStore.Active()
	}

	if err != nil {
		s.logger.Error("anticipation list failed", "filter", filter, "error", err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}

	data := AnticipationsData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "anticipations",
		},
		Filter: filter,
	}

	now := time.Now()
	for _, a := range items {
		row := &anticipationRow{
			ID:          a.ID,
			Description: a.Description,
			Trigger:     describeTrigger(a.Trigger),
			Recurring:   a.Recurring,
			CreatedAt:   timeAgo(a.CreatedAt),
		}

		if a.CooldownSeconds > 0 {
			row.Cooldown = (time.Duration(a.CooldownSeconds) * time.Second).String()
		}

		if a.LastFiredAt != nil {
			row.LastFiredAt = timeAgo(*a.LastFiredAt)
		}

		// Determine status.
		switch {
		case a.ResolvedAt != nil:
			row.Status = "resolved"
		case a.ExpiresAt != nil && a.ExpiresAt.Before(now):
			row.Status = "expired"
		default:
			row.Status = "active"
		}

		data.Anticipations = append(data.Anticipations, row)
	}

	// For htmx table-body-only updates.
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "anticipations-tbody" {
		if s.renderBlock(w, "anticipations.html", "anticipations-tbody", data) {
			return
		}
	}

	s.render(w, r, "anticipations.html", data)
}

// describeTrigger returns a brief description of an anticipation trigger.
func describeTrigger(t anticipation.Trigger) string {
	var parts []string

	if t.AfterTime != nil {
		parts = append(parts, "after "+formatTime(*t.AfterTime))
	}
	if t.EntityID != "" {
		desc := t.EntityID
		if t.EntityState != "" {
			desc += " = " + t.EntityState
		}
		parts = append(parts, desc)
	}
	if t.Zone != "" {
		desc := "zone:" + t.Zone
		if t.ZoneAction != "" {
			desc += " (" + t.ZoneAction + ")"
		}
		parts = append(parts, desc)
	}
	if t.EventType != "" {
		parts = append(parts, "event:"+t.EventType)
	}
	if t.Expression != "" {
		parts = append(parts, "expr:"+truncate(t.Expression, 30))
	}

	if len(parts) == 0 {
		return "—"
	}
	return joinStrings(parts, "; ")
}
