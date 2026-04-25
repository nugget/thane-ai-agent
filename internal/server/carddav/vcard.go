package carddav

import (
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
)

// addressBookPath is the fixed path for the single Thane address book.
const addressBookPath = "/carddav/default/"

// objectPath constructs the CardDAV object path for a contact ID.
func objectPath(id uuid.UUID) string {
	return path.Join(addressBookPath, id.String()+".vcf")
}

// contactIDFromPath extracts the contact UUID from a CardDAV object
// path.  Paths follow the pattern /carddav/default/{uuid}.vcf.
func contactIDFromPath(p string) (uuid.UUID, error) {
	base := path.Base(p)
	name := strings.TrimSuffix(base, ".vcf")
	id, err := uuid.Parse(name)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid contact path %q: %w", p, err)
	}
	return id, nil
}
