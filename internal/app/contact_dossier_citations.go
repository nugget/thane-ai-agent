package app

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// archiveSessionResolver adapts the archive's session-id prefix lookup to
// the resolver contact_dossier_write uses when it refuses a leading part
// of a session id, so the refusal can name the full citation or list the
// sessions that share the prefix. It is nil without an archive store,
// which leaves the refusal with its content-search recovery only.
func archiveSessionResolver(store *memory.ArchiveStore) contacts.ArchiveSessionResolver {
	if store == nil {
		return nil
	}
	return func(ctx context.Context, prefix string) (contacts.ArchiveSessionLookup, error) {
		lookup, err := store.ResolveSessionPrefix(ctx, prefix, memory.DefaultSessionPrefixCandidates)
		if err != nil {
			return contacts.ArchiveSessionLookup{}, err
		}
		matches := make([]contacts.ArchiveSessionMatch, 0, len(lookup.Matches))
		for _, match := range lookup.Matches {
			matches = append(matches, contacts.ArchiveSessionMatch{
				ID:        match.ID,
				StartedAt: match.StartedAt,
				Title:     match.Title,
			})
		}
		return contacts.ArchiveSessionLookup{Matches: matches, Total: lookup.Total}, nil
	}
}
