package promptfmt

import (
	"fmt"
	"strings"
	"time"
)

// This file renders conversational rhythm as interpreted narrative for
// interactive channels: where the current turn stands relative to the
// previous contact with the channel counterparty, with all bucketing done
// here so the model never does timestamp arithmetic or day-boundary
// inference. Prose is deliberate — once the interpretation is complete
// there is no structure left to convey, and a gap-class enum would hand
// the interpretation work back to the model (the "prose is genuinely the
// clearer contract" case in docs/model-facing-context.md). The precise
// deltas stay in the Older Sessions catalog rendered alongside; the
// sentence is a different altitude over the same timestamps.
//
// Vocabulary contract (used here and by every consumer of these facts):
// the CURRENT TURN is what the loop is processing now — either a contact
// message or a wake; CONTACT is communication with the channel
// counterparty in either direction; a WAKE is an internally-originated
// turn; the PREVIOUS CONTACT is the most recent contact before the
// current turn, which wakes never move. With a current-turn anchor in
// frame the term is always "previous", never "last" — "last message" is
// ambiguous the moment a just-arrived message exists.

// TurnKind classifies how the current turn originated.
type TurnKind int

const (
	// TurnContact is a turn opened by a message from the channel
	// counterparty.
	TurnContact TurnKind = iota
	// TurnWake is a turn opened internally — loop_wake, a subscription
	// fire, a scheduled trigger — with no new message from the
	// counterparty.
	TurnWake
)

// ConversationRecencyFacts are the mechanical inputs to
// [ConversationRecency]. Zero values mean "not known": callers pass what
// their stores can state truthfully and the renderer degrades clause by
// clause rather than guessing.
type ConversationRecencyFacts struct {
	// Kind is how the current turn originated.
	Kind TurnKind

	// WakeLoopName optionally names the loop behind a TurnWake turn,
	// for "an internal wake from the X loop". Empty renders "an
	// internal wake".
	WakeLoopName string

	// PreviousContactAt is when the previous contact occurred — the
	// most recent contact-classified message before the current turn.
	// Zero means no previous contact is known.
	PreviousContactAt time.Time

	// ActiveSessionStart is when the currently-open archive session
	// began. Zero means no active session. Only rendered inside the
	// active-conversation branch, where it is necessarily
	// recent-anchored: session boundaries are activity-based and wake
	// rows keep sessions open, so rhythm always gates on contact
	// timestamps, never on session boundaries.
	ActiveSessionStart time.Time

	// PreviousSessionEnd is when the most recent closed session with
	// content ended. Zero means none known.
	PreviousSessionEnd time.Time

	// PreviousSessionTopic is the archivist's summary of that session
	// (one-liner or title). Empty degrades the clause to the time word
	// alone — the archivist may simply not have run yet.
	PreviousSessionTopic string

	// HasHistory reports whether this conversation has any recorded
	// past at all (prior sessions or contact). Distinguishes "first
	// conversation ever" from "history exists but contact timing is
	// not recorded".
	HasHistory bool

	// RecentWindow is the active/resumed boundary: a contact gap at or
	// under it reads as an ongoing conversation. Zero defaults to 30
	// minutes, matching the message-channel provider's catalog window.
	RecentWindow time.Duration
}

// defaultRecentWindow mirrors MessageChannelProviderConfig's default so
// the "active conversation" boundary and the catalog's recent-window
// exclusion describe the same span when neither is configured.
const defaultRecentWindow = 30 * time.Minute

// lullCeiling bounds the "resuming after a quiet stretch" bucket: a gap
// past the recent window but under this still reads as one conversation
// pausing, not a new contact.
const lullCeiling = 2 * time.Hour

// ConversationRecency renders the conversational rhythm of the current
// turn as one or two plain sentences, or "" when there is nothing
// truthful to say. now anchors all deltas; loc is the household timezone
// that anchors day words ("last night", "yesterday") — pass the
// configured zone, never the server's.
func ConversationRecency(f ConversationRecencyFacts, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	window := f.RecentWindow
	if window <= 0 {
		window = defaultRecentWindow
	}

	var gap time.Duration
	if !f.PreviousContactAt.IsZero() {
		gap = now.Sub(f.PreviousContactAt)
		if gap < 0 {
			gap = 0
		}
	}

	rhythm, active := rhythmClause(f, now, loc, window, gap)

	// The previous-conversation clause orients a fresh contact inside
	// past experience. Inside an ongoing conversation it is noise (the
	// working message list already carries the context), and a session
	// that ended within the recent window is likewise still in the
	// working list.
	var prevConv string
	if !active && !f.PreviousSessionEnd.IsZero() && now.Sub(f.PreviousSessionEnd) > window {
		prevConv = previousConversationClause(f.PreviousSessionEnd, f.PreviousSessionTopic, now, loc)
	}

	switch {
	case rhythm == "":
		return prevConv
	case prevConv == "":
		return rhythm
	default:
		return rhythm + " " + prevConv
	}
}

// rhythmClause renders the always-present clause describing where the
// current turn stands, and reports whether the conversation is in the
// active/continuing state (which suppresses the previous-conversation
// clause).
func rhythmClause(f ConversationRecencyFacts, now time.Time, loc *time.Location, window time.Duration, gap time.Duration) (string, bool) {
	wake := f.Kind == TurnWake
	wakePhrase := "an internal wake"
	if f.WakeLoopName != "" {
		wakePhrase = "an internal wake from the " + f.WakeLoopName + " loop"
	}

	if f.PreviousContactAt.IsZero() {
		switch {
		case wake && !f.HasHistory:
			return "This turn is " + wakePhrase + "; there has been no contact on this channel yet.", false
		case wake:
			return "This turn is " + wakePhrase + ", not a message from them.", false
		case !f.HasHistory:
			return "This is the first conversation on this channel.", false
		default:
			// History exists but no contact time is recorded —
			// say nothing rather than guess; the
			// previous-conversation clause may still render.
			return "", false
		}
	}

	if wake {
		if gap <= window {
			return fmt.Sprintf(
				"You're in an active conversation (previous contact %s ago), but this turn is %s, not a new message — they haven't said anything new.",
				fuzzySpan(gap), wakePhrase), true
		}
		return fmt.Sprintf("This turn is %s, not a message from them. The previous contact was %s.",
			wakePhrase, agoPhrase(f.PreviousContactAt, now, loc)), false
	}

	switch {
	case gap <= window:
		if !f.ActiveSessionStart.IsZero() {
			return fmt.Sprintf(
				"You're in an active conversation that's been going on for %s; the previous message was %s before this one.",
				fuzzySpan(now.Sub(f.ActiveSessionStart)), fuzzySpan(gap)), true
		}
		return fmt.Sprintf("The conversation is continuing; the previous message was %s ago.", fuzzySpan(gap)), true
	case gap <= lullCeiling:
		return fmt.Sprintf("The conversation is resuming after a quiet stretch — the previous message was %s ago.", fuzzySpan(gap)), false
	case sameDay(f.PreviousContactAt, now, loc):
		return fmt.Sprintf("This is the first message in %s.", fuzzySpan(gap)), false
	default:
		return "This is the first contact today.", false
	}
}

// previousConversationClause renders "The previous conversation was last
// night, about the garage lighting circuits." — time word from day
// bucketing, topic from the archivist when available.
func previousConversationClause(end time.Time, topic string, now time.Time, loc *time.Location) string {
	var sb strings.Builder
	sb.WriteString("The previous conversation was ")
	sb.WriteString(dayPhrase(end, now, loc))
	if topic != "" {
		sb.WriteString(", about ")
		sb.WriteString(strings.TrimRight(topic, "."))
	}
	sb.WriteString(".")
	return sb.String()
}

// agoPhrase renders a past instant relative to now: same-day gaps as a
// fuzzy span ("about 3 hours ago"), older ones as a day word ("last
// night", "yesterday", "on Tuesday").
func agoPhrase(t, now time.Time, loc *time.Location) string {
	if sameDay(t, now, loc) {
		return fuzzySpan(now.Sub(t)) + " ago"
	}
	return dayPhrase(t, now, loc)
}

// fuzzySpan renders a duration in conversational units: "moments",
// "about 5 minutes", "about an hour", "about 6 hours", "about 3 days".
// Callers supply the surrounding grammar ("ago", "before this one",
// "going on for").
func fuzzySpan(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return "moments"
	case d < 60*time.Minute:
		m := int((d + 30*time.Second) / time.Minute)
		if m < 2 {
			m = 2
		}
		return fmt.Sprintf("about %d minutes", m)
	case d < 100*time.Minute:
		return "about an hour"
	case d < 24*time.Hour:
		h := int((d + 30*time.Minute) / time.Hour)
		return fmt.Sprintf("about %d hours", h)
	default:
		days := int((d + 12*time.Hour) / (24 * time.Hour))
		if days == 1 {
			return "about a day"
		}
		return fmt.Sprintf("about %d days", days)
	}
}

// dayPhrase buckets a past instant into household-calendar words:
// "earlier today", "last night", "yesterday", a weekday name inside the
// week, "N weeks ago" inside the month, and an absolute date beyond.
func dayPhrase(t, now time.Time, loc *time.Location) string {
	t = t.In(loc)
	now = now.In(loc)

	// "Last night": the instant fell in yesterday-evening-through-
	// early-today (17:00 to 04:00) and it is still morning — after
	// noon, "yesterday" (or "earlier today" for the small-hours half)
	// reads more naturally than "last night". Checked before the
	// same-day bucket so a 02:00 session viewed at 09:00 renders "last
	// night", which is what that conversation was.
	if now.Hour() < 12 && inLastNightBand(t, now) {
		return "last night"
	}

	if sameDay(t, now, loc) {
		return "earlier today"
	}

	days := calendarDaysBetween(t, now)
	switch {
	case days <= 1:
		return "yesterday"
	case days <= 6:
		return "on " + t.Weekday().String()
	case days <= 27:
		weeks := days / 7
		if weeks == 1 {
			return "a week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	default:
		if t.Year() == now.Year() {
			return "on " + t.Format("January 2")
		}
		return "on " + t.Format("January 2, 2006")
	}
}

// inLastNightBand reports whether t falls between 17:00 yesterday and
// 04:00 today, in the timezone both t and now are already expressed in.
func inLastNightBand(t, now time.Time) bool {
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	bandStart := todayStart.Add(-7 * time.Hour) // 17:00 yesterday
	bandEnd := todayStart.Add(4 * time.Hour)    // 04:00 today
	return !t.Before(bandStart) && t.Before(bandEnd)
}

// sameDay reports whether two instants fall on the same calendar day in
// loc.
func sameDay(a, b time.Time, loc *time.Location) bool {
	a, b = a.In(loc), b.In(loc)
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// calendarDaysBetween counts whole calendar-day boundaries crossed
// between t and now (both already in the same location). Rounded rather
// than truncated so a 23-hour DST-transition day still counts as one day.
func calendarDaysBetween(t, now time.Time) int {
	tDay := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return int(nowDay.Sub(tDay).Hours()/24 + 0.5)
}
