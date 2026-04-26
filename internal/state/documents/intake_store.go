package documents

import (
	"context"
	"fmt"
	"strings"
)

// Intake analyzes a proposed document contribution against existing
// corpus structure.
func (s *Store) Intake(ctx context.Context, args IntakeArgs) (*IntakeResult, error) {
	if s == nil {
		return nil, fmt.Errorf("document index not configured")
	}
	root, err := s.resolveIntakeRoot(args.Root)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Summary) == "" && strings.TrimSpace(args.BodySnippet) == "" && strings.TrimSpace(args.ContentDigest) == "" {
		return nil, fmt.Errorf("summary, body_snippet, or content_digest is required")
	}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}

	policy := s.rootPolicy(root)
	observedTags, err := s.values(ctx, root, "tags", 40, false)
	if err != nil {
		return nil, fmt.Errorf("load observed tags: %w", err)
	}
	normalizedTags := normalizeIntakeTags(args.Tags, observedTags)
	title := normalizeIntakeTitle(args)
	frontmatter := map[string][]string{
		"title":       {title},
		"description": {firstNonEmpty(args.Summary, args.ContentDigest)},
	}
	if len(normalizedTags) > 0 {
		frontmatter["tags"] = normalizedTags
	}
	related, err := s.relatedIntakeDocuments(ctx, root, args, title, normalizedTags)
	if err != nil {
		return nil, err
	}

	proposedRef, proposedRel, err := s.proposeIntakeRef(ctx, root, args, title, normalizedTags, related)
	if err != nil {
		return nil, err
	}
	top := topRelated(related)
	proposedExists := s.refExists(ctx, root, proposedRel)
	appendPreferred := intakeLooksAppendOrJournal(args.Intent)

	status := IntakeReady
	reason := ""
	action := IntakeActionCreateNew
	targetRef := ""
	rationale := []string{
		"normalized title, tags, and proposed path from current corpus state",
	}

	switch policy.Authoring {
	case AuthoringReadOnly:
		status = IntakeRefused
		reason = "target root is read_only"
		action = IntakeActionDraftForReview
		rationale = append(rationale, "root authoring policy refuses managed writes")
	case AuthoringRestricted:
		status = IntakeDraftForReview
		reason = "target root is restricted"
		action = IntakeActionDraftForReview
		rationale = append(rationale, "root authoring policy requires a narrower review path")
	case "", AuthoringManaged:
		switch {
		case proposedExists:
			status = IntakeConfirmUpdate
			reason = "proposed_ref_already_exists"
			action = IntakeActionUpdateExisting
			targetRef = proposedRef
			rationale = append(rationale, "proposed path already exists; update is safer than overwrite")
		case top != nil && top.Score >= intakeHighOverlapScore:
			status = IntakeConfirmUpdate
			reason = "high_similarity_existing_document"
			action = IntakeActionUpdateExisting
			if appendPreferred {
				action = IntakeActionAppendExisting
			}
			targetRef = top.Ref
			rationale = append(rationale, fmt.Sprintf("top related document score %.2f suggests reuse", top.Score))
		case top != nil && top.Score >= intakeMaybeOverlapScore:
			status = IntakeConfirmCreate
			reason = "similar_documents_found"
			action = IntakeActionUpdateExisting
			targetRef = top.Ref
			rationale = append(rationale, fmt.Sprintf("top related document score %.2f makes create_new a confirmation decision", top.Score))
		default:
			rationale = append(rationale, "no high-overlap document found; create_new is safe")
		}
	default:
		status = IntakeRefused
		reason = "unsupported_authoring_policy"
		action = IntakeActionDraftForReview
		rationale = append(rationale, "root authoring policy is unsupported")
	}

	requiresConfirmation := status == IntakeConfirmCreate || status == IntakeConfirmUpdate || status == IntakeDraftForReview
	confirmationReason := reason
	if requiresConfirmation && confirmationReason == "" {
		confirmationReason = string(status)
	}
	result := &IntakeResult{
		Status:                status,
		Reason:                reason,
		Root:                  root,
		RootPolicy:            s.rootPolicySummary(root),
		RecommendedAction:     action,
		ProposedRef:           proposedRef,
		TargetRef:             targetRef,
		NormalizedTitle:       title,
		NormalizedTags:        normalizedTags,
		NormalizedFrontmatter: frontmatter,
		ObservedTags:          observedTags,
		RelatedDocuments:      related,
		Rationale:             rationale,
	}
	result.CommitPlan = IntakeCommitPlan{
		RecommendedAction:     action,
		ProposedRef:           proposedRef,
		TargetRef:             targetRef,
		NormalizedTitle:       title,
		NormalizedTags:        normalizedTags,
		NormalizedFrontmatter: frontmatter,
		RequiresConfirmation:  requiresConfirmation,
		ConfirmationReason:    confirmationReason,
	}
	return result, nil
}

func (s *Store) resolveIntakeRoot(root string) (string, error) {
	root = normalizeRootName(root)
	if root != "" {
		if !rootExists(s.roots, root) {
			return "", fmt.Errorf("unknown document root %q; use doc_roots to choose one of %v", root, s.allRoots())
		}
		return root, nil
	}
	roots := s.allRoots()
	if len(roots) == 1 {
		return roots[0], nil
	}
	return "", fmt.Errorf("root is required; choose one of %v", roots)
}
