package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

func emailPassAccount(name string, mailbox config.EmailMailboxConfig) config.EmailAccountConfig {
	return config.EmailAccountConfig{
		Name:    name,
		IMAP:    config.EmailIMAPConfig{Host: "imap.example.com", Port: 993, Username: name + "@example.com"},
		Mailbox: mailbox,
	}
}

func emailPassConfig(accounts ...config.EmailAccountConfig) *config.Config {
	return &config.Config{Email: config.EmailConfig{Accounts: accounts}}
}

func specNamed(specs []looppkg.Spec, name string) (looppkg.Spec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return looppkg.Spec{}, false
}

func emailPassQueue(t *testing.T) *loopqueue.Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	queue, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new loopqueue store: %v", err)
	}
	return queue
}

// operatorWithReview is an operator mailbox that wakes the triage pass
// and names the built-in review pass, beside an assistant mailbox.
func operatorWithReview() *config.Config {
	return emailPassConfig(
		emailPassAccount("personal", config.EmailMailboxConfig{Owner: config.EmailMailboxOwnerOperator, ReviewLoop: email.DraftReviewLoopName}),
		emailPassAccount("thane", config.EmailMailboxConfig{}),
	)
}

// TestEmailPassBuiltinsArePinned pins the two passes' specs: what each
// wears, what it may not call, and how it routes, with the review pass
// asking for more quality than the triage pass.
func TestEmailPassBuiltinsArePinned(t *testing.T) {
	specs := builtInServiceDefinitionSpecs(operatorWithReview())
	tests := []struct {
		name      string
		tags      []string
		localOnly string
		mission   string
		category  string
	}{
		{email.OwnerTriageLoopName, []string{"email"}, "true", "email_triage", "owner_triage"},
		{email.DraftReviewLoopName, []string{"email", "email_drafts"}, "false", "email_review", "draft_review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := specNamed(specs, tt.name)
			if !ok {
				t.Fatalf("no %s spec", tt.name)
			}
			if spec.Operation != looppkg.OperationEventDriven || spec.Completion != looppkg.CompletionNone || spec.ParentName != pollersContainerName || !spec.Enabled {
				t.Errorf("shape = operation %q completion %q parent %q enabled %v", spec.Operation, spec.Completion, spec.ParentName, spec.Enabled)
			}
			if spec.PromptMode != agentctx.PromptModeTask {
				t.Errorf("prompt_mode = %q, want task", spec.PromptMode)
			}
			if !slices.Equal(spec.Tags, tt.tags) {
				t.Errorf("tags = %v, want %v", spec.Tags, tt.tags)
			}
			if !slices.Equal(spec.ExcludeTools, []string{"email_send"}) {
				t.Errorf("exclude_tools = %v, want [email_send]", spec.ExcludeTools)
			}
			if len(spec.Bindings) != 0 {
				t.Errorf("bindings = %v, want none; the pass serves every mailbox routed to it", spec.Bindings)
			}
			p := spec.Profile
			if p.LocalOnly != tt.localOnly || p.Mission != tt.mission || p.DelegationGating != "disabled" {
				t.Errorf("profile = %+v", p)
			}
			if _, ok := p.ExtraHints["local_required"]; ok {
				t.Error("profile carries local_required; cloud fallback is allowed")
			}
			if spec.Metadata["subsystem"] != "email" || spec.Metadata["category"] != tt.category {
				t.Errorf("metadata = %v", spec.Metadata)
			}
			if err := spec.Validate(); err != nil {
				t.Errorf("spec does not validate: %v", err)
			}
		})
	}
	triage, _ := specNamed(specs, email.OwnerTriageLoopName)
	review, _ := specNamed(specs, email.DraftReviewLoopName)
	if review.Profile.QualityFloor <= triage.Profile.QualityFloor {
		t.Errorf("review quality_floor %d must exceed triage's %d", review.Profile.QualityFloor, triage.Profile.QualityFloor)
	}
}

// TestEmailPassBuiltinsAppearOnlyWhenRouted pins the gate: a pass is
// registered when some account routes to it; the triage pass and the
// default handler only while mail is polled, and the review pass, with
// its container, whether or not it is.
func TestEmailPassBuiltinsAppearOnlyWhenRouted(t *testing.T) {
	tests := []struct {
		name       string
		mailbox    config.EmailMailboxConfig
		pollOff    bool
		wantTriage bool
		wantReview bool
	}{
		{"assistant only", config.EmailMailboxConfig{}, false, false, false},
		{"operator at its default", config.EmailMailboxConfig{Owner: config.EmailMailboxOwnerOperator}, false, true, false},
		{"operator kept on the default handler with a custom reviewer", config.EmailMailboxConfig{Owner: config.EmailMailboxOwnerOperator, WakeLoop: email.DefaultHandlerLoopName, ReviewLoop: "inbox-reviewer"}, false, false, false},
		{"assistant naming the review pass", config.EmailMailboxConfig{ReviewLoop: email.DraftReviewLoopName}, false, false, true},
		{"assistant naming the triage pass", config.EmailMailboxConfig{WakeLoop: email.OwnerTriageLoopName}, false, true, false},
		{"polling off, operator naming the review pass", config.EmailMailboxConfig{Owner: config.EmailMailboxOwnerOperator, ReviewLoop: email.DraftReviewLoopName}, true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := emailPassConfig(emailPassAccount("a", tt.mailbox))
			if tt.pollOff {
				zero := 0
				cfg.Email.PollInterval = &zero
			}
			specs := builtInServiceDefinitionSpecs(cfg)
			_, triage := specNamed(specs, email.OwnerTriageLoopName)
			_, review := specNamed(specs, email.DraftReviewLoopName)
			_, handler := specNamed(specs, email.DefaultHandlerLoopName)
			if triage != tt.wantTriage || review != tt.wantReview || handler == tt.pollOff {
				t.Errorf("triage %v review %v handler %v; want %v %v %v", triage, review, handler, tt.wantTriage, tt.wantReview, !tt.pollOff)
			}
			if _, ok := specNamed(builtInContainerDefinitionSpecs(cfg, nil), pollersContainerName); review && !ok {
				t.Errorf("the review pass is registered without its %s container", pollersContainerName)
			}
		})
	}
}

// TestEmailPassTasksTeachTheirProcedure pins the load-bearing lines of
// each task, and that neither names a folder some server happens to use.
func TestEmailPassTasksTeachTheirProcedure(t *testing.T) {
	specs := builtInServiceDefinitionSpecs(operatorWithReview())
	tests := []struct {
		loop string
		want []string
	}{
		{email.OwnerTriageLoopName, []string{
			"always pass mark_seen: false",
			`destination_role: "junk"}`,
			"The junk folder is the only destination you use",
			"email_reply {account, folder, uid, body, draft: true}",
			`only where the entry shows access: "send" and a drafts_folder`,
			"in the name writes_as shows and the voice the entry gives",
			"never mention an assistant",
			`flag: "flagged"}`,
			"email_escalate {account, folder, uid, reason}",
			"On an account without a review_loop, email_escalate refuses",
			"route draft_open",
			"operator_reply_started",
			"do not escalate it as well",
			`destination_role: "inbox"}`,
			"event.metadata.flags already includes \\Flagged",
			"When event.metadata.flags includes \\Answered, someone has already replied",
			"On any other entry, write it as the account itself",
			"the only way you write there",
		}},
		{email.DraftReviewLoopName, []string{
			"Call queue_pull once",
			"email_draft_get {draft_id}",
			"email_draft_revise {draft_id, body, note}",
			"email_draft_withdraw {draft_id, reason}",
			"refused as gone or held, the operator has it now",
			"email_search {account, folder, message_id}",
			"email_read {account, folder, uid, mark_seen: false}",
			"email_reply {account, folder, uid, body, draft: true}",
			"queue_ack {subject}",
			"queue_defer {subject}",
			"never move mail anywhere but junk",
			"include \\Answered, someone has already replied",
			"If queue_ack answers retained_newer",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.loop, func(t *testing.T) {
			spec, ok := specNamed(specs, tt.loop)
			if !ok {
				t.Fatalf("no %s spec", tt.loop)
			}
			for _, want := range tt.want {
				if !strings.Contains(spec.Task, want) {
					t.Errorf("task must contain %q", want)
				}
			}
			if m := siteFolderName.FindString(spec.Task); m != "" {
				t.Errorf("task names the site folder %q", m)
			}
			// The passes never name a later consumer of the queue,
			// routing has no local_required factor, and no closing rule
			// contradicts the drafting step.
			for _, banned := range []string{"curator", "local_required", "never touch anything in the drafts folder", "a draft that is not yours"} {
				if strings.Contains(spec.Task, banned) {
					t.Errorf("task mentions %q", banned)
				}
			}
		})
	}
}

// TestCoreDefinitionOverridesEmailPassBuiltins pins the override rule:
// a definition with the built-in's name, declared ahead of it, wins.
func TestCoreDefinitionOverridesEmailPassBuiltins(t *testing.T) {
	cfg := operatorWithReview()
	override := looppkg.Spec{Name: email.OwnerTriageLoopName, Enabled: true, Operation: looppkg.OperationEventDriven, Task: "The operator's own triage."}
	cfg.Loops.Definitions = []looppkg.Spec{override}
	base, err := (&App{cfg: cfg}).buildLoopDefinitionBaseSpecs()
	if err != nil {
		t.Fatalf("base specs: %v", err)
	}
	var found []looppkg.Spec
	for _, spec := range base {
		if spec.Name == email.OwnerTriageLoopName {
			found = append(found, spec)
		}
	}
	if len(found) != 1 || found[0].Task != override.Task {
		t.Fatalf("triage definitions = %+v, want only the override", found)
	}
	if _, ok := specNamed(base, email.DraftReviewLoopName); !ok {
		t.Error("the review built-in should still be registered")
	}
}

// emailRoutesApp is an App whose definition registry holds the
// built-ins the config produces plus extra.
func emailRoutesApp(t *testing.T, cfg *config.Config, extra ...looppkg.Spec) *App {
	t.Helper()
	specs := append(builtInContainerDefinitionSpecs(cfg, nil), builtInServiceDefinitionSpecs(cfg)...)
	specs = append(specs, extra...)
	reg, err := looppkg.NewDefinitionRegistry(specs)
	if err != nil {
		t.Fatalf("definition registry: %v", err)
	}
	return &App{cfg: cfg, loopDefinitionRegistry: reg}
}

// TestEmailRouteHydrationRefusesBadLoops pins the hydration check: a
// wake_loop or review_loop must name an event_driven definition.
func TestEmailRouteHydrationRefusesBadLoops(t *testing.T) {
	custom := looppkg.Spec{Name: "inbox-reviewer", Enabled: true, Operation: looppkg.OperationEventDriven, Task: "Review."}
	tests := []struct {
		name    string
		mailbox config.EmailMailboxConfig
		wantErr string
	}{
		{"operator defaults with the built-in review pass", config.EmailMailboxConfig{Owner: config.EmailMailboxOwnerOperator, ReviewLoop: email.DraftReviewLoopName}, ""},
		{"a custom event-driven reviewer", config.EmailMailboxConfig{ReviewLoop: "inbox-reviewer"}, ""},
		{"missing wake loop", config.EmailMailboxConfig{WakeLoop: "missing-loop"}, `mailbox.wake_loop "missing-loop" names no loop definition`},
		{"wake loop is a service", config.EmailMailboxConfig{WakeLoop: emailPollerDefinitionName}, `names a loop definition whose operation is "service"`},
		{"missing review loop", config.EmailMailboxConfig{ReviewLoop: "missing-review"}, `mailbox.review_loop "missing-review" names no loop definition`},
		{"review loop is a service", config.EmailMailboxConfig{ReviewLoop: emailPollerDefinitionName}, `mailbox.review_loop "email-poller" names a loop definition whose operation is "service"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := emailRoutesApp(t, emailPassConfig(emailPassAccount("personal", tt.mailbox)), custom)
			err := a.validateEmailRoutes()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateEmailRoutes() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "email.accounts[0] (personal)") {
				t.Fatalf("validateEmailRoutes() = %v, want an error naming the account and containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestEmailPollerHydrationRunsTheRouteCheck pins the check to the
// poller's hydration, the moment the poller would start waking loops.
func TestEmailPollerHydrationRunsTheRouteCheck(t *testing.T) {
	cfg := emailPassConfig(emailPassAccount("personal", config.EmailMailboxConfig{WakeLoop: "missing-loop"}))
	a := emailRoutesApp(t, cfg)
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	state, err := opstate.NewStore(db, nil)
	if err != nil {
		t.Fatalf("opstate: %v", err)
	}
	svc, err := email.NewService(cfg.Email, email.ServiceDependencies{State: state, MessageBus: messages.NewBus(nil)})
	if err != nil {
		t.Fatalf("email service: %v", err)
	}
	t.Cleanup(svc.Close)
	a.emailService = svc

	poller, ok := specNamed(builtInServiceDefinitionSpecs(cfg), emailPollerDefinitionName)
	if !ok {
		t.Fatal("no email-poller spec")
	}
	if _, err := a.hydrateLoopDefinitionSpec(poller); err == nil || !strings.Contains(err.Error(), `mailbox.wake_loop "missing-loop" names no loop definition`) {
		t.Fatalf("poller hydration = %v, want the route refusal", err)
	}
}

func queueToolNames(spec looppkg.Spec) []string {
	var names []string
	for _, tool := range spec.RuntimeTools {
		if strings.HasPrefix(tool.Name, "queue_") {
			names = append(names, tool.Name)
		}
	}
	return names
}

// TestEmailReviewLoopGetsQueueTools pins the attachment: a loop an
// account names as its review_loop carries queue_pull, queue_ack, and
// queue_defer over its own partition, and no other loop gets them.
func TestEmailReviewLoopGetsQueueTools(t *testing.T) {
	custom := looppkg.Spec{Name: "inbox-reviewer", Enabled: true, Operation: looppkg.OperationEventDriven, Task: "Review."}
	cfg := emailPassConfig(
		emailPassAccount("personal", config.EmailMailboxConfig{Owner: config.EmailMailboxOwnerOperator, ReviewLoop: email.DraftReviewLoopName}),
		emailPassAccount("shop", config.EmailMailboxConfig{ReviewLoop: "inbox-reviewer"}),
	)
	queue := emailPassQueue(t)
	a := &App{cfg: cfg, loopQueue: queue}
	tests := []struct {
		spec looppkg.Spec
		want []string
	}{
		{emailDraftReviewSpec(), []string{"queue_pull", "queue_ack", "queue_defer"}},
		{custom, []string{"queue_pull", "queue_ack", "queue_defer"}},
		{emailOwnerTriageSpec(), nil},
		{looppkg.Spec{Name: email.DefaultHandlerLoopName, Enabled: true, Operation: looppkg.OperationEventDriven, Task: "Triage."}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.spec.Name, func(t *testing.T) {
			hydrated, err := a.hydrateLoopDefinitionSpec(tt.spec)
			if err != nil {
				t.Fatalf("hydrate: %v", err)
			}
			if got := queueToolNames(hydrated); !slices.Equal(got, tt.want) {
				t.Errorf("queue tools = %v, want %v", got, tt.want)
			}
		})
	}

	// The pull drains the review loop's own partition.
	if err := queue.Enqueue(t.Context(), email.DraftReviewLoopName, "draft:d-1", 0, []byte(`{}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	hydrated, _ := a.hydrateLoopDefinitionSpec(emailDraftReviewSpec())
	for _, tool := range hydrated.RuntimeTools {
		if tool.Name != "queue_pull" {
			continue
		}
		out, err := tool.Handler(context.Background(), map[string]any{})
		if err != nil || !strings.Contains(out, `"subject":"draft:d-1"`) {
			t.Errorf("queue_pull = %s, %v; want the review partition's item", out, err)
		}
	}
}
