package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	sigcli "github.com/nugget/thane-ai-agent/internal/channels/messaging/signal"
	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/delegate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/server/api"
	"github.com/nugget/thane-ai-agent/internal/server/web"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// factSetterFunc adapts knowledge.Store to the memory.FactSetter interface,
// adding confidence reinforcement: if a fact already exists, its confidence
// is bumped by 0.1 (capped at 1.0) rather than overwritten. This rewards
// the model for re-extracting known knowledge.
type factSetterFunc struct {
	store  *knowledge.Store
	logger *slog.Logger
}

// SetFact sets a fact, reinforcing confidence if the fact already exists
// with the same value.
func (f *factSetterFunc) SetFact(category, key, value, source string, confidence float64) error {
	// Check for existing fact to apply confidence reinforcement.
	existing, err := f.store.Get(knowledge.Category(category), key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// Real database error (not just "fact doesn't exist yet") — log and bail.
		f.logger.Warn("failed to check existing fact for reinforcement",
			"category", category, "key", key, "error", err)
		return err
	}
	if err == nil && existing != nil {
		if existing.Value == value {
			// Same fact re-observed — reinforce confidence.
			reinforced := min(existing.Confidence+0.1, 1.0)
			if reinforced > confidence {
				confidence = reinforced
			}
			f.logger.Debug("reinforcing existing fact confidence",
				"category", category, "key", key,
				"old_confidence", existing.Confidence,
				"new_confidence", confidence)
		} else {
			// Value changed — this is a correction, not a reinforcement.
			// Use the incoming confidence as-is.
			f.logger.Debug("updating fact value (correction)",
				"category", category, "key", key,
				"old_value", existing.Value, "new_value", value,
				"confidence", confidence)
		}
	}

	_, err = f.store.Set(knowledge.Category(category), key, value, source, confidence, nil, "")
	return err
}

// mqttStatsAdapter bridges the API server and build info to the MQTT
// publisher's [mqtt.StatsSource] interface. It holds only a narrow
// reference to the server (via its lock-protected getter), not a
// direct pointer to mutable stats fields.
type mqttStatsAdapter struct {
	model  string
	server *api.Server
}

// Uptime returns how long the process has been running.
func (a *mqttStatsAdapter) Uptime() time.Duration { return buildinfo.Uptime() }

// Version returns the current build version string.
func (a *mqttStatsAdapter) Version() string { return buildinfo.Version }

// DefaultModel returns the configured default model name.
func (a *mqttStatsAdapter) DefaultModel() string { return a.model }

// LastRequestTime returns the time of the last request processed by the server.
func (a *mqttStatsAdapter) LastRequestTime() time.Time { return a.server.LastRequest() }

// signalChannelSender wraps a [sigcli.Client] for sending text messages
// to a Signal recipient. Used by the loop completion dispatcher to
// deliver detached/async loop results back to the originating channel.
type signalChannelSender struct {
	client *sigcli.Client
}

// SendMessage delivers a text message to the given recipient via Signal.
func (s *signalChannelSender) SendMessage(ctx context.Context, recipient, message string) error {
	_, err := s.client.Send(ctx, recipient, message)
	return err
}

// emailContactResolver resolves email addresses to trust zone levels
// for the email package's send gating. Implements email.ContactResolver.
type emailContactResolver struct {
	store *contacts.Store
}

// ResolveTrustZone returns the trust zone for the contact matching the
// given email address. Returns ("", false, nil) if no contact is found.
func (r *emailContactResolver) ResolveTrustZone(addr string) (string, bool, error) {
	matches, err := r.store.FindByPropertyExact("EMAIL", addr)
	if err != nil {
		return "", false, err
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	return matches[0].TrustZone, true, nil
}

// contactPhoneResolver resolves phone numbers to contact names via the
// contact directory's property store. It looks up contacts with a TEL
// property matching the given phone number.
type contactPhoneResolver struct {
	store *contacts.Store
}

// ResolvePhone returns the name and trust zone of the contact whose TEL
// property matches the given phone number. Returns ("", "", false) if no match.
func (r *contactPhoneResolver) ResolvePhone(phone string) (string, string, bool) {
	matches, err := r.store.FindByPropertyExact("TEL", phone)
	if err != nil || len(matches) == 0 {
		return "", "", false
	}
	return matches[0].FormattedName, matches[0].TrustZone, true
}

// contactChannelBindingResolver resolves a channel/address pair to a
// typed conversation binding with contact identity when available.
type contactChannelBindingResolver struct {
	store            *contacts.Store
	ownerContactName string

	mu                 sync.Mutex
	ownerContactID     uuid.UUID
	ownerContactCached bool
}

// ResolveChannelBinding returns a typed binding for the given
// channel/address pair. It always returns a channel-scoped binding when
// the inputs are non-empty, even if no contact match is found.
func (r *contactChannelBindingResolver) ResolveChannelBinding(channel, address string) *memory.ChannelBinding {
	ownerConfigured := strings.TrimSpace(r.ownerContactName) != ""
	return resolveChannelBinding(r.store, channel, address, ownerConfigured, r.cachedOwnerContactID())
}

// contactNameLookup resolves contact names to rich context profiles for
// channel context injection. Implements agent.ContactLookup.
type contactNameLookup struct {
	store  *contacts.Store
	logger *slog.Logger
}

func (r *contactNameLookup) contactWithPropertiesByName(name string) (*contacts.Contact, []contacts.Property, bool) {
	c, err := r.store.ResolveContact(name)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			r.logger.Error("failed to resolve contact by name", "name", name, "error", err)
		}
		return nil, nil, false
	}
	props, ok := r.propertiesForContact(c)
	return c, props, ok
}

func (r *contactNameLookup) contactWithPropertiesByID(id string) (*contacts.Contact, []contacts.Property, bool) {
	contactID, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		r.logger.Warn("failed to parse contact id", "contact_id", id, "error", err)
		return nil, nil, false
	}

	c, err := r.store.Get(contactID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			r.logger.Error("failed to resolve contact by id", "contact_id", id, "error", err)
		}
		return nil, nil, false
	}
	props, ok := r.propertiesForContact(c)
	return c, props, ok
}

func (r *contactNameLookup) propertiesForContact(c *contacts.Contact) ([]contacts.Property, bool) {
	props, err := r.store.GetProperties(c.ID)
	if err != nil {
		r.logger.Error("failed to get properties for contact", "contact_id", c.ID, "name", c.FormattedName, "error", err)
		return nil, true
	}
	return props, true
}

// LookupContact returns a ContactContext for the given name, or nil if
// no matching contact is found. The source parameter identifies the
// channel so fields can be gated by trust zone — known-zone contacts
// only see the channel matching the current source. Database errors
// other than "not found" are logged so operational issues don't
// silently disable contact context injection.
func (r *contactNameLookup) LookupContact(name string, source string) *agent.ContactContext {
	if r == nil || r.store == nil {
		return nil
	}

	c, props, ok := r.contactWithPropertiesByName(name)
	if !ok {
		return nil
	}

	policy := contacts.Policy(c.TrustZone)
	return buildContactContext(c, props, policy, source, time.Now())
}

// LookupContactByID returns a ContactContext for the exact contact UUID,
// or nil if no matching contact is found.
func (r *contactNameLookup) LookupContactByID(id string, source string) *agent.ContactContext {
	if r == nil || r.store == nil {
		return nil
	}

	c, props, ok := r.contactWithPropertiesByID(id)
	if !ok {
		return nil
	}

	policy := contacts.Policy(c.TrustZone)
	return buildContactContext(c, props, policy, source, time.Now())
}

// LookupContactOriginPolicy returns contact-owned origin policy from the
// contact directory. Policy lives with the contact record so adding,
// removing, or changing origin tags does not require editing config or
// restarting the agent.
func (r *contactNameLookup) LookupContactOriginPolicy(id string, name string, source string) *agent.ContactOriginPolicy {
	if r == nil || r.store == nil {
		return nil
	}
	var props []contacts.Property
	var ok bool
	if strings.TrimSpace(id) != "" {
		_, props, ok = r.contactWithPropertiesByID(id)
	}
	if !ok && strings.TrimSpace(name) != "" {
		_, props, ok = r.contactWithPropertiesByName(name)
	}
	if !ok {
		return nil
	}
	policy := contacts.OriginPolicyFromProperties(props, source)
	if policy.Empty() {
		return nil
	}
	return &agent.ContactOriginPolicy{
		Tags:        policy.Tags,
		ContextRefs: policy.ContextRefs,
	}
}

// buildContactContext assembles a ContactContext from a contact record,
// its properties, and the applicable trust policy. Fields are gated by
// trust zone — lower zones receive fewer fields.
// Size limits for ContactContext fields to prevent prompt bloat.
const (
	maxSummaryLen = 300 // characters in ai_summary
	maxGroups     = 10
	maxRelated    = 10
	maxTopics     = 10
)

// buildContactContext constructs the agent's view of a contact for
// system prompt injection, gated by trust zone.
func buildContactContext(c *contacts.Contact, props []contacts.Property, policy contacts.ZonePolicy, source string, now time.Time) *agent.ContactContext {
	ctx := &agent.ContactContext{
		ID:        c.ID.String(),
		Name:      c.FormattedName,
		TrustZone: c.TrustZone,
		TrustPolicy: &agent.TrustPolicyView{
			FrontierModel:     policy.FrontierModelAccess,
			ProactiveOutreach: policy.ProactiveOutreach,
			ToolAccess:        policy.ToolAccess,
			SendGating:        policy.SendGating,
		},
		ContactSince: c.CreatedAt.Format("2006-01-02"),
	}

	// Known zone: minimal fields — name, trust_zone, trust_policy,
	// current-channel only, contact_since.
	if c.TrustZone == contacts.ZoneKnown {
		channels := extractChannels(props)
		if filtered := filterChannelsForSource(channels, source); len(filtered) > 0 {
			ctx.Channels = filtered
		}
		return ctx
	}

	// Trusted, household, admin: full profile.
	ctx.GivenName = c.GivenName
	ctx.FamilyName = c.FamilyName
	summary := c.AISummary
	if len(summary) > maxSummaryLen {
		summary = summary[:maxSummaryLen] + "…"
	}
	ctx.Summary = summary

	if c.Org != "" {
		ctx.Org = &c.Org
	}
	if c.Title != "" {
		ctx.Title = &c.Title
	}
	if c.Role != "" {
		ctx.Role = &c.Role
	}

	// Extract structured data from properties, capped to prevent
	// large contact records from bloating the system prompt.
	ctx.Channels = extractChannels(props)
	if groups := extractGroups(props); len(groups) > maxGroups {
		ctx.Groups = groups[:maxGroups]
	} else {
		ctx.Groups = groups
	}
	if related := extractRelated(props); len(related) > maxRelated {
		ctx.Related = related[:maxRelated]
	} else {
		ctx.Related = related
	}

	// Interaction history (trusted+).
	if !c.LastInteraction.IsZero() {
		ref := &agent.InteractionRef{
			AgoSeconds: int64(c.LastInteraction.Sub(now).Truncate(time.Second).Seconds()),
		}
		if c.LastInteractionMeta != nil {
			ref.Channel = c.LastInteractionMeta.Channel
			ref.SessionID = c.LastInteractionMeta.SessionID
			topics := c.LastInteractionMeta.Topics
			if len(topics) > maxTopics {
				topics = topics[:maxTopics]
			}
			ref.Topics = topics
		}
		ctx.LastInteraction = ref
	}

	return ctx
}

// extractChannels builds a channels map from EMAIL, TEL, and IMPP
// properties. IMPP values are split on prefix (e.g., "signal:+1..." →
// channels["signal"]).
func extractChannels(props []contacts.Property) map[string]any {
	channels := make(map[string]any)

	var emails, tels []string
	imppByScheme := make(map[string][]string)

	for _, p := range props {
		switch p.Property {
		case "EMAIL":
			emails = append(emails, p.Value)
		case "TEL":
			tels = append(tels, p.Value)
		case "IMPP":
			scheme, addr, ok := strings.Cut(p.Value, ":")
			if ok {
				imppByScheme[scheme] = append(imppByScheme[scheme], addr)
			} else {
				imppByScheme["other"] = append(imppByScheme["other"], p.Value)
			}
		}
	}

	if len(emails) > 0 {
		channels["email"] = emails
	}
	if len(tels) > 0 {
		channels["tel"] = tels
	}
	for scheme, addrs := range imppByScheme {
		if len(addrs) == 1 {
			channels[scheme] = addrs[0]
		} else {
			channels[scheme] = addrs
		}
	}

	if len(channels) == 0 {
		return nil
	}
	return channels
}

// extractGroups returns group names from CATEGORIES properties.
// Each CATEGORIES value may be comma-separated per vCard spec.
func extractGroups(props []contacts.Property) []string {
	var groups []string
	for _, p := range props {
		if p.Property == "CATEGORIES" {
			for _, cat := range strings.Split(p.Value, ",") {
				cat = strings.TrimSpace(cat)
				if cat != "" {
					groups = append(groups, cat)
				}
			}
		}
	}
	return groups
}

// extractRelated returns related contacts from RELATED properties.
func extractRelated(props []contacts.Property) []RelatedContact {
	var related []RelatedContact
	for _, p := range props {
		if p.Property == "RELATED" {
			rc := RelatedContact{Name: p.Value}
			if p.Type != "" {
				rc.Type = p.Type
			}
			related = append(related, rc)
		}
	}
	return related
}

// RelatedContact mirrors agent.RelatedContact for the app package
// builder. We re-export the agent type alias here for clarity.
type RelatedContact = agent.RelatedContact

// filterChannelsForSource returns only channel entries relevant to the
// source hint. Used for known-zone contacts where only the current
// communication channel is revealed. For Signal, also includes "tel"
// since Signal contacts are often identified by phone number even when
// they lack an explicit IMPP signal: property.
func filterChannelsForSource(channels map[string]any, source string) map[string]any {
	if channels == nil {
		return nil
	}
	result := make(map[string]any)
	if val, ok := channels[source]; ok {
		result[source] = val
	}
	// Signal contacts may only have TEL properties without an IMPP
	// signal: entry. Include tel so the agent sees their phone number.
	if source == "signal" {
		if val, ok := channels["tel"]; ok {
			result["tel"] = val
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// updateContactInteraction resolves a contact from a conversation ID
// and updates their last interaction metadata. Conversation IDs follow
// the pattern "channel-address" (e.g., "signal-15551234567").
func updateContactInteraction(store *contacts.Store, logger *slog.Logger, conversationID, sessionID string, endedAt time.Time, topics []string) {
	channel, address, ok := strings.Cut(conversationID, "-")
	if !ok || channel == "" || address == "" {
		return // Not a channel conversation (e.g., API, scheduler).
	}

	contactID, found := resolveContactByChannelAddress(store, channel, address)
	if !found {
		return
	}

	meta := &contacts.InteractionMeta{
		Channel:   channel,
		SessionID: sessionID,
		Topics:    topics,
	}
	if err := store.UpdateLastInteraction(contactID, endedAt, meta); err != nil {
		logger.Warn("failed to update contact interaction",
			"contact_id", contactID,
			"conversation_id", conversationID,
			"error", err,
		)
	}
}

// resolveContactByChannelAddress finds a contact by their channel
// address. For Signal, checks IMPP (signal:address) then TEL fallback.
// For email, checks EMAIL property.
func resolveContactByChannelAddress(store *contacts.Store, channel, address string) (uuid.UUID, bool) {
	id, _, ok := resolveContactByChannelLink(store, channel, address)
	return id, ok
}

func resolveChannelBinding(store *contacts.Store, channel, address string, ownerConfigured bool, ownerContactID uuid.UUID) *memory.ChannelBinding {
	binding := (&memory.ChannelBinding{
		Channel: channel,
		Address: address,
	}).Normalize()
	if binding == nil || store == nil {
		return binding
	}

	contactID, linkSource, found := resolveContactByChannelLink(store, binding.Channel, binding.Address)
	if !found {
		return binding
	}

	contact, err := store.Get(contactID)
	if err != nil || contact == nil {
		return binding
	}

	binding.ContactID = contact.ID.String()
	binding.ContactName = contact.FormattedName
	binding.TrustZone = contact.TrustZone
	binding.LinkSource = linkSource
	binding.IsOwner = isOwnerContact(store, contact, ownerConfigured, ownerContactID)
	return binding.Normalize()
}

func (r *contactChannelBindingResolver) cachedOwnerContactID() uuid.UUID {
	if r == nil || r.store == nil || strings.TrimSpace(r.ownerContactName) == "" {
		return uuid.Nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ownerContactCached {
		return r.ownerContactID
	}

	owner, err := r.store.ResolveContact(r.ownerContactName)
	if err == nil && owner != nil {
		r.ownerContactID = owner.ID
	}
	r.ownerContactCached = true
	return r.ownerContactID
}

func isOwnerContact(store *contacts.Store, contact *contacts.Contact, ownerConfigured bool, ownerContactID uuid.UUID) bool {
	if store == nil || contact == nil {
		return false
	}

	if ownerConfigured {
		return ownerContactID != uuid.Nil && ownerContactID == contact.ID
	}

	admins, err := store.FindByTrustZone(contacts.ZoneAdmin)
	if err != nil || len(admins) != 1 {
		return false
	}
	return admins[0].ID == contact.ID
}

func resolveContactByChannelLink(store *contacts.Store, channel, address string) (uuid.UUID, string, bool) {
	var nilID uuid.UUID

	switch channel {
	case "signal":
		// Signal conversation IDs use sanitizePhone which strips the "+"
		// prefix (e.g., "+15551234567" → "15551234567"), but contact
		// properties store the canonical form with "+". Try both forms.
		candidates := []string{address}
		if address != "" && address[0] != '+' {
			candidates = append(candidates, "+"+address)
		}
		for _, addr := range candidates {
			matches, err := store.FindByPropertyExact("IMPP", "signal:"+addr)
			if err == nil && len(matches) == 1 {
				return matches[0].ID, "impp", true
			}
		}
		// Fallback to TEL (also try both forms).
		for _, addr := range candidates {
			matches, err := store.FindByPropertyExact("TEL", addr)
			if err == nil && len(matches) == 1 {
				return matches[0].ID, "tel", true
			}
		}
	case "email":
		matches, err := store.FindByPropertyExact("EMAIL", address)
		if err == nil && len(matches) == 1 {
			return matches[0].ID, "email", true
		}
	}

	return nilID, "", false
}

// conversationSystemInjector is the shared app-side bridge for writing
// detached messages back into live conversations. Both notification
// callbacks and loops-ng detached completions use this adapter so
// completion routing converges on one app-level seam.
type conversationSystemInjector struct {
	mem      memory.MemoryStore
	archiver *memory.ArchiveAdapter
}

// InjectSystemMessage adds a system message to the conversation's
// memory so the agent sees it on the next turn.
func (n *conversationSystemInjector) InjectSystemMessage(conversationID, message string) error {
	if n == nil || n.mem == nil {
		return nil
	}
	if conversationID == "" || strings.TrimSpace(message) == "" {
		return nil
	}
	return n.mem.AddMessage(conversationID, "system", message)
}

// InjectAssistantMessage adds an assistant-authored message to the
// conversation's memory so channel-shaped detached completions can
// appear in the same transcript as normal replies.
func (n *conversationSystemInjector) InjectAssistantMessage(conversationID, message string) error {
	if n == nil || n.mem == nil {
		return nil
	}
	if conversationID == "" || strings.TrimSpace(message) == "" {
		return nil
	}
	return n.mem.AddMessage(conversationID, "assistant", message)
}

// IsSessionAlive reports whether the conversation has an active
// archive session.
func (n *conversationSystemInjector) IsSessionAlive(conversationID string) bool {
	if n == nil || n.archiver == nil || conversationID == "" {
		return false
	}
	return n.archiver.ActiveSessionID(conversationID) != ""
}

// notifDelegateSpawner adapts the delegate executor into a
// [notifications.DelegateSpawner].
type notifDelegateSpawner struct {
	exec *delegate.Executor
}

// Spawn executes the task in a lightweight delegate loop.
func (n *notifDelegateSpawner) Spawn(ctx context.Context, task, guidance string) error {
	_, err := n.exec.Execute(ctx, task, "", guidance, nil)
	return err
}

// channelLoopAdapter bridges [agent.ChannelLoopSource] to the loop
// registry, filtering for channel-category loops only.
type channelLoopAdapter struct {
	registry *looppkg.Registry
}

// ChannelLoops returns loop snapshots for all loops with
// category=channel metadata (both parents and children). Consumers
// that need only child loops should filter on channel-specific
// identifiers (e.g., sender for signal, conversation_id for owu).
func (a *channelLoopAdapter) ChannelLoops() []agent.LoopSnapshot {
	statuses := a.registry.Statuses()
	var result []agent.LoopSnapshot
	for _, s := range statuses {
		if s.Config.Metadata["category"] != "channel" {
			continue
		}
		result = append(result, agent.LoopSnapshot{
			ID:            s.ID,
			Name:          s.Name,
			State:         string(s.State),
			LastWakeAt:    s.LastWakeAt,
			Metadata:      s.Config.Metadata,
			RecentConvIDs: s.RecentConvIDs,
		})
	}
	return result
}

// signalMemoryRecorder records outbound Signal notifications in
// conversation memory so the agent has context when the user replies.
// Implements [notifications.MessageRecorder].
type signalMemoryRecorder struct {
	mem memory.MemoryStore
}

// RecordOutbound stores an annotated assistant message in the Signal
// conversation for the given phone number.
func (r *signalMemoryRecorder) RecordOutbound(phone, message string) error {
	// Derive conversation ID the same way the Signal bridge does:
	// "signal-" + phone normalized to alphanumeric characters.
	var sb strings.Builder
	for _, c := range phone {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			sb.WriteRune(c)
		}
	}
	convID := "signal-" + sb.String()
	return r.mem.AddMessage(convID, "assistant", message)
}

// channelActivityAdapter bridges [notifications.ChannelActivitySource]
// to the loop registry, resolving sender identities to contact names.
type channelActivityAdapter struct {
	loops *channelLoopAdapter
	store *contacts.Store
}

// ownerChannelActivityAdapter returns active channel loops that the
// runtime has marked as owner-scoped. This lets helper tools surface a
// structured view of current owner conversations without inferring
// authenticity from prompt hints.
type ownerChannelActivityAdapter struct {
	loops *channelLoopAdapter
}

// ActiveOwnerChannels returns active owner-scoped channel loops in a
// stable order, newest activity first.
func (a *ownerChannelActivityAdapter) ActiveOwnerChannels() []contacts.OwnerChannelActivity {
	if a == nil || a.loops == nil {
		return nil
	}
	loops := a.loops.ChannelLoops()
	result := make([]contacts.OwnerChannelActivity, 0, len(loops))
	for _, l := range loops {
		if !strings.EqualFold(l.Metadata["is_owner"], "true") {
			continue
		}
		subsystem := l.Metadata["subsystem"]
		if subsystem == "" {
			continue
		}
		convID := l.Metadata["conversation_id"]
		if convID == "" && len(l.RecentConvIDs) > 0 {
			convID = l.RecentConvIDs[0]
		}
		result = append(result, contacts.OwnerChannelActivity{
			Channel:        subsystem,
			LoopID:         l.ID,
			LoopName:       l.Name,
			ConversationID: convID,
			ContactName:    l.Metadata["contact_name"],
			State:          l.State,
			LastActive:     l.LastWakeAt,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastActive.After(result[j].LastActive)
	})
	return result
}

// ActiveChannels returns channel activity entries for active channel
// child loops, resolving Signal phone numbers to contact names via
// both TEL and IMPP properties.
func (a *channelActivityAdapter) ActiveChannels() []notifications.ChannelActivity {
	loops := a.loops.ChannelLoops()
	var result []notifications.ChannelActivity
	for _, l := range loops {
		subsystem := l.Metadata["subsystem"]
		if subsystem == "" {
			continue
		}
		// Skip parent loops (no per-conversation identity).
		if subsystem == "signal" && l.Metadata["sender"] == "" {
			continue
		}
		if subsystem == "owu" && l.Metadata["conversation_id"] == "" {
			continue
		}

		entry := notifications.ChannelActivity{
			Channel:    subsystem,
			LastActive: l.LastWakeAt,
		}

		// Resolve contact name from channel-specific identifiers.
		if a.store != nil {
			switch subsystem {
			case "signal":
				if sender := l.Metadata["sender"]; sender != "" {
					entry.Contact = resolveSignalContact(a.store, sender)
				}
			}
		}

		result = append(result, entry)
	}
	return result
}

// resolveSignalContact resolves a phone number to a contact name by
// checking TEL and IMPP properties with both raw and +-prefixed forms.
func resolveSignalContact(store *contacts.Store, phone string) string {
	candidates := []string{phone}
	if !strings.HasPrefix(phone, "+") {
		candidates = append(candidates, "+"+phone)
	}
	for _, p := range candidates {
		if matches, err := store.FindByPropertyExact("TEL", p); err == nil && len(matches) > 0 {
			return matches[0].FormattedName
		}
	}
	for _, p := range candidates {
		if matches, err := store.FindByPropertyExact("IMPP", "signal:"+p); err == nil && len(matches) > 0 {
			return matches[0].FormattedName
		}
	}
	return ""
}

// logQueryAdapter bridges the web package's [web.LogQuerier] interface
// to the [logging.Query] function, keeping the web package decoupled
// from database/sql.
type logQueryAdapter struct {
	db *sql.DB
}

// Query delegates to [logging.Query].
func (a *logQueryAdapter) Query(params logging.QueryParams) ([]logging.LogEntry, error) {
	return logging.Query(a.db, params)
}

// contentQueryAdapter bridges the web package's [web.ContentQuerier]
// interface to [logging.QueryRequestDetail].
type contentQueryAdapter struct {
	db *sql.DB
}

// QueryRequestDetail delegates to [logging.QueryRequestDetail].
func (a *contentQueryAdapter) QueryRequestDetail(requestID string) (*logging.RequestDetail, error) {
	return logging.QueryRequestDetail(a.db, requestID)
}

// fallbackContentQuerier checks the primary source first, then falls back
// to a secondary querier when the primary has no matching request detail.
type fallbackContentQuerier struct {
	primary  web.ContentQuerier
	fallback web.ContentQuerier
}

func (q *fallbackContentQuerier) QueryRequestDetail(requestID string) (*logging.RequestDetail, error) {
	if q.primary != nil {
		detail, err := q.primary.QueryRequestDetail(requestID)
		if err != nil || detail != nil {
			return detail, err
		}
	}
	if q.fallback == nil {
		return nil, nil
	}
	return q.fallback.QueryRequestDetail(requestID)
}

// systemStatusAdapter bridges [connwatch.Manager] and [buildinfo] to the
// web package's [web.SystemStatusProvider] interface, keeping the web
// package decoupled from connwatch and buildinfo.
//
// capSurface is a getter rather than a captured slice so the adapter
// sees the current value at call time. The capability surface is
// finalized in a late init phase (after initServers), so any consumer
// that captured the slice at construction time would see an empty
// snapshot.
type systemStatusAdapter struct {
	connMgr       *connwatch.Manager
	modelRegistry *fleet.Registry
	router        *router.Router
	capSurface    func() []toolcatalog.CapabilitySurface
}

// Health returns the health state of all watched services.
func (a *systemStatusAdapter) Health() map[string]web.ServiceHealth {
	status := a.connMgr.Status()
	result := make(map[string]web.ServiceHealth, len(status))
	for name, s := range status {
		h := web.ServiceHealth{
			Name:      s.Name,
			Ready:     s.Ready,
			LastError: s.LastError,
		}
		if !s.LastCheck.IsZero() {
			h.LastCheck = s.LastCheck.Format(time.RFC3339)
		}
		result[name] = h
	}
	return result
}

// Uptime returns how long the process has been running.
func (a *systemStatusAdapter) Uptime() time.Duration {
	return buildinfo.Uptime()
}

// Version returns build and runtime metadata.
func (a *systemStatusAdapter) Version() map[string]string {
	return buildinfo.RuntimeInfo()
}

// ModelRegistry returns the current effective model-registry snapshot.
func (a *systemStatusAdapter) ModelRegistry() *fleet.RegistrySnapshot {
	if a.modelRegistry == nil {
		return nil
	}
	return a.modelRegistry.Snapshot()
}

// RouterStats returns the current router statistics snapshot.
func (a *systemStatusAdapter) RouterStats() *router.Stats {
	if a.router == nil {
		return nil
	}
	stats := a.router.GetStats()
	return &stats
}

// CapabilityCatalog returns the runtime capability catalog used by the
// agent prompt/tooling layer.
func (a *systemStatusAdapter) CapabilityCatalog() *toolcatalog.CapabilityCatalogView {
	var surface []toolcatalog.CapabilitySurface
	if a.capSurface != nil {
		surface = a.capSurface()
	}
	if len(surface) == 0 {
		return nil
	}
	view := toolcatalog.BuildCapabilityCatalogView(surface, true)
	return &view
}

// loopAdapter bridges [looppkg.Runner] to [*agent.Loop], converting
// between the loop package's request/response types and the agent
// package's types. It lives in internal/app to avoid a circular import
// between the loop and agent packages.
//
// capSurface is a getter so the adapter resolves the current capability
// surface at call time rather than capturing a stale snapshot during
// construction. Adapters can outlive the finalization of the surface.
type loopAdapter struct {
	agentLoop  *agent.Loop
	router     *router.Router
	capSurface func() []toolcatalog.CapabilitySurface
}

// capSurfaceSnapshot resolves the current capability surface via the
// getter, returning nil when no getter is configured. Takes the
// snapshot once per Run so all references inside the request use a
// consistent view even if the surface is concurrently re-finalized.
func (a *loopAdapter) capSurfaceSnapshot() []toolcatalog.CapabilitySurface {
	if a.capSurface == nil {
		return nil
	}
	return a.capSurface()
}

// maxToolResultLen is the maximum tool result length forwarded to the
// dashboard via SSE. Results longer than this are truncated with an
// ellipsis to keep event payloads bounded.
const maxToolResultLen = 2000

// Run converts a [looppkg.Request] to [agent.Request], calls the agent
// loop, and converts the result back to [looppkg.Response].
func (a *loopAdapter) Run(ctx context.Context, req looppkg.Request, _ looppkg.StreamCallback) (*looppkg.Response, error) {
	agentReq := compileLoopAgentRequest(req)

	// Build an agent streaming callback that relays tool and LLM
	// events through the loop's OnProgress callback.
	capSurface := a.capSurfaceSnapshot()

	var agentStream agent.StreamCallback
	if req.OnProgress != nil {
		agentStream = func(e agent.StreamEvent) {
			switch e.Kind {
			case agent.KindLLMStart:
				if e.Response != nil {
					activeTags, _ := e.Data["active_tags"].([]string)
					effectiveTools, _ := e.Data["effective_tools"].([]string)
					loadedCapabilities := toolcatalog.BuildLoadedCapabilityEntries(capSurface, activeTags)
					data := map[string]any{
						"model": e.Response.Model,
						"tooling": looppkg.BuildToolingState(
							nil,
							activeTags,
							effectiveTools,
							nil,
							loadedCapabilities,
							nil,
						),
					}
					// Forward enrichment data from agent (tokens, tools, router).
					for k, v := range e.Data {
						data[k] = v
					}
					req.OnProgress(events.KindLoopLLMStart, data)
				}
			case agent.KindToolCallStart:
				if e.ToolCall != nil {
					data := map[string]any{
						"tool": e.ToolCall.Function.Name,
					}
					if len(e.ToolCall.Function.Arguments) > 0 {
						data["args"] = e.ToolCall.Function.Arguments
					}
					req.OnProgress(events.KindLoopToolStart, data)
				}
			case agent.KindToolCallDone:
				data := map[string]any{"tool": e.ToolName}
				if e.ToolError != "" {
					data["error"] = e.ToolError
				}
				if e.ToolResult != "" {
					r := e.ToolResult
					if len(r) > maxToolResultLen {
						r = r[:maxToolResultLen] + "…"
					}
					data["result"] = r
				}
				activeTags, _ := e.Data["active_tags"].([]string)
				effectiveTools, _ := e.Data["effective_tools"].([]string)
				if len(activeTags) > 0 || len(effectiveTools) > 0 {
					loadedCapabilities := toolcatalog.BuildLoadedCapabilityEntries(capSurface, activeTags)
					data["active_tags"] = append([]string(nil), activeTags...)
					data["effective_tools"] = append([]string(nil), effectiveTools...)
					data["tooling"] = looppkg.BuildToolingState(
						nil,
						activeTags,
						effectiveTools,
						nil,
						loadedCapabilities,
						nil,
					)
				}
				req.OnProgress(events.KindLoopToolDone, data)
			case agent.KindLLMResponse:
				if e.Response != nil {
					data := map[string]any{
						"model":         e.Response.Model,
						"input_tokens":  e.Response.InputTokens,
						"output_tokens": e.Response.OutputTokens,
					}
					for k, v := range e.Data {
						data[k] = v
					}
					req.OnProgress(events.KindLoopLLMResponse, data)
				}
			}
		}
	}

	resp, err := a.agentLoop.Run(ctx, agentReq, agentStream)
	if err != nil {
		return nil, err
	}

	loadedCapabilities := append([]toolcatalog.LoadedCapabilityEntry(nil), resp.LoadedCapabilities...)
	if len(loadedCapabilities) == 0 {
		loadedCapabilities = toolcatalog.BuildLoadedCapabilityEntries(capSurface, resp.ActiveTags)
	}

	// Use the routed model's context window if available, otherwise
	// fall back to the agent loop's default.
	ctxWindow := a.agentLoop.GetContextWindow()
	if a.router != nil && resp.Model != "" {
		if mw := a.router.ContextWindowForModel(resp.Model); mw > 0 {
			ctxWindow = mw
		}
	}

	return &looppkg.Response{
		Content:                  resp.Content,
		Model:                    resp.Model,
		FinishReason:             resp.FinishReason,
		InputTokens:              resp.InputTokens,
		OutputTokens:             resp.OutputTokens,
		CacheCreationInputTokens: resp.CacheCreationInputTokens,
		CacheReadInputTokens:     resp.CacheReadInputTokens,
		ContextWindow:            ctxWindow,
		ToolsUsed:                resp.ToolsUsed,
		EffectiveTools:           append([]string(nil), resp.EffectiveTools...),
		LoadedCapabilities:       append([]toolcatalog.LoadedCapabilityEntry(nil), loadedCapabilities...),
		RequestID:                resp.RequestID,
		Iterations:               resp.Iterations,
		Exhausted:                resp.Exhausted,
		ActiveTags:               resp.ActiveTags,
	}, nil
}

func compileLoopAgentRequest(req looppkg.Request) *agent.Request {
	// Convert messages.
	msgs := make([]agent.Message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = agent.Message{Role: m.Role, Content: m.Content}
	}

	return &agent.Request{
		Model:           req.Model,
		ConversationID:  req.ConversationID,
		ChannelBinding:  req.ChannelBinding.Clone(),
		Messages:        msgs,
		SkipContext:     req.SkipContext,
		AllowedTools:    append([]string(nil), req.AllowedTools...),
		ExcludeTools:    append([]string(nil), req.ExcludeTools...),
		SkipTagFilter:   req.SkipTagFilter,
		Hints:           cloneStringMap(req.Hints),
		InitialTags:     append([]string(nil), req.InitialTags...),
		RuntimeTools:    compileLoopRuntimeTools(req.RuntimeTools),
		MaxIterations:   req.MaxIterations,
		MaxOutputTokens: req.MaxOutputTokens,
		ToolTimeout:     req.ToolTimeout,
		UsageRole:       req.UsageRole,
		UsageTaskName:   req.UsageTaskName,
		SystemPrompt:    req.SystemPrompt,
	}
}

func compileLoopRuntimeTools(src []looppkg.RuntimeTool) []*tools.Tool {
	if len(src) == 0 {
		return nil
	}
	out := make([]*tools.Tool, 0, len(src))
	for _, t := range src {
		if strings.TrimSpace(t.Name) == "" || t.Handler == nil {
			continue
		}
		out = append(out, &tools.Tool{
			Name:                 t.Name,
			Description:          t.Description,
			Parameters:           t.Parameters,
			Handler:              t.Handler,
			AlwaysAvailable:      true,
			SkipContentResolve:   t.SkipContentResolve,
			ContentResolveExempt: append([]string(nil), t.ContentResolveExempt...),
			Source:               string(toolcatalog.NativeToolSource),
		})
	}
	return out
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
