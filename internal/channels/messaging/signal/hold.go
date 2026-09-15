package signal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// HoldReplyToolName is the runtime tool a Signal loop-notification wake
// turn offers so the model can end the turn without messaging the person
// on the thread. On every other Signal turn a reply is expected, so the
// tool is not offered there.
//
// It exists because "leave the final text empty" cannot express silence:
// the agent engine nudges an empty ending ("Please respond now") and then
// substitutes fallback text, so an absence always became a message. A
// hold is an explicit act the bridge detects directly, the same way it
// detects signal_send_message, before it looks at the final text at all.
const HoldReplyToolName = "signal_hold_reply"

// replyHold records one turn's hold decision. The bridge installs it on
// the run context of a turn that offers the hold tool, and the tool
// handler fills it in, so detection never depends on reading tool
// arguments back out of the runner's response.
type replyHold struct {
	mu     sync.Mutex
	reason string
}

func (h *replyHold) set(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reason = reason
}

// heldReason reports the recorded reason and whether the turn was held.
// A nil hold (a turn that never offered the tool) is never held.
func (h *replyHold) heldReason() (string, bool) {
	if h == nil {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reason, h.reason != ""
}

type replyHoldKey struct{}

func withReplyHold(ctx context.Context, h *replyHold) context.Context {
	return context.WithValue(ctx, replyHoldKey{}, h)
}

func replyHoldFromContext(ctx context.Context) *replyHold {
	h, _ := ctx.Value(replyHoldKey{}).(*replyHold)
	return h
}

// holdReplyRuntimeTool builds the request-scoped hold tool. Runtime tools
// are exempt from tag filtering, so attaching it to the wake turn's
// request is what makes it callable there and nowhere else.
func holdReplyRuntimeTool() loop.RuntimeTool {
	return loop.RuntimeTool{
		Name:        HoldReplyToolName,
		Description: "Hold this turn's Signal message: when the turn ends, nothing is sent to the person on this Signal thread, and any final response text you write is kept in the conversation record instead of being delivered. Offered only when a loop woke you, because on that turn your final text would otherwise reach them word for word. Call it when your determination does not need to reach them right now. It holds only the Signal message: a reply you owe the requesting loop still goes through loop_wake, before or after this call.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Required. One sentence on why nothing should reach them right now, for example \"answered garage-watch with loop_wake; the reading was a sensor glitch with nothing for a person to do\". Logged and stored in the conversation record, never sent.",
				},
			},
			"required": []string{"reason"},
		},
		Handler: handleHoldReply,
	}
}

// holdReplyNext is the hold result's guidance. The loop_wake duty leads
// and ending the turn is conditional on it: the description allows the
// hold before loop_wake, and a model that reads "end the turn" first can
// stop with the requesting loop unanswered — the dropped thread the wake
// prompt exists to prevent. The result carries no signal_message_sent
// field: at call time the handler cannot know what else the turn sends.
const holdReplyNext = "The Signal message is held. If a loop asked you for a determination and you have not sent it back yet, send it now with loop_wake (its reply_to.loop_id); the hold does not send it. Then end the turn with one short line saying what you decided. That line is kept in the conversation record and never sent."

func handleHoldReply(ctx context.Context, args map[string]any) (string, error) {
	reason, _ := args["reason"].(string)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("reason is required (string): say in one sentence why nothing should reach the person on this Signal thread right now, for example \"answered the requesting loop; the reading was a sensor glitch\"; the Signal message is not held until a call with a reason succeeds")
	}
	hold := replyHoldFromContext(ctx)
	if hold == nil {
		return "", fmt.Errorf("%s works only on a Signal turn opened by a loop wake, and this turn is not one; your final response text will be sent as usual", HoldReplyToolName)
	}
	hold.set(reason)
	out, err := json.Marshal(map[string]any{
		"status": "held",
		"reason": reason,
		"next":   holdReplyNext,
	})
	if err != nil {
		return "", fmt.Errorf("marshal %s result: %w", HoldReplyToolName, err)
	}
	return string(out), nil
}

// offersReplyHold reports whether a request carries the hold tool. The
// bridge reads this in its response runner to know the turn is a
// loop-notification wake, whose reply rules differ from a contact turn.
func offersReplyHold(tools []loop.RuntimeTool) bool {
	for _, t := range tools {
		if t.Name == HoldReplyToolName {
			return true
		}
	}
	return false
}

// finishReasonTimeoutRecovery is the agent's FinishReason for a turn
// whose final text the runtime wrote after the model call timed out: the
// static timeout notice, or a recovery model's summary of the work. The
// signal package does not import the agent, so the value is mirrored
// here; TestBridge_WakeTimeoutRecoveryThroughAgent pins it against the
// real agent.
const finishReasonTimeoutRecovery = "timeout_recovery"

// runtimeWroteFinalText reports whether a turn's final text came from
// the runtime rather than from the model deciding what the thread should
// see: a placeholder isRuntimeFallback recognises, or any text on a
// timeout-recovered turn. A recovery model's summary is written for a
// user waiting on an answer, and no one on a wake turn is.
func runtimeWroteFinalText(resp *loop.Response, requestFallback string) bool {
	return resp.FinishReason == finishReasonTimeoutRecovery || isRuntimeFallback(resp.Content, requestFallback)
}

// isRuntimeFallback reports whether content is one of the runtime's own
// placeholders rather than text the model wrote: an empty-response
// fallback or a timeout notice. On a wake turn such a placeholder ("...
// Please try again.") answers a request nobody made, so it must not be
// sent. Fixed strings compare exactly. The one formatted notice,
// prompts.TimeoutRecoveryFallback, matches on the text around its verbs.
func isRuntimeFallback(content, requestFallback string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	for _, fallback := range []string{requestFallback, prompts.InteractiveEmptyResponseFallback, prompts.EmptyResponseFallback, prompts.TimeoutRecoveryEmpty} {
		if fallback != "" && content == strings.TrimSpace(fallback) {
			return true
		}
	}
	return matchesFormatFrame(content, prompts.TimeoutRecoveryFallback)
}

// matchesFormatFrame reports whether content has the frame of format as
// fmt renders it: the text before the first verb and the text after the
// last one. Verbs are assumed to be one letter, as in %d and %s.
func matchesFormatFrame(content, format string) bool {
	first := strings.IndexByte(format, '%')
	last := strings.LastIndexByte(format, '%')
	if first < 0 || last+2 > len(format) {
		return false
	}
	prefix, suffix := format[:first], format[last+2:]
	return len(content) >= len(prefix)+len(suffix) &&
		strings.HasPrefix(content, prefix) &&
		strings.HasSuffix(content, suffix)
}

// Note kinds persisted into the Signal conversation when a wake turn
// ends without sending anything.
const (
	replyNoteHeld    = "signal_reply_held"
	replyNoteNotSent = "signal_reply_not_sent"
)

// signalReplyNote is the system-authored record of a wake turn whose
// final text was not sent to the person on the thread. It is stored as a
// system row, which later turns read as a framed memory note rather than
// as something said on Signal, so the history shows a deliberate hold
// instead of an unexplained gap, and an unsent final text row above it
// is not mistaken for a delivered one.
type signalReplyNote struct {
	Kind string `json:"kind"`
	// SignalMessageSent reports whether the turn delivered a message
	// itself with signal_send_message. It is read from the turn's tool
	// tally, so it is absent when the run failed and returned none.
	SignalMessageSent *bool `json:"signal_message_sent,omitempty"`
	// RunFailed is true when the agent run ended in an error after the
	// model held the reply.
	RunFailed bool `json:"run_failed,omitempty"`
	// Reason is the model's own words, set only for a hold.
	Reason string `json:"reason,omitempty"`
	// Cause is the runtime's explanation, set only when the turn sent
	// nothing without a hold.
	Cause string `json:"cause,omitempty"`
	// FinalTextWithheld is true when the turn ended with final text
	// (stored in the conversation by the agent) that was not sent.
	FinalTextWithheld bool `json:"final_text_withheld"`
}
