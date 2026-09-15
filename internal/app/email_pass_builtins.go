package app

import (
	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// Email is handled in passes. The wake loop an account names sees each
// new message first; email-owner-triage is the built-in first pass for
// an operator mailbox. It prefers local models and does the least that
// serves the operator, handing what needs more to the account's review
// loop, which it never names: the coupling is the loop work queue, and
// email-draft-review is the built-in consumer of it. Each built-in is
// registered only when an account routes to it, and a core loops/
// document or a loops.definitions entry with the same name overrides it,
// because both come before the built-ins and the first definition of a
// name wins.

// Quality floors for the two passes. The first pass keeps the floor the
// default handler has always had; the review pass asks for more,
// because it exists for the messages and drafts the first pass could
// not do well.
const (
	emailOwnerTriageQualityFloor = 5
	emailDraftReviewQualityFloor = 8
)

// emailPassDefinitionSpecs returns the built-in email passes that some
// account routes to: email-owner-triage when an account's wake_loop is
// that name (every operator mailbox that does not name another) and
// mail is polled, because only the poller wakes it; email-draft-review
// when an account's review_loop is, polled or not, because drafts and
// escalations queue for it either way.
func emailPassDefinitionSpecs(cfg *config.Config) []looppkg.Spec {
	if !cfg.Email.Configured() {
		return nil
	}
	polling := emailServicesEnabled(cfg)
	var triage, review bool
	for _, acct := range cfg.Email.Accounts {
		triage = triage || (polling && acct.WakeLoopName() == email.OwnerTriageLoopName)
		review = review || acct.ReviewLoopName() == email.DraftReviewLoopName
	}
	var specs []looppkg.Spec
	if triage {
		specs = append(specs, emailOwnerTriageSpec())
	}
	if review {
		specs = append(specs, emailDraftReviewSpec())
	}
	return specs
}

// emailReviewConfigured reports whether some configured account names a
// review loop, which queued work wakes whether or not mail is polled.
func emailReviewConfigured(cfg *config.Config) bool {
	if !cfg.Email.Configured() {
		return false
	}
	for _, acct := range cfg.Email.Accounts {
		if acct.ReviewLoopName() != "" {
			return true
		}
	}
	return false
}

// emailOwnerTriageSpec is the first pass over an operator mailbox. It is
// unbound, like the default handler, because one pass serves every
// mailbox that routes to it and each event names its account. local_only
// "true" is a preference: the router may still choose a cloud model when
// no local one can take the turn. email_send is excluded so the pass can
// only answer the message in front of it, from the account it arrived on.
func emailOwnerTriageSpec() looppkg.Spec {
	return looppkg.Spec{
		Name:         email.OwnerTriageLoopName,
		Enabled:      true,
		ParentName:   pollersContainerName,
		Task:         emailOwnerTriageTask,
		Operation:    looppkg.OperationEventDriven,
		Completion:   looppkg.CompletionNone,
		PromptMode:   agentctx.PromptModeTask,
		Tags:         []string{"email"},
		ExcludeTools: []string{"email_send"},
		Profile: router.LoopProfile{
			Mission:          "email_triage",
			LocalOnly:        "true",
			QualityFloor:     emailOwnerTriageQualityFloor,
			DelegationGating: "disabled",
			ExtraHints:       map[string]string{"source": "email_poll"},
		},
		Metadata: map[string]string{
			"subsystem": "email",
			"category":  "owner_triage",
		},
	}
}

// emailDraftReviewSpec is the review pass. It wears email_drafts beside
// email because revising and withdrawing drafts is most of its work, and
// it is cloud-eligible with a higher quality floor because it exists for
// what the first pass could not do well. Its queue tools are attached at
// hydration, scoped to its own partition (email_review_wake.go).
func emailDraftReviewSpec() looppkg.Spec {
	return looppkg.Spec{
		Name:         email.DraftReviewLoopName,
		Enabled:      true,
		ParentName:   pollersContainerName,
		Task:         emailDraftReviewTask,
		Operation:    looppkg.OperationEventDriven,
		Completion:   looppkg.CompletionNone,
		PromptMode:   agentctx.PromptModeTask,
		Tags:         []string{"email", "email_drafts"},
		ExcludeTools: []string{"email_send"},
		Profile: router.LoopProfile{
			Mission:          "email_review",
			LocalOnly:        "false",
			QualityFloor:     emailDraftReviewQualityFloor,
			DelegationGating: "disabled",
			ExtraHints:       map[string]string{"source": "email_review"},
		},
		Metadata: map[string]string{
			"subsystem": "email",
			"category":  "draft_review",
		},
	}
}
