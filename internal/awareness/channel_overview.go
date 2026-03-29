package awareness

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// LoopSnapshot is a subset of loop.Status relevant to channel overview.
// Defined here to avoid importing the loop package.
type LoopSnapshot struct {
	ID            string
	Name          string
	State         string
	LastWakeAt    time.Time
	Metadata      map[string]string
	RecentConvIDs []string
}

// ChannelLoopSource provides snapshots of channel-category loops.
type ChannelLoopSource interface {
	// ChannelLoops returns status snapshots for loops with
	// category=channel metadata.
	ChannelLoops() []LoopSnapshot
}

// PhoneResolver maps phone numbers to contact names and trust zones.
type PhoneResolver interface {
	ResolvePhone(phone string) (name string, trustZone string, ok bool)
}

// HintsFunc extracts routing hints from a context. The caller provides
// this to bridge the awareness package to the tools context without a
// direct import dependency.
type HintsFunc func(ctx context.Context) map[string]string

// ChannelOverviewProvider injects a compact JSON summary of active
// communication channels into the system prompt. The agent uses this
// to understand the current communication landscape — who's reachable,
// on which channel, and how recently they were active.
type ChannelOverviewProvider struct {
	loops     ChannelLoopSource
	phones    PhoneResolver // nil disables phone→name resolution
	hintsFunc HintsFunc     // nil disables you_are_here annotation
	nowFunc   func() time.Time
	logger    *slog.Logger
}

// ChannelOverviewConfig holds dependencies for [NewChannelOverviewProvider].
type ChannelOverviewConfig struct {
	Loops  ChannelLoopSource
	Phones PhoneResolver // optional
	Hints  HintsFunc     // optional; extracts routing hints from ctx
	Logger *slog.Logger  // optional; defaults to slog.Default
}

// NewChannelOverviewProvider creates a provider that aggregates channel
// state from the loop registry and contact store.
func NewChannelOverviewProvider(cfg ChannelOverviewConfig) *ChannelOverviewProvider {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &ChannelOverviewProvider{
		loops:     cfg.Loops,
		phones:    cfg.Phones,
		hintsFunc: cfg.Hints,
		nowFunc:   time.Now,
		logger:    logger,
	}
}

// channelEntry is the JSON structure for a single channel in the overview.
type channelEntry struct {
	Channel      string `json:"channel"`
	Contact      string `json:"contact,omitempty"`
	Sender       string `json:"sender,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	TrustZone    string `json:"trust_zone,omitempty"`
	State        string `json:"state"`
	LastActivity string `json:"last_activity,omitempty"`
	LoopID       string `json:"loop_id,omitempty"`
	ConvID       string `json:"conv_id,omitempty"`
	YouAreHere   bool   `json:"you_are_here,omitempty"`
}

// GetContext returns a compact JSON channel overview for injection into
// the system prompt. Returns empty string when no channel loops exist.
func (p *ChannelOverviewProvider) GetContext(ctx context.Context, _ string) (string, error) {
	if p.loops == nil {
		return "", nil
	}

	loops := p.loops.ChannelLoops()
	if len(loops) == 0 {
		return "", nil
	}

	now := p.nowFunc()

	// Determine the requesting channel's source hint for annotation.
	var currentSource, currentSenderName string
	if p.hintsFunc != nil {
		if hints := p.hintsFunc(ctx); hints != nil {
			currentSource = hints["source"]
			currentSenderName = hints["sender_name"]
		}
	}

	var entries []channelEntry
	for _, l := range loops {
		subsystem := l.Metadata["subsystem"]
		if subsystem == "" {
			continue
		}

		e := channelEntry{
			Channel: subsystem,
			State:   l.State,
			LoopID:  shortID(l.ID),
		}

		// Channel-specific field extraction.
		switch subsystem {
		case "signal":
			sender := l.Metadata["sender"]
			e.Sender = sender
			e.TrustZone = l.Metadata["trust_zone"]
			if len(l.RecentConvIDs) > 0 {
				e.ConvID = l.RecentConvIDs[0]
			}
			// Resolve phone to contact name.
			if p.phones != nil && sender != "" {
				if name, _, ok := p.phones.ResolvePhone(sender); ok {
					e.Contact = name
				}
			}
			// Match requesting channel by sender_name hint.
			if currentSource == "signal" && currentSenderName != "" && e.Contact == currentSenderName {
				e.YouAreHere = true
			}
		case "owu":
			e.ConvID = l.Metadata["conversation_id"]
			e.DisplayName = cleanLoopName(l.Name)
			// OWU loops don't have per-sender identity in hints.
			if currentSource == "owu" {
				e.YouAreHere = true
			}
		default:
			e.ConvID = l.Metadata["conversation_id"]
		}

		// Exact-second delta per issue #458.
		if !l.LastWakeAt.IsZero() {
			e.LastActivity = FormatDeltaOnly(l.LastWakeAt, now)
		}

		entries = append(entries, e)
	}

	if len(entries) == 0 {
		return "", nil
	}

	data, err := json.Marshal(entries)
	if err != nil {
		p.logger.Warn("failed to marshal channel overview", "error", err)
		return "", nil
	}

	return "### Channel Overview\n\n" + string(data) + "\n", nil
}

// shortID returns the first 8 characters of an ID for compact display.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// cleanLoopName strips the "owu/" prefix from OWU loop names to get
// the display name.
func cleanLoopName(name string) string {
	return strings.TrimPrefix(name, "owu/")
}
