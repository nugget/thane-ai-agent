package documents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Intake analyzes where new knowledge should land in a managed corpus.
func (t *Tools) Intake(ctx context.Context, args IntakeArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	result, err := t.store.Intake(ctx, args)
	if err != nil {
		return "", err
	}
	t.rememberIntake(result)
	return marshalToolResult(result)
}

// Commit applies a previously returned intake plan through managed
// document mutation paths.
func (t *Tools) Commit(ctx context.Context, args CommitArgs) (string, error) {
	if t == nil || t.store == nil {
		return "", fmt.Errorf("document index not configured")
	}
	args.IntakeID = strings.TrimSpace(args.IntakeID)
	if args.IntakeID == "" {
		return "", fmt.Errorf("intake_id is required; call doc_intake first")
	}
	result, ok := t.lookupIntake(args.IntakeID)
	if !ok {
		return "", fmt.Errorf("unknown intake_id %q; call doc_intake again before doc_commit", args.IntakeID)
	}

	action := normalizeIntakeAction(args.Action)
	if args.Action != "" && action == "" {
		return "", fmt.Errorf("unsupported action %q; expected create_new, update_existing, append_existing, or draft_for_review", args.Action)
	}
	if action == "" {
		action = result.RecommendedAction
	}
	if action == "" {
		action = result.CommitPlan.RecommendedAction
	}
	if action == "" {
		return "", fmt.Errorf("action is required for intake_id %q", args.IntakeID)
	}
	if result.Status == IntakeRefused {
		return "", fmt.Errorf("intake_id %q cannot be committed: %s", args.IntakeID, result.Reason)
	}
	if action != IntakeActionDraftForReview && (result.CommitPlan.RequiresConfirmation || action != result.RecommendedAction) && !args.Confirm {
		return "", fmt.Errorf("intake_id %q requires confirm=true before %s; recommended_action=%s reason=%s",
			args.IntakeID, action, result.RecommendedAction, result.CommitPlan.ConfirmationReason)
	}

	body := strings.TrimSpace(args.Body)
	status := "committed"
	var (
		ref string
		out any
		err error
	)
	switch action {
	case IntakeActionCreateNew:
		ref = result.ProposedRef
		if ref == "" {
			return "", fmt.Errorf("intake_id %q has no proposed_ref for create_new", args.IntakeID)
		}
		if body == "" {
			return "", fmt.Errorf("body is required for create_new")
		}
		root, relPath, parseErr := parseRef(ref)
		if parseErr != nil {
			return "", parseErr
		}
		if t.store.refExists(ctx, root, relPath) {
			return "", fmt.Errorf("proposed_ref %q already exists; rerun doc_intake or choose update_existing", ref)
		}
		out, err = t.store.Write(ctx, WriteArgs{
			Ref:         ref,
			Title:       result.NormalizedTitle,
			Description: firstValue(result.NormalizedFrontmatter, "description"),
			Tags:        result.NormalizedTags,
			Frontmatter: result.NormalizedFrontmatter,
			Body:        &body,
		})
	case IntakeActionUpdateExisting:
		ref = result.TargetRef
		if ref == "" {
			return "", fmt.Errorf("intake_id %q has no target_ref for update_existing", args.IntakeID)
		}
		if body == "" {
			return "", fmt.Errorf("body is required for update_existing")
		}
		mode := "append_body"
		section := strings.TrimSpace(args.Section)
		heading := strings.TrimSpace(args.Heading)
		if section != "" || heading != "" {
			mode = "upsert_section"
			if section == "" {
				section = heading
			}
		}
		out, err = t.store.Edit(ctx, EditArgs{
			Ref:     ref,
			Mode:    mode,
			Content: body,
			Section: section,
			Heading: heading,
		})
	case IntakeActionAppendExisting:
		ref = result.TargetRef
		if ref == "" {
			return "", fmt.Errorf("intake_id %q has no target_ref for append_existing", args.IntakeID)
		}
		if body == "" {
			return "", fmt.Errorf("body is required for append_existing")
		}
		out, err = t.store.JournalUpdate(ctx, JournalUpdateArgs{
			Ref:    ref,
			Entry:  body,
			Window: args.Window,
		})
	case IntakeActionDraftForReview:
		status = "draft_for_review"
		ref = result.ProposedRef
		out = map[string]any{
			"proposed_ref":      result.ProposedRef,
			"target_ref":        result.TargetRef,
			"normalized_title":  result.NormalizedTitle,
			"normalized_tags":   result.NormalizedTags,
			"related_documents": result.RelatedDocuments,
			"review_suggestion": "No document was written. Ask the user which destination/action to use, then rerun doc_intake if needed.",
		}
	default:
		return "", fmt.Errorf("unsupported action %q; expected create_new, update_existing, append_existing, or draft_for_review", action)
	}
	if err != nil {
		return "", err
	}
	if action != IntakeActionDraftForReview {
		t.forgetIntake(args.IntakeID)
	}
	return marshalToolResult(CommitResult{
		IntakeID: args.IntakeID,
		Action:   action,
		Ref:      ref,
		Status:   status,
		Result:   out,
	})
}

func (t *Tools) rememberIntake(result *IntakeResult) {
	if t == nil || result == nil {
		return
	}
	id := newIntakeID()
	result.IntakeID = id
	result.CommitPlan.IntakeID = id
	t.intakeMu.Lock()
	defer t.intakeMu.Unlock()
	if t.intakes == nil {
		t.intakes = make(map[string]IntakeResult)
	}
	t.intakes[id] = *result
}

func (t *Tools) lookupIntake(id string) (IntakeResult, bool) {
	t.intakeMu.Lock()
	defer t.intakeMu.Unlock()
	result, ok := t.intakes[id]
	return result, ok
}

func (t *Tools) forgetIntake(id string) {
	t.intakeMu.Lock()
	defer t.intakeMu.Unlock()
	delete(t.intakes, id)
}

func newIntakeID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("intake_%d", time.Now().UnixNano())
	}
	return "intake_" + hex.EncodeToString(b[:])
}

func normalizeIntakeAction(action IntakeAction) IntakeAction {
	switch action {
	case IntakeActionCreateNew, IntakeActionUpdateExisting, IntakeActionAppendExisting, IntakeActionDraftForReview:
		return action
	default:
		return ""
	}
}
