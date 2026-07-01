package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const docRootSyncTransitionKind = "document_root_sync_transition"

func (a *App) docRootSyncAttentionNotifier() syncTransitionNotifier {
	if a == nil || a.messageBus == nil || a.loopRegistry == nil {
		return nil
	}
	return a.notifyDocRootSyncTransition
}

func (a *App) notifyDocRootSyncTransition(ctx context.Context, transition syncStateTransition) error {
	_, err := looppkg.WakeCoreLoop(ctx, a.loopRegistry, a.messageBus, docRootSyncTransitionWake(transition))
	return err
}

func docRootSyncTransitionWake(transition syncStateTransition) looppkg.CoreWakeRequest {
	priority := messages.PriorityNormal
	forceSupervisor := false
	if transition.Kind == syncTransitionAttentionRequired {
		priority = messages.PriorityUrgent
		forceSupervisor = true
	}

	return looppkg.CoreWakeRequest{
		From: messages.Identity{
			Kind: messages.IdentitySystem,
			Name: "document_root_syncer",
		},
		Kind:            docRootSyncTransitionKind,
		Concern:         docRootSyncTransitionConcern(transition),
		SuggestedAction: docRootSyncTransitionAction(transition),
		Events:          []messages.LoopEventPayload{docRootSyncTransitionEvent(transition)},
		Priority:        priority,
		Scope:           []string{"document_root_sync"},
		ForceSupervisor: forceSupervisor,
	}
}

func docRootSyncTransitionEnvelope(target looppkg.CoreAttentionTarget, transition syncStateTransition) messages.Envelope {
	return looppkg.CoreWakeEnvelope(target, docRootSyncTransitionWake(transition))
}

func docRootSyncTransitionEvent(transition syncStateTransition) messages.LoopEventPayload {
	st := transition.Current
	return messages.LoopEventPayload{
		Source:     "document_root_sync",
		Type:       string(transition.Kind),
		ID:         docRootSyncTransitionID(transition),
		Title:      docRootSyncTransitionTitle(transition),
		Summary:    docRootSyncTransitionSummary(transition),
		ObservedAt: st.LastSyncAt,
		Metadata:   docRootSyncTransitionMetadata(transition),
	}
}

func docRootSyncTransitionConcern(transition syncStateTransition) string {
	st := transition.Current
	switch transition.Kind {
	case syncTransitionAttentionRequired:
		if detail := strings.TrimSpace(st.Detail); detail != "" {
			return fmt.Sprintf("Document root sync requires core review: root %q entered %s: %s", st.Root, st.Outcome, detail)
		}
		return fmt.Sprintf("Document root sync requires core review: root %q entered %s.", st.Root, st.Outcome)
	case syncTransitionRecovered:
		return fmt.Sprintf("Document root sync recovered: root %q is %s after %s.", st.Root, st.Outcome, transition.Previous.Outcome)
	default:
		return fmt.Sprintf("Document root sync changed state for root %q.", st.Root)
	}
}

func docRootSyncTransitionAction(transition syncStateTransition) string {
	switch transition.Kind {
	case syncTransitionAttentionRequired:
		return "Review the refusal state and decide whether to wait for the next self-healing pass or ask the operator to resolve the remote/worktree condition."
	case syncTransitionRecovered:
		return "Note the recovery; no direct human message is required unless other current context makes follow-up useful."
	default:
		return "Review the sync state change."
	}
}

func docRootSyncTransitionID(transition syncStateTransition) string {
	st := transition.Current
	h := sha256.Sum256([]byte(strings.Join([]string{
		st.Root,
		string(transition.Kind),
		string(st.Outcome),
		strings.TrimSpace(st.Detail),
		string(transition.Previous.Outcome),
		strings.TrimSpace(transition.Previous.Detail),
	}, "\x00")))
	return fmt.Sprintf("%s:%s:%s:%x", st.Root, transition.Kind, st.Outcome, h[:8])
}

func docRootSyncTransitionTitle(transition syncStateTransition) string {
	switch transition.Kind {
	case syncTransitionAttentionRequired:
		return "Document root sync attention required"
	case syncTransitionRecovered:
		return "Document root sync recovered"
	default:
		return "Document root sync state changed"
	}
}

func docRootSyncTransitionSummary(transition syncStateTransition) string {
	st := transition.Current
	if st.Detail == "" {
		return fmt.Sprintf("root=%s outcome=%s", st.Root, st.Outcome)
	}
	return fmt.Sprintf("root=%s outcome=%s detail=%s", st.Root, st.Outcome, st.Detail)
}

func docRootSyncTransitionMetadata(transition syncStateTransition) map[string]string {
	st := transition.Current
	meta := map[string]string{
		"root":        st.Root,
		"outcome":     string(st.Outcome),
		"ahead":       strconv.Itoa(st.Ahead),
		"behind":      strconv.Itoa(st.Behind),
		"local_head":  st.LocalHead,
		"remote_head": st.RemoteHead,
	}
	if st.Detail != "" {
		meta["detail"] = st.Detail
	}
	if transition.HasPrevious {
		meta["previous_outcome"] = string(transition.Previous.Outcome)
		if transition.Previous.Detail != "" {
			meta["previous_detail"] = transition.Previous.Detail
		}
	}
	return meta
}
