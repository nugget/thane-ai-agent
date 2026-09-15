package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// The draft ledger is Thane's record of the drafts it wrote, kept in
// the operational state store under draftLedgerNamespace with the key
// <account>/<draft_id>, where draft_id is a UUIDv7. It is how Thane
// tells its own drafts from the operator's, and ownership is proven,
// never inferred: a draft is Thane's only while its folder still has
// the recorded UIDVALIDITY and still holds the recorded UID with the
// recorded Message-ID (proveOwnership). IMAP never changes a stored
// message under its UID, so a draft that passes holds exactly the bytes
// Thane wrote. An edit in the operator's client stores a new message
// under a new UID, and that draft is the operator's from then on.
//
// The ledger records no review state. An entry is open while its draft
// is Thane's, gone once the proof fails, and withdrawn once Thane moved
// the draft to the trash; nothing else. Its history keeps who wrote
// each version and when, for provenance, and the body one version back.

const (
	// draftLedgerNamespace is the opstate namespace of the ledger.
	draftLedgerNamespace = "email_drafts"

	// maxDraftEntriesPerAccount caps the entries one account keeps.
	maxDraftEntriesPerAccount = 200

	// draftClosedTTL is how long an entry that is gone or withdrawn is
	// kept, through the store's expires_at, before it is forgotten.
	draftClosedTTL = 14 * 24 * time.Hour

	// maxDraftHistory caps the versions an entry's history keeps: the
	// first, as the draft was written, and the latest after it.
	maxDraftHistory = 50
)

// DraftStage is where a ledger entry stands.
type DraftStage string

// Draft stages. An entry is in exactly one.
const (
	// DraftStageOpen: the draft is proven Thane's, and the draft tools
	// may act on it.
	DraftStageOpen DraftStage = "open"

	// DraftStageGone: the draft can no longer be proven Thane's; its
	// closed reason says why. It is the operator's, or no longer exists.
	DraftStageGone DraftStage = "gone"

	// DraftStageWithdrawn: Thane moved the draft to the trash folder.
	DraftStageWithdrawn DraftStage = "withdrawn"
)

// Closed reasons say why an entry left open.
const (
	// closedVanished: the recorded UID is gone and nothing replaced it,
	// so the operator sent or discarded the draft.
	closedVanished = "vanished"

	// closedOperatorTookOver: the recorded UID is gone, and a draft
	// carrying the same Message-ID or answering the same message is in
	// the folder under another UID, which is what an edit leaves.
	closedOperatorTookOver = "operator_took_over"

	// closedTouchedDuringRevision: the operator changed the draft while a
	// revision was replacing it, so Thane's new version was removed again.
	closedTouchedDuringRevision = "operator_touched_during_revision"

	// closedUIDValidityChanged: the drafts folder was rebuilt, so no UID
	// in it can be proven to be the one Thane recorded.
	closedUIDValidityChanged = "uid_validity_changed"

	// closedFolderChanged: the drafts role now resolves to another folder.
	closedFolderChanged = "folder_changed"

	// closedNoDraftsFolder: no folder has the drafts role any more.
	closedNoDraftsFolder = "no_drafts_folder"

	// closedMessageIDChanged: the message at the recorded UID carries
	// another Message-ID.
	closedMessageIDChanged = "message_id_changed"

	// closedMarkedDeleted: the draft is still at its recorded UID but
	// marked \Deleted, which is how a mail client discards a draft
	// before it expunges it, and nothing replaced it.
	closedMarkedDeleted = "marked_deleted"

	// closedWithdrawn: Thane moved the draft to the trash folder.
	closedWithdrawn = "withdrawn"
)

// errNoDraftLedger refuses every draft tool on a service without a
// state store.
var errNoDraftLedger = errors.New("the draft ledger is unavailable: this Thane runs without an operational state store, so drafts are not recorded and no draft tool can act on one; tell the operator")

// draftEntry is one ledger entry.
type draftEntry struct {
	ID           string     `json:"draft_id"`
	Account      string     `json:"account"`
	Stage        DraftStage `json:"stage"`
	ClosedReason string     `json:"closed_reason,omitempty"`

	// Folder, UIDValidity, UID, and MessageID are the identity the proof
	// checks. UID and UIDValidity are zero when the server returned no
	// APPENDUID; such a draft can be counted but never acted on.
	Folder      string `json:"folder"`
	UIDValidity uint32 `json:"uid_validity"`
	UID         uint32 `json:"uid"`
	MessageID   string `json:"message_id"`

	// SHA256 is the hex digest of the current version's exact bytes. It
	// proves a draft whose UID the server never reported, when reconcile
	// finds it by its Message-ID.
	SHA256 string `json:"sha256,omitempty"`

	// From, To, Cc, Subject, InReplyTo, and References are the headers
	// every version keeps: a revision changes only the body.
	From       string   `json:"from"`
	To         []string `json:"to"`
	Cc         []string `json:"cc,omitempty"`
	Subject    string   `json:"subject"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References []string `json:"references,omitempty"`

	// Original is the message the draft answers, or nil.
	Original *draftOriginal `json:"original,omitempty"`

	// Body is the current version's markdown and PreviousBody the one
	// before it, one level deep.
	Body         string `json:"body"`
	PreviousBody string `json:"previous_body,omitempty"`

	// Revisions counts the revisions applied; History lists the
	// versions, oldest first, capped at maxDraftHistory.
	Revisions int             `json:"revisions"`
	History   []draftRevision `json:"history"`

	// Pending is the write-ahead record of a revision in flight: set
	// before the new version is appended, cleared once the ledger holds
	// the outcome. Reconcile settles one a crash left behind.
	Pending *draftPending `json:"pending,omitempty"`

	// WithdrawnTo is where a withdrawn draft went.
	WithdrawnTo *draftWithdrawal `json:"withdrawn_to,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// draftRevision is one version's provenance: who wrote it (a loop name,
// or operator in a turn the operator was present for), when, and why.
type draftRevision struct {
	By   string    `json:"by"`
	At   time.Time `json:"at"`
	Note string    `json:"note,omitempty"`
}

// draftOriginal identifies the message a draft answers, so it can be
// read again beside the draft.
type draftOriginal struct {
	MessageID string  `json:"message_id"`
	Folder    string  `json:"folder"`
	From      Address `json:"from"`
	Subject   string  `json:"subject,omitempty"`
}

// draftPending is a revision's write-ahead record. SHA256 is the hex
// digest of the new version's exact bytes: a message in the folder with
// the pending Message-ID and those bytes is proven to be Thane's copy.
type draftPending struct {
	MessageID string        `json:"message_id"`
	SHA256    string        `json:"sha256"`
	Body      string        `json:"body"`
	Revision  draftRevision `json:"revision"`

	// NewUID and NewUIDValidity are the new version's, recorded once its
	// APPEND answered and before the old version is flagged \Deleted.
	// Zero means Thane had not begun removing the old version, so a
	// \Deleted flag or an absence there is the operator's doing.
	NewUID         uint32 `json:"new_uid,omitempty"`
	NewUIDValidity uint32 `json:"new_uid_validity,omitempty"`
}

// uidKnown reports whether the server reported the draft's UID, without
// which it can be counted but never proven or acted on.
func (e draftEntry) uidKnown() bool { return e.UID != 0 && e.UIDValidity != 0 }

// draftWithdrawal records where a withdrawn draft went and why.
type draftWithdrawal struct {
	Folder string    `json:"folder"`
	UID    uint32    `json:"uid,omitempty"`
	Reason string    `json:"reason,omitempty"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// key is the entry's opstate key.
func (e draftEntry) key() string { return e.Account + "/" + e.ID }

// lastRevision is the current version's provenance.
func (e draftEntry) lastRevision() draftRevision {
	if len(e.History) == 0 {
		return draftRevision{}
	}
	return e.History[len(e.History)-1]
}

// heldByOperator reports whether a closed entry closed because the
// operator took the draft over.
func (e draftEntry) heldByOperator() bool {
	return e.ClosedReason == closedOperatorTookOver || e.ClosedReason == closedTouchedDuringRevision
}

// addRevision appends a version to the history, keeping the first and
// the latest when the cap is reached.
func (e *draftEntry) addRevision(r draftRevision) {
	e.History = append(e.History, r)
	if len(e.History) > maxDraftHistory {
		e.History = append(e.History[:1], e.History[len(e.History)-maxDraftHistory+1:]...)
	}
}

// requireDraftLedger refuses when the service keeps no ledger.
func (s *Service) requireDraftLedger() error {
	if s == nil || s.state == nil {
		return errNoDraftLedger
	}
	return nil
}

// lockDraftLedger takes draftsMu when there is a ledger to guard and
// returns its release.
func (s *Service) lockDraftLedger() func() {
	if s.state == nil {
		return func() {}
	}
	s.draftsMu.Lock()
	return s.draftsMu.Unlock
}

// draftEntries returns an account's entries, or every account's when
// account is "", most recently updated first. An entry that does not
// parse is logged and skipped, so one corrupt row cannot hide the rest.
func (s *Service) draftEntries(account string) ([]draftEntry, error) {
	if err := s.requireDraftLedger(); err != nil {
		return nil, err
	}
	rows, err := s.state.List(draftLedgerNamespace)
	if err != nil {
		return nil, fmt.Errorf("read the draft ledger: %w", err)
	}
	entries := make([]draftEntry, 0, len(rows))
	for key, value := range rows {
		if account != "" && !strings.HasPrefix(key, account+"/") {
			continue
		}
		var e draftEntry
		if err := json.Unmarshal([]byte(value), &e); err != nil || e.ID == "" || e.key() != key {
			s.logger.Warn("email draft ledger entry unreadable; skipped", "key", key, "error", err)
			continue
		}
		if account != "" && e.Account != account {
			continue
		}
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b draftEntry) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return entries, nil
}

// draftByID finds one entry by its draft_id, whatever its account.
func (s *Service) draftByID(id string) (draftEntry, bool, error) {
	entries, err := s.draftEntries("")
	if err != nil {
		return draftEntry{}, false, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, true, nil
		}
	}
	return draftEntry{}, false, nil
}

// saveDraft writes an entry. An open entry never expires; one that is
// gone or withdrawn expires draftClosedTTL after it was last written.
func (s *Service) saveDraft(e *draftEntry) error {
	e.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode draft %s: %w", e.ID, err)
	}
	if e.Stage == DraftStageOpen {
		err = s.state.Set(draftLedgerNamespace, e.key(), string(data))
	} else {
		err = s.state.SetWithTTL(draftLedgerNamespace, e.key(), string(data), draftClosedTTL)
	}
	if err != nil {
		return fmt.Errorf("write draft %s to the draft ledger: %w", e.ID, err)
	}
	return nil
}

// closeDraft moves an entry out of open, records why, and logs one line
// keyed to the turn.
func (s *Service) closeDraft(ctx context.Context, e *draftEntry, stage DraftStage, reason string) error {
	from := e.Stage
	e.Stage, e.ClosedReason, e.Pending = stage, reason, nil
	if err := s.saveDraft(e); err != nil {
		return err
	}
	s.logger.Info("email draft closed",
		"draft_id", e.ID,
		"account", e.Account,
		"from_stage", from,
		"stage", stage,
		"reason", reason,
		"folder", e.Folder,
		"uid", e.UID,
		"uid_validity", e.UIDValidity,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	return nil
}

// recordDraft writes the ledger entry for the draft Send just appended
// and returns its draft_id. It returns "" when the service keeps no
// ledger or the write failed. A failure is logged rather than returned:
// the draft is already in the folder, and an unrecorded draft only
// reads as the operator's, which no draft tool touches.
func (s *Service) recordDraft(ctx context.Context, req SendRequest, composed Composed, appended AppendResult) string {
	if s.state == nil {
		return ""
	}
	id, err := uuid.NewV7()
	if err != nil {
		s.logger.Warn("email draft not recorded: draft_id generation failed", "account", req.Account.Name, "message_id", composed.MessageID, "error", err)
		return ""
	}
	now := time.Now().UTC()
	e := draftEntry{
		ID:          id.String(),
		Account:     req.Account.Name,
		Stage:       DraftStageOpen,
		Folder:      appended.Folder,
		UIDValidity: appended.UIDValidity,
		UID:         appended.UID,
		MessageID:   composed.MessageID,
		SHA256:      contentSum(composed.Bytes),
		From:        req.Account.Config.DefaultFrom,
		To:          addressStrings(composed.To),
		Cc:          addressStrings(composed.Cc),
		Subject:     req.Subject,
		InReplyTo:   req.InReplyTo,
		References:  slices.Clone(req.References),
		Body:        req.Body,
		History:     []draftRevision{{By: draftAuthor(ctx), At: now, Note: "drafted with " + req.Tool}},
		CreatedAt:   now,
	}
	if req.InReplyTo != "" && req.OriginalEnvelope != nil {
		e.Original = &draftOriginal{
			MessageID: req.InReplyTo,
			Folder:    normalizeFolder(req.OriginalFolder),
			From:      req.OriginalEnvelope.From,
			Subject:   req.OriginalEnvelope.Subject,
		}
	}
	if err := s.saveDraft(&e); err != nil {
		s.logger.Warn("email draft not recorded in the draft ledger", "account", e.Account, "folder", e.Folder, "uid", e.UID, "message_id", e.MessageID, "error", err)
		return ""
	}
	s.logger.Info("email draft recorded",
		"draft_id", e.ID,
		"account", e.Account,
		"folder", e.Folder,
		"uid", e.UID,
		"uid_validity", e.UIDValidity,
		"message_id", e.MessageID,
		"in_reply_to", e.InReplyTo,
		"by", e.History[0].By,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	s.enforceDraftCap(e.Account)
	return e.ID
}

// enforceDraftCap keeps at most maxDraftEntriesPerAccount entries for
// an account by forgetting closed entries, oldest first. An open entry
// is never forgotten: it is the only thing that marks a draft still in
// the folder as Thane's, and forgetting it would let a second draft
// answer the same message, or read Thane's own draft as the operator's.
// Send refuses a new draft while the account already has the cap's
// worth open (refuseDraftConflict), so closed entries always suffice.
func (s *Service) enforceDraftCap(account string) {
	entries, err := s.draftEntries(account)
	if err != nil {
		s.logger.Warn("email draft ledger cap not enforced", "account", account, "error", err)
		return
	}
	excess := len(entries) - maxDraftEntriesPerAccount
	if excess <= 0 {
		return
	}
	closed := slices.DeleteFunc(entries, func(e draftEntry) bool { return e.Stage == DraftStageOpen })
	slices.SortStableFunc(closed, func(a, b draftEntry) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	if excess > len(closed) {
		s.logger.Warn("email draft ledger holds more open entries than its cap", "account", account, "entries", len(entries), "cap", maxDraftEntriesPerAccount)
		excess = len(closed)
	}
	for _, e := range closed[:excess] {
		if err := s.state.Delete(draftLedgerNamespace, e.key()); err != nil {
			s.logger.Warn("email draft ledger entry not evicted", "draft_id", e.ID, "account", account, "error", err)
			continue
		}
		s.logger.Info("email draft ledger entry evicted", "draft_id", e.ID, "account", account, "stage", e.Stage, "cap", maxDraftEntriesPerAccount)
	}
}

// draftAuthor names who wrote a version, for its history: operator in a
// turn the operator is present for, else the loop's name when the turn
// carries one, else its loop id, else unattended.
func draftAuthor(ctx context.Context) string {
	if attended(ctx) {
		return "operator"
	}
	if name := strings.TrimSpace(tools.HintsFromContext(ctx)["loop_name"]); name != "" {
		return name
	}
	if id := tools.LoopIDFromContext(ctx); id != "" {
		return "loop:" + id
	}
	return "unattended"
}

// normalizeMessageID strips the angle brackets and whitespace a
// Message-ID may carry, so the ledger and a server compare alike.
func normalizeMessageID(id string) string {
	return strings.Trim(strings.TrimSpace(id), "<>")
}

// sameMessageID reports whether two Message-IDs name the same message.
func sameMessageID(a, b string) bool {
	a, b = normalizeMessageID(a), normalizeMessageID(b)
	return a != "" && a == b
}
