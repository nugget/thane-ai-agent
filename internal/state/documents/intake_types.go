package documents

const (
	intakeSimilarLimit      = 5
	intakeCandidateScanCap  = 500
	intakeHighOverlapScore  = 0.82
	intakeMaybeOverlapScore = 0.55
)

// IntakeStatus describes whether an intake can be committed directly or
// needs a second explicit decision.
type IntakeStatus string

const (
	// IntakeReady means the proposed commit can proceed without extra
	// confirmation.
	IntakeReady IntakeStatus = "ready"
	// IntakeConfirmCreate means a new document is possible, but similar
	// documents make an update the safer default.
	IntakeConfirmCreate IntakeStatus = "confirm_create"
	// IntakeConfirmUpdate means the intake is likely an update or append
	// to an existing document and should be confirmed before committing.
	IntakeConfirmUpdate IntakeStatus = "confirm_update"
	// IntakeDraftForReview means root policy or corpus ambiguity suggests
	// leaving the content as a draft instead of committing it.
	IntakeDraftForReview IntakeStatus = "draft_for_review"
	// IntakeRefused means the target root cannot accept managed commits.
	IntakeRefused IntakeStatus = "refused"
)

// IntakeAction is the document mutation shape recommended by intake.
type IntakeAction string

const (
	// IntakeActionCreateNew creates a new managed document at proposed_ref.
	IntakeActionCreateNew IntakeAction = "create_new"
	// IntakeActionUpdateExisting updates an existing related document.
	IntakeActionUpdateExisting IntakeAction = "update_existing"
	// IntakeActionAppendExisting appends a journal-style note to an
	// existing related document.
	IntakeActionAppendExisting IntakeAction = "append_existing"
	// IntakeActionDraftForReview declines mutation and returns a draft
	// plan for human or later review.
	IntakeActionDraftForReview IntakeAction = "draft_for_review"
)

// IntakeArgs describes a proposed managed-document addition before it is
// assigned to a final corpus destination.
type IntakeArgs struct {
	Root          string   `json:"root,omitempty"`
	Intent        string   `json:"intent,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	BodySnippet   string   `json:"body_snippet,omitempty"`
	ContentDigest string   `json:"content_digest,omitempty"`
	DesiredTitle  string   `json:"desired_title,omitempty"`
	DesiredRef    string   `json:"desired_ref,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	PathPrefix    string   `json:"path_prefix,omitempty"`
}

// IntakeRelatedDocument describes a corpus document that overlaps with
// the proposed intake.
type IntakeRelatedDocument struct {
	Ref       string   `json:"ref"`
	Title     string   `json:"title"`
	Path      string   `json:"path"`
	Tags      []string `json:"tags,omitempty"`
	Score     float64  `json:"score"`
	Rationale string   `json:"rationale,omitempty"`
}

// IntakeCommitPlan is the exact structured handoff expected by
// doc_commit.
type IntakeCommitPlan struct {
	IntakeID              string              `json:"intake_id,omitempty"`
	RecommendedAction     IntakeAction        `json:"recommended_action"`
	ProposedRef           string              `json:"proposed_ref,omitempty"`
	TargetRef             string              `json:"target_ref,omitempty"`
	NormalizedTitle       string              `json:"normalized_title,omitempty"`
	NormalizedTags        []string            `json:"normalized_tags,omitempty"`
	NormalizedFrontmatter map[string][]string `json:"normalized_frontmatter,omitempty"`
	RequiresConfirmation  bool                `json:"requires_confirmation,omitempty"`
	ConfirmationReason    string              `json:"confirmation_reason,omitempty"`
}

// IntakeResult is the model-facing corpus-aware placement analysis.
type IntakeResult struct {
	IntakeID              string                  `json:"intake_id,omitempty"`
	Status                IntakeStatus            `json:"status"`
	Reason                string                  `json:"reason,omitempty"`
	Root                  string                  `json:"root"`
	RootPolicy            RootPolicySummary       `json:"root_policy"`
	RecommendedAction     IntakeAction            `json:"recommended_action"`
	ProposedRef           string                  `json:"proposed_ref,omitempty"`
	TargetRef             string                  `json:"target_ref,omitempty"`
	NormalizedTitle       string                  `json:"normalized_title,omitempty"`
	NormalizedTags        []string                `json:"normalized_tags,omitempty"`
	NormalizedFrontmatter map[string][]string     `json:"normalized_frontmatter,omitempty"`
	ObservedTags          []ValueCount            `json:"observed_tags,omitempty"`
	RelatedDocuments      []IntakeRelatedDocument `json:"related_documents,omitempty"`
	Rationale             []string                `json:"rationale,omitempty"`
	CommitPlan            IntakeCommitPlan        `json:"commit_plan"`
}

// CommitArgs commits an approved document intake plan.
type CommitArgs struct {
	IntakeID string       `json:"intake_id"`
	Action   IntakeAction `json:"action,omitempty"`
	Body     string       `json:"body,omitempty"`
	Section  string       `json:"section,omitempty"`
	Heading  string       `json:"heading,omitempty"`
	Window   string       `json:"window,omitempty"`
	Confirm  bool         `json:"confirm,omitempty"`
}

// CommitResult describes the mutation, or draft, created from an intake.
type CommitResult struct {
	IntakeID string       `json:"intake_id"`
	Action   IntakeAction `json:"action"`
	Ref      string       `json:"ref,omitempty"`
	Status   string       `json:"status"`
	Result   any          `json:"result,omitempty"`
}
