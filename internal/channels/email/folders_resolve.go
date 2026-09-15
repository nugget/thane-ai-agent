package email

import (
	"context"
	"strings"
)

// Role resolution: which folder on an account holds a special-use role.
// Code never names a folder a site happens to use, so every
// role-addressed operation resolves here, in one order: the account's
// configured key for the role, then the role in the cached folder
// listing, then one fresh LIST, and otherwise unresolved.

// allFolderRoles lists every role roleFromAttrs can report, in the
// order the email_folders description names them. The config package
// validates mailbox.move_into against its own copy of this set, and a
// test pins the two together.
var allFolderRoles = []FolderRole{RoleInbox, RoleDrafts, RoleSent, RoleTrash, RoleJunk, RoleArchive, RoleAll, RoleFlagged, RoleImportant}

// parseFolderRole returns the role named s, compared without case, or
// false when s names none.
func parseFolderRole(s string) (FolderRole, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, r := range allFolderRoles {
		if string(r) == s {
			return r, true
		}
	}
	return "", false
}

// destinationRoleNames lists the roles email_move's destination_role
// offers: every role but drafts, which is never a destination.
func destinationRoleNames() []string {
	names := make([]string, 0, len(allFolderRoles)-1)
	for _, r := range allFolderRoles {
		if r != RoleDrafts {
			names = append(names, string(r))
		}
	}
	return names
}

// configuredRoleFolder returns the folder the account's configuration
// names for role, or "" when it names none. INBOX needs no key: RFC 3501
// guarantees it exists under that name.
func configuredRoleFolder(cfg AccountConfig, role FolderRole) string {
	switch role {
	case RoleInbox:
		return DefaultFolder
	case RoleDrafts:
		return strings.TrimSpace(cfg.DraftsFolder)
	case RoleSent:
		return strings.TrimSpace(cfg.SentFolder)
	case RoleJunk:
		return strings.TrimSpace(cfg.JunkFolder)
	case RoleTrash:
		return strings.TrimSpace(cfg.TrashFolder)
	}
	return ""
}

// roleConfigKey names the account key that answers for role, or ""
// when no key does.
func roleConfigKey(role FolderRole) string {
	switch role {
	case RoleDrafts:
		return "drafts_folder"
	case RoleSent:
		return "sent_folder"
	case RoleJunk:
		return "junk_folder"
	case RoleTrash:
		return "trash_folder"
	}
	return ""
}

// knownRoleFolder resolves role without touching the server: the
// configured key, else the folder the cached listing marks with the
// role, else "" when neither is known yet.
func (s *Service) knownRoleFolder(cfg AccountConfig, role FolderRole) string {
	if folder := configuredRoleFolder(cfg, role); folder != "" {
		return folder
	}
	if snap, ok := s.cachedFolders(cfg.Name); ok {
		if f, found := FindFolderByRole(snap.Folders, role); found {
			return f.Name
		}
	}
	return ""
}

// folderResolver resolves roles for one tool call on one account. It
// lists the account's folders at most once, however many roles the
// call needs, so a move that resolves its destination, its filing
// policy, and the junk folder costs one LIST at worst.
type folderResolver struct {
	service *Service
	acct    ResolvedAccount
	listed  bool

	// err is the failed LIST, if one failed. The roles it would have
	// answered read as unresolved, so a caller checks err once it has
	// resolved everything it needs, before acting on an absence.
	err error
}

func (s *Service) newFolderResolver(acct ResolvedAccount) *folderResolver {
	return &folderResolver{service: s, acct: acct}
}

// folder returns the folder that holds role, or "" when none does: the
// configured key, then the cached listing, then one fresh LIST, which
// also refreshes the cache the Email Accounts block renders from.
func (r *folderResolver) folder(ctx context.Context, role FolderRole) string {
	if folder := r.service.knownRoleFolder(r.acct.Config, role); folder != "" || r.listed {
		return folder
	}
	r.listed = true
	if _, err := r.service.listFolders(ctx, r.acct); err != nil {
		r.err = err
		return ""
	}
	return r.service.knownRoleFolder(r.acct.Config, role)
}
