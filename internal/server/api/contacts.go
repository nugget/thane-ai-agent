package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

const (
	defaultContactsAPILimit = 100
	maxContactsAPILimit     = 500
)

type contactsListResponse struct {
	Status   string              `json:"status"`
	Count    int                 `json:"count"`
	Contacts []*contacts.Contact `json:"contacts"`
}

type contactResponse struct {
	Status  string            `json:"status"`
	Contact *contacts.Contact `json:"contact"`
}

type contactDeleteResponse struct {
	Status string `json:"status"`
	ID     string `json:"id"`
}

func (s *Server) handleContactsList(w http.ResponseWriter, r *http.Request) {
	if s.contactStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "contact store not configured")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))
	trustZone := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("trust_zone")))
	property := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("property")))
	value := strings.TrimSpace(r.URL.Query().Get("value"))
	exact := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("exact")), "true")
	limit, ok := parseContactsLimit(r)
	if !ok {
		s.errorResponse(w, http.StatusBadRequest, "limit must be a positive integer")
		return
	}

	var (
		list []*contacts.Contact
		err  error
	)
	switch {
	case property != "":
		if value == "" {
			s.errorResponse(w, http.StatusBadRequest, "value is required when property is set")
			return
		}
		if exact {
			list, err = s.contactStore.FindByPropertyExact(property, value)
		} else {
			list, err = s.contactStore.FindByProperty(property, value)
		}
	case query != "":
		list, err = s.contactStore.Search(query)
	case kind != "":
		if !contacts.ValidKinds[kind] {
			s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid kind %q", kind))
			return
		}
		list, err = s.contactStore.ListByKind(kind)
	case trustZone != "":
		if !contacts.ValidTrustZones[trustZone] {
			s.errorResponse(w, http.StatusBadRequest, fmt.Sprintf("invalid trust zone %q", trustZone))
			return
		}
		list, err = s.contactStore.FindByTrustZoneLimit(trustZone, limit)
	default:
		list, err = s.contactStore.ListAllLimit(limit)
	}
	if err != nil {
		s.logger.Error("list contacts failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "failed to list contacts")
		return
	}

	if len(list) > limit {
		list = list[:limit]
	}
	if err := s.hydrateContactProperties(list); err != nil {
		s.logger.Error("hydrate contacts failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, "failed to load contact properties")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, contactsListResponse{
		Status:   "ok",
		Count:    len(list),
		Contacts: list,
	}, s.logger)
}

func (s *Server) handleContactGet(w http.ResponseWriter, r *http.Request) {
	if s.contactStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "contact store not configured")
		return
	}
	id, ok := parseContactID(s, w, r)
	if !ok {
		return
	}

	c, err := s.contactStore.GetWithProperties(id)
	if err != nil {
		s.writeContactStoreError(w, err, "failed to load contact")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, contactResponse{Status: "ok", Contact: c}, s.logger)
}

func (s *Server) handleContactCreate(w http.ResponseWriter, r *http.Request) {
	if s.contactStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "contact store not configured")
		return
	}
	c, props, ok := decodeAPIContact(s, w, r, uuid.Nil)
	if !ok {
		return
	}
	c.ID = uuid.Nil

	saved, err := s.contactStore.UpsertWithProperties(c, props)
	if err != nil {
		s.writeContactStoreError(w, err, "failed to create contact")
		return
	}
	final, err := s.contactStore.GetWithProperties(saved.ID)
	if err != nil {
		s.writeContactStoreError(w, err, "failed to load created contact")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, contactResponse{Status: "ok", Contact: final}, s.logger)
}

func (s *Server) handleContactUpdate(w http.ResponseWriter, r *http.Request) {
	if s.contactStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "contact store not configured")
		return
	}
	id, ok := parseContactID(s, w, r)
	if !ok {
		return
	}
	c, props, ok := decodeAPIContact(s, w, r, id)
	if !ok {
		return
	}
	c.ID = id

	saved, err := s.contactStore.UpsertWithProperties(c, props)
	if err != nil {
		s.writeContactStoreError(w, err, "failed to update contact")
		return
	}
	final, err := s.contactStore.GetWithProperties(saved.ID)
	if err != nil {
		s.writeContactStoreError(w, err, "failed to load updated contact")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, contactResponse{Status: "ok", Contact: final}, s.logger)
}

func (s *Server) handleContactDelete(w http.ResponseWriter, r *http.Request) {
	if s.contactStore == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "contact store not configured")
		return
	}
	id, ok := parseContactID(s, w, r)
	if !ok {
		return
	}
	if _, err := s.contactStore.Get(id); err != nil {
		s.writeContactStoreError(w, err, "failed to load contact")
		return
	}
	if err := s.contactStore.Delete(id); err != nil {
		s.writeContactStoreError(w, err, "failed to delete contact")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, contactDeleteResponse{Status: "ok", ID: id.String()}, s.logger)
}

func parseContactID(s *Server, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := strings.TrimSpace(r.PathValue("id"))
	if raw == "" {
		s.errorResponse(w, http.StatusBadRequest, "contact id is required")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "contact id must be a UUID")
		return uuid.Nil, false
	}
	return id, true
}

func parseContactsLimit(r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultContactsAPILimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, false
	}
	if limit > maxContactsAPILimit {
		limit = maxContactsAPILimit
	}
	return limit, true
}

func decodeAPIContact(s *Server, w http.ResponseWriter, r *http.Request, id uuid.UUID) (*contacts.Contact, []contacts.Property, bool) {
	var c contacts.Contact
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid JSON")
		return nil, nil, false
	}
	if id != uuid.Nil && c.ID != uuid.Nil && c.ID != id {
		s.errorResponse(w, http.StatusBadRequest, "body id must match path id")
		return nil, nil, false
	}
	normalizeAPIContact(&c)
	if c.FormattedName == "" {
		s.errorResponse(w, http.StatusBadRequest, "formatted_name is required")
		return nil, nil, false
	}
	props, err := normalizeAPIContactProperties(c.Properties)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return nil, nil, false
	}
	c.Properties = nil
	return &c, props, true
}

func normalizeAPIContact(c *contacts.Contact) {
	if c == nil {
		return
	}
	c.Kind = strings.ToLower(strings.TrimSpace(c.Kind))
	c.FormattedName = strings.TrimSpace(c.FormattedName)
	c.FamilyName = strings.TrimSpace(c.FamilyName)
	c.GivenName = strings.TrimSpace(c.GivenName)
	c.AdditionalNames = strings.TrimSpace(c.AdditionalNames)
	c.NamePrefix = strings.TrimSpace(c.NamePrefix)
	c.NameSuffix = strings.TrimSpace(c.NameSuffix)
	c.Nickname = strings.TrimSpace(c.Nickname)
	c.Birthday = strings.TrimSpace(c.Birthday)
	c.Anniversary = strings.TrimSpace(c.Anniversary)
	c.Gender = strings.TrimSpace(c.Gender)
	c.Org = strings.TrimSpace(c.Org)
	c.Title = strings.TrimSpace(c.Title)
	c.Role = strings.TrimSpace(c.Role)
	c.Note = strings.TrimSpace(c.Note)
	c.PhotoURI = strings.TrimSpace(c.PhotoURI)
	c.TrustZone = strings.ToLower(strings.TrimSpace(c.TrustZone))
	c.AISummary = strings.TrimSpace(c.AISummary)
}

func normalizeAPIContactProperties(props []contacts.Property) ([]contacts.Property, error) {
	out := make([]contacts.Property, 0, len(props))
	for i, prop := range props {
		prop.Property = strings.ToUpper(strings.TrimSpace(prop.Property))
		prop.Value = strings.TrimSpace(prop.Value)
		prop.Type = strings.TrimSpace(prop.Type)
		prop.Label = strings.TrimSpace(prop.Label)
		prop.MediaType = strings.TrimSpace(prop.MediaType)
		if prop.Property == "" {
			return nil, fmt.Errorf("properties[%d].property is required", i)
		}
		if prop.Value == "" {
			return nil, fmt.Errorf("properties[%d].value is required", i)
		}
		out = append(out, prop)
	}
	return out, nil
}

func (s *Server) hydrateContactProperties(list []*contacts.Contact) error {
	ids := make([]uuid.UUID, 0, len(list))
	for _, c := range list {
		if c == nil {
			continue
		}
		ids = append(ids, c.ID)
	}
	propsByContact, err := s.contactStore.GetPropertiesForContacts(ids)
	if err != nil {
		return fmt.Errorf("get contact properties: %w", err)
	}
	for _, c := range list {
		if c == nil {
			continue
		}
		c.Properties = propsByContact[c.ID]
	}
	return nil
}

func (s *Server) writeContactStoreError(w http.ResponseWriter, err error, fallback string) {
	lower := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.errorResponse(w, http.StatusNotFound, "contact not found")
	case isSQLiteConstraint(err):
		s.errorResponse(w, http.StatusConflict, contactConstraintMessage(err))
	case strings.Contains(lower, "not found"):
		s.errorResponse(w, http.StatusNotFound, "contact not found")
	case strings.Contains(err.Error(), "invalid kind"), strings.Contains(err.Error(), "invalid trust zone"):
		s.errorResponse(w, http.StatusBadRequest, err.Error())
	default:
		s.logger.Error("contact store operation failed", "error", err)
		s.errorResponse(w, http.StatusInternalServerError, fallback)
	}
}

func isSQLiteConstraint(err error) bool {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "constraint") || strings.Contains(lower, "unique")
}

func contactConstraintMessage(err error) string {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		if sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			return "formatted_name must be unique"
		}
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "idx_contacts_fn_active") || strings.Contains(lower, "unique") {
		return "formatted_name must be unique"
	}
	return "contact violates a database constraint"
}
