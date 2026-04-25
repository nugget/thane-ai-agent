package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
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
	// category=channel metadata. The returned slice must be in a
	// deterministic order (e.g., sorted by name) to ensure prompt
	// stability across turns.
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
	// Signal sets "source"; OWU/Ollama sets "channel". Read both.
	var currentSource, currentSender, currentSenderName string
	if p.hintsFunc != nil {
		if hints := p.hintsFunc(ctx); hints != nil {
			currentSource = hints["source"]
			if currentSource == "" {
				currentSource = hints["channel"]
			}
			currentSender = hints["sender"]
			currentSenderName = hints["sender_name"]
		}
	}
	// Normalize OWU's "ollama" channel hint to match loop metadata.
	if currentSource == "ollama" {
		currentSource = "owu"
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
			LoopID:  promptfmt.ShortIDPrefix(l.ID),
		}

		// Skip parent loops (no per-conversation identity).
		switch subsystem {
		case "signal":
			if l.Metadata["sender"] == "" {
				continue
			}
		case "owu":
			if l.Metadata["conversation_id"] == "" {
				continue
			}
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
			// Resolve phone to contact name and authoritative trust zone.
			if p.phones != nil && sender != "" {
				if name, tz, ok := p.phones.ResolvePhone(sender); ok {
					e.Contact = name
					if tz != "" {
						e.TrustZone = tz
					}
				}
			}
		case "owu":
			e.ConvID = l.Metadata["conversation_id"]
			e.DisplayName = cleanLoopName(l.Name)
		default:
			e.ConvID = l.Metadata["conversation_id"]
		}

		// Annotate the channel this request arrived on. Per-sender
		// channels (signal) require a sender match to avoid marking
		// all sender loops; other channels match on subsystem alone.
		if currentSource == subsystem {
			if e.Sender != "" {
				// Per-sender channel: match by raw phone, resolved name,
				// or sender_name hint (whichever is available).
				if e.Sender == currentSender || e.Sender == currentSenderName || e.Contact == currentSenderName {
					e.YouAreHere = true
				}
			} else {
				e.YouAreHere = true
			}
		}

		// Exact-second delta per issue #458.
		if !l.LastWakeAt.IsZero() {
			e.LastActivity = promptfmt.FormatDeltaOnly(l.LastWakeAt, now)
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

// cleanLoopName strips the "owu/" prefix from OWU loop names to get
// the display name.
func cleanLoopName(name string) string {
	return strings.TrimPrefix(name, "owu/")
}
