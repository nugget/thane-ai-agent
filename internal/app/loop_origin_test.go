package app

import (
	"context"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func authoringCtx(reqID, convID, loopID string) context.Context {
	ctx := context.Background()
	if reqID != "" {
		ctx = tools.WithRequestID(ctx, reqID)
	}
	if convID != "" {
		ctx = tools.WithConversationID(ctx, convID)
	}
	if loopID != "" {
		ctx = tools.WithLoopID(ctx, loopID)
	}
	return ctx
}

// TestOriginFromContext_StampsAuthoringIdentity confirms a real authoring turn
// produces full provenance from the tool context.
func TestOriginFromContext_StampsAuthoringIdentity(t *testing.T) {
	now := time.Date(2026, 6, 30, 9, 0, 0, 0, time.UTC)
	got := originFromContext(authoringCtx("r_new", "conv-9", "lp_x"), now)
	if got == nil {
		t.Fatal("expected origin from an authoring turn")
	}
	if got.RequestID != "r_new" || got.ConversationID != "conv-9" ||
		got.CreatedByLoopID != "lp_x" || !got.CreatedAt.Equal(now) {
		t.Errorf("origin = %+v", got)
	}
}

// TestOriginFromContext_NilWithoutIdentity guards the config-hydration case: a
// bare context carries no request or loop id, and conversation_id's "default"
// fallback must not, by itself, stamp a hollow origin.
func TestOriginFromContext_NilWithoutIdentity(t *testing.T) {
	if got := originFromContext(context.Background(), time.Now()); got != nil {
		t.Errorf("expected nil origin without authoring identity, got %+v", got)
	}
}

// TestAuthoritativeOrigin_PreservesCreationProvenance is the core C2 invariant:
// a later update/replace from a DIFFERENT turn keeps the original creation
// origin rather than re-stamping it with the editing turn.
func TestAuthoritativeOrigin_PreservesCreationProvenance(t *testing.T) {
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	created := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	orig := &looppkg.OriginInfo{
		RequestID:       "r_orig",
		ConversationID:  "conv-1",
		CreatedByLoopID: "lp_a",
		CreatedAt:       created,
	}
	if err := defs.Upsert(looppkg.Spec{Name: "watcher", Enabled: true, Task: "watch the reservoir", Operation: looppkg.OperationService, Origin: orig}, created); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	a := &App{loopDefinitionRegistry: defs}

	got := a.authoritativeOrigin(authoringCtx("r_later", "conv-2", "lp_b"), "watcher", time.Now())
	if got == nil || got.RequestID != "r_orig" || !got.CreatedAt.Equal(created) {
		t.Fatalf("update must preserve creation origin, got %+v", got)
	}
}

// TestAuthoritativeOrigin_StampsNewDefinition confirms a genuinely new name is
// stamped fresh from the committing turn.
func TestAuthoritativeOrigin_StampsNewDefinition(t *testing.T) {
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	a := &App{loopDefinitionRegistry: defs}
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	got := a.authoritativeOrigin(authoringCtx("r_fresh", "conv-7", "lp_z"), "brand_new", now)
	if got == nil || got.RequestID != "r_fresh" || got.CreatedByLoopID != "lp_z" ||
		got.ConversationID != "conv-7" || !got.CreatedAt.Equal(now) {
		t.Fatalf("new definition origin = %+v", got)
	}
}

// TestAuthoritativeOrigin_ExistingWithoutOriginStaysNil guards the #1125 review
// fix: an existing definition that carries no origin (config-sourced or pre-C2)
// must NOT be re-stamped on a later edit — that would record the editing turn as
// the creation. Existence, not non-nil origin, is the discriminator.
func TestAuthoritativeOrigin_ExistingWithoutOriginStaysNil(t *testing.T) {
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	// A pre-existing definition with NO origin (e.g. a config-sourced loop).
	if err := defs.Upsert(looppkg.Spec{Name: "config_loop", Enabled: true, Task: "watch", Operation: looppkg.OperationService}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	a := &App{loopDefinitionRegistry: defs}

	// A later edit from a real authoring turn must not fabricate creation origin.
	got := a.authoritativeOrigin(authoringCtx("r_edit", "conv-2", "lp_b"), "config_loop", time.Now())
	if got != nil {
		t.Errorf("existing origin-less definition must stay origin-less on edit, got %+v", got)
	}
}
