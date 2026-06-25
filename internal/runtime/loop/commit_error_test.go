package loop

import (
	"errors"
	"testing"
)

func TestCommitErrorErrorText(t *testing.T) {
	base := errors.New("boom")
	cases := []struct {
		stage CommitStage
		want  string
	}{
		{CommitStagePersist, "persist loop definition: boom"},
		{CommitStageReconcile, "reconcile loop definition: boom"},
		// The register stage returns the underlying error verbatim so its
		// already-self-describing messages stay unprefixed.
		{CommitStageRegister, "boom"},
	}
	for _, c := range cases {
		ce := &CommitError{Stage: c.stage, Err: base}
		if got := ce.Error(); got != c.want {
			t.Errorf("stage %q: Error() = %q, want %q", c.stage, got, c.want)
		}
	}
}

func TestCommitErrorErrorTextNilErr(t *testing.T) {
	// Error() must not panic on a nil Err at any stage — it runs on every
	// logging/formatting path that touches a commit failure.
	for _, stage := range []CommitStage{CommitStagePersist, CommitStageRegister, CommitStageReconcile} {
		ce := &CommitError{Stage: stage}
		if got, want := ce.Error(), string(stage)+" loop definition: <nil>"; got != want {
			t.Errorf("stage %q: Error() = %q, want %q", stage, got, want)
		}
	}
}

func TestCommitErrorUnwrapsTypedErrors(t *testing.T) {
	immutable := &ImmutableDefinitionError{Name: "metacog_like"}
	ce := &CommitError{Stage: CommitStageRegister, Err: immutable}

	var target *ImmutableDefinitionError
	if !errors.As(ce, &target) {
		t.Fatal("errors.As did not unwrap CommitError to *ImmutableDefinitionError")
	}
	if target.Name != "metacog_like" {
		t.Errorf("unwrapped name = %q, want metacog_like", target.Name)
	}
}
