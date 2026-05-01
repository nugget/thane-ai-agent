package messages

import (
	"fmt"
	"strings"
)

// ReactionEvent is a channel-neutral description of a reaction observed on
// an interactive message transport.
type ReactionEvent struct {
	// ChannelName is the human-facing channel label used in model context.
	ChannelName string
	// SenderID is the stable channel address that produced the reaction.
	SenderID string
	// SenderName is the optional display name supplied by the channel.
	SenderName string
	// Emoji is the reaction content.
	Emoji string
	// TargetAuthor identifies the author of the message being reacted to.
	TargetAuthor string
	// TargetTimestamp is the channel-native timestamp of the target message.
	TargetTimestamp int64
	// Removed reports that this event removed a prior reaction.
	Removed bool
}

// SenderLabel returns the display name and stable address in a compact form
// suitable for model-facing context.
func (e ReactionEvent) SenderLabel() string {
	name := strings.TrimSpace(e.SenderName)
	id := strings.TrimSpace(e.SenderID)
	if name != "" && id != "" {
		return fmt.Sprintf("%s (%s)", name, id)
	}
	if name != "" {
		return name
	}
	if id == "" {
		return "unknown sender"
	}
	return id
}

// Prompt renders the reaction as a concise model-facing message.
func (e ReactionEvent) Prompt() string {
	channel := strings.TrimSpace(e.ChannelName)
	if channel == "" {
		channel = "Message"
	}
	return fmt.Sprintf("%s reaction from %s: %s on message [ts:%d] from %s",
		channel,
		e.SenderLabel(),
		e.Emoji,
		e.TargetTimestamp,
		e.TargetAuthor,
	)
}

// Hints returns routing hints common to reaction-triggered model turns.
func (e ReactionEvent) Hints() map[string]string {
	eventType := "reaction"
	if e.Removed {
		eventType = "reaction_removed"
	}
	return map[string]string{
		"event_type":            eventType,
		"reaction_emoji":        e.Emoji,
		"target_sent_timestamp": fmt.Sprintf("%d", e.TargetTimestamp),
	}
}
