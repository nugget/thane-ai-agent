package carddav

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"

	"github.com/nugget/thane-ai-agent/internal/contacts"
)

// Backend implements the [carddav.Backend] interface using the
// contacts store.  It exposes a single read-write address book.
type Backend struct {
	store  *contacts.Store
	logger *slog.Logger
}

// NewBackend creates a CardDAV backend backed by the given contact
// store.
func NewBackend(store *contacts.Store, logger *slog.Logger) *Backend {
	return &Backend{store: store, logger: logger}
}

// CurrentUserPrincipal returns the principal URL for the authenticated
// user.  Since Thane uses a single-user model, this is a fixed path.
func (b *Backend) CurrentUserPrincipal(_ context.Context) (string, error) {
	return "/carddav/", nil
}

// AddressBookHomeSetPath returns the home set path where address books
// live.
func (b *Backend) AddressBookHomeSetPath(_ context.Context) (string, error) {
	return "/carddav/", nil
}

// ListAddressBooks returns the single Thane address book.
func (b *Backend) ListAddressBooks(_ context.Context) ([]carddav.AddressBook, error) {
	return []carddav.AddressBook{b.addressBook()}, nil
}

// GetAddressBook returns the address book at the given path.
func (b *Backend) GetAddressBook(_ context.Context, path string) (*carddav.AddressBook, error) {
	if path != addressBookPath {
		return nil, fmt.Errorf("address book not found: %s", path)
	}
	ab := b.addressBook()
	return &ab, nil
}

// CreateAddressBook returns an error because Thane only supports its
// built-in address book.
func (b *Backend) CreateAddressBook(_ context.Context, _ *carddav.AddressBook) error {
	return fmt.Errorf("creating address books is not supported")
}

// DeleteAddressBook returns an error because the built-in address book
// cannot be deleted.
func (b *Backend) DeleteAddressBook(_ context.Context, _ string) error {
	return fmt.Errorf("deleting address books is not supported")
}

// GetAddressObject retrieves a single contact as a CardDAV address
// object.
func (b *Backend) GetAddressObject(_ context.Context, path string, _ *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	id, err := contactIDFromPath(path)
	if err != nil {
		return nil, err
	}
	c, err := b.store.GetWithProperties(id)
	if err != nil {
		return nil, err
	}
	return b.contactToObject(c), nil
}

// ListAddressObjects returns all active contacts as CardDAV address
// objects.
func (b *Backend) ListAddressObjects(_ context.Context, path string, _ *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	if path != addressBookPath {
		return nil, fmt.Errorf("address book not found: %s", path)
	}
	all, err := b.store.ListAllWithProperties()
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	objects := make([]carddav.AddressObject, 0, len(all))
	for _, c := range all {
		objects = append(objects, *b.contactToObject(c))
	}
	return objects, nil
}

// QueryAddressObjects filters contacts based on the CardDAV query.
// It loads all contacts and applies the filter in-memory.
func (b *Backend) QueryAddressObjects(_ context.Context, path string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	if path != addressBookPath {
		return nil, fmt.Errorf("address book not found: %s", path)
	}
	all, err := b.store.ListAllWithProperties()
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}

	var result []carddav.AddressObject
	for _, c := range all {
		obj := b.contactToObject(c)
		if matchesQuery(obj.Card, query) {
			result = append(result, *obj)
		}
	}
	if query.Limit > 0 && len(result) > query.Limit {
		result = result[:query.Limit]
	}
	return result, nil
}

// PutAddressObject creates or updates a contact from a vCard.
func (b *Backend) PutAddressObject(_ context.Context, path string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	id, err := contactIDFromPath(path)
	if err != nil {
		return nil, err
	}

	// Check conditional headers for conflict detection.
	existing, existErr := b.store.GetWithProperties(id)
	exists := existErr == nil

	if opts != nil {
		if opts.IfNoneMatch.IsSet() && opts.IfNoneMatch.IsWildcard() && exists {
			return nil, fmt.Errorf("contact already exists: %s", id)
		}
		if opts.IfMatch.IsSet() && exists {
			matched, matchErr := opts.IfMatch.MatchETag(formatETag(existing.Rev))
			if matchErr != nil {
				return nil, fmt.Errorf("etag match: %w", matchErr)
			}
			if !matched {
				return nil, fmt.Errorf("etag mismatch for %s: expected %s", id, existing.Rev)
			}
		}
	}

	contact, props := CardToContact(card)
	contact.ID = id

	upserted, err := b.store.Upsert(contact)
	if err != nil {
		return nil, fmt.Errorf("upsert contact: %w", err)
	}

	// Replace all properties atomically.
	if err := b.store.DeleteAllProperties(upserted.ID); err != nil {
		return nil, fmt.Errorf("clear properties: %w", err)
	}
	for _, p := range props {
		if err := b.store.AddProperty(upserted.ID, &contacts.Property{
			Property:  p.Property,
			Value:     p.Value,
			Type:      p.Type,
			Pref:      p.Pref,
			Label:     p.Label,
			MediaType: p.MediaType,
		}); err != nil {
			return nil, fmt.Errorf("add property %s: %w", p.Property, err)
		}
	}

	// Re-read to get the final state with Rev set by Upsert.
	final, err := b.store.GetWithProperties(upserted.ID)
	if err != nil {
		return nil, fmt.Errorf("re-read contact: %w", err)
	}
	return b.contactToObject(final), nil
}

// DeleteAddressObject soft-deletes a contact.
func (b *Backend) DeleteAddressObject(_ context.Context, path string) error {
	id, err := contactIDFromPath(path)
	if err != nil {
		return err
	}
	return b.store.Delete(id)
}

// addressBook returns the single Thane address book descriptor.
func (b *Backend) addressBook() carddav.AddressBook {
	ab := carddav.AddressBook{
		Path:        addressBookPath,
		Name:        "Thane Contacts",
		Description: "Thane AI agent contact directory",
		SupportedAddressData: []carddav.AddressDataType{
			{ContentType: "text/vcard", Version: "4.0"},
		},
	}

	ctag, err := b.store.CTag()
	if err != nil {
		b.logger.Warn("failed to compute CTag", "error", err)
	} else if ctag != "" {
		ab.Description = fmt.Sprintf("ctag:%s", ctag)
	}

	return ab
}

// contactToObject converts a Contact (with properties) to a CardDAV
// AddressObject.
func (b *Backend) contactToObject(c *contacts.Contact) *carddav.AddressObject {
	card := ContactToCard(c)
	return &carddav.AddressObject{
		Path:    objectPath(c.ID),
		ModTime: c.UpdatedAt,
		ETag:    formatETag(c.Rev),
		Card:    card,
	}
}

// formatETag wraps a revision string in double quotes for HTTP ETag
// format.
func formatETag(rev string) string {
	return `"` + rev + `"`
}

// matchesQuery checks whether a vCard matches a CardDAV address book
// query.  This is a simplified implementation that handles the common
// PropFilter cases.
func matchesQuery(card vcard.Card, query *carddav.AddressBookQuery) bool {
	if len(query.PropFilters) == 0 {
		return true
	}

	allOf := query.FilterTest == carddav.FilterAllOf
	for _, pf := range query.PropFilters {
		matched := matchesPropFilter(card, pf)
		if allOf && !matched {
			return false
		}
		if !allOf && matched {
			return true
		}
	}
	return allOf
}

// matchesPropFilter checks a single PropFilter against a card.
func matchesPropFilter(card vcard.Card, pf carddav.PropFilter) bool {
	fields := card[pf.Name]
	if pf.IsNotDefined {
		return len(fields) == 0
	}
	if len(fields) == 0 {
		return false
	}

	if len(pf.TextMatches) == 0 && len(pf.Params) == 0 {
		return true
	}

	allOf := pf.Test == carddav.FilterAllOf
	for _, f := range fields {
		for _, tm := range pf.TextMatches {
			matched := matchesText(f.Value, tm)
			if allOf && !matched {
				return false
			}
			if !allOf && matched {
				return true
			}
		}
	}
	return allOf
}

// matchesText checks a single TextMatch against a value.
func matchesText(value string, tm carddav.TextMatch) bool {
	v := strings.ToLower(value)
	t := strings.ToLower(tm.Text)

	var matched bool
	switch tm.MatchType {
	case carddav.MatchEquals:
		matched = v == t
	case carddav.MatchContains, "":
		matched = strings.Contains(v, t)
	case carddav.MatchStartsWith:
		matched = strings.HasPrefix(v, t)
	case carddav.MatchEndsWith:
		matched = strings.HasSuffix(v, t)
	}

	if tm.NegateCondition {
		return !matched
	}
	return matched
}

// ensure Backend satisfies the interface at compile time.
var _ carddav.Backend = (*Backend)(nil)
