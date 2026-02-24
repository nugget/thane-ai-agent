package web

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/contacts"
)

// ContactsData is the template context for the contacts list page.
type ContactsData struct {
	PageData
	Contacts  []*contactRow
	Query     string
	TrustZone string
	Kind      string
}

// contactRow is a display-friendly wrapper around a contact for the list view.
type contactRow struct {
	ID              uuid.UUID
	Name            string
	Kind            string
	TrustZone       string
	Relationship    string
	Summary         string
	LastInteraction string // relative time
}

// ContactDetailData is the template context for a single contact.
type ContactDetailData struct {
	PageData
	ID           uuid.UUID
	Name         string
	Kind         string
	TrustZone    string
	Relationship string
	Summary      string
	Details      string
	CreatedAt    string
	UpdatedAt    string
	Facts        map[string][]string
}

// handleContacts renders the contacts list page with optional search and filtering.
func (s *WebServer) handleContacts(w http.ResponseWriter, r *http.Request) {
	if s.contactStore == nil {
		http.Error(w, "contact store not configured", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query().Get("q")
	trustZone := r.URL.Query().Get("trust_zone")
	kind := r.URL.Query().Get("kind")

	data := ContactsData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "contacts",
		},
		Query:     query,
		TrustZone: trustZone,
		Kind:      kind,
	}

	var (
		cc  []*contacts.Contact
		err error
	)

	switch {
	case query != "":
		cc, err = s.contactStore.Search(query)
		if err != nil {
			s.logger.Error("contact search failed", "query", query, "error", err)
			http.Error(w, "search failed", http.StatusInternalServerError)
			return
		}
	case trustZone != "":
		cc, err = s.contactStore.FindByTrustZone(trustZone)
		if err != nil {
			s.logger.Error("contact trust zone filter failed", "trust_zone", trustZone, "error", err)
			http.Error(w, "filter failed", http.StatusInternalServerError)
			return
		}
	case kind != "":
		cc, err = s.contactStore.ListByKind(kind)
		if err != nil {
			s.logger.Error("contact kind filter failed", "kind", kind, "error", err)
			http.Error(w, "filter failed", http.StatusInternalServerError)
			return
		}
	default:
		cc, err = s.contactStore.ListAll()
		if err != nil {
			s.logger.Error("contact list failed", "error", err)
			http.Error(w, "list failed", http.StatusInternalServerError)
			return
		}
	}

	data.Contacts = contactsToRows(cc)

	// For htmx table-body-only updates, render just the rows.
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "contacts-tbody" {
		if s.renderBlock(w, "contacts.html", "contacts-tbody", data) {
			return
		}
	}

	s.render(w, r, "contacts.html", data)
}

func contactsToRows(cc []*contacts.Contact) []*contactRow {
	rows := make([]*contactRow, 0, len(cc))
	for _, c := range cc {
		rows = append(rows, &contactRow{
			ID:              c.ID,
			Name:            c.Name,
			Kind:            c.Kind,
			TrustZone:       c.TrustZone,
			Relationship:    c.Relationship,
			Summary:         c.Summary,
			LastInteraction: timeAgo(c.LastInteraction),
		})
	}
	return rows
}

// handleContactDetail renders the detail view for a single contact.
func (s *WebServer) handleContactDetail(w http.ResponseWriter, r *http.Request) {
	if s.contactStore == nil {
		http.Error(w, "contact store not configured", http.StatusServiceUnavailable)
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	c, err := s.contactStore.GetWithFacts(id)
	if err != nil {
		s.logger.Error("contact detail failed", "id", idStr, "error", err)
		http.Error(w, "load failed", http.StatusInternalServerError)
		return
	}
	if c == nil {
		http.NotFound(w, r)
		return
	}

	data := ContactDetailData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "contacts",
		},
		ID:           c.ID,
		Name:         c.Name,
		Kind:         c.Kind,
		TrustZone:    c.TrustZone,
		Relationship: c.Relationship,
		Summary:      c.Summary,
		Details:      c.Details,
		CreatedAt:    formatTime(c.CreatedAt),
		UpdatedAt:    formatTime(c.UpdatedAt),
		Facts:        c.Facts,
	}

	s.render(w, r, "contact_detail.html", data)
}
