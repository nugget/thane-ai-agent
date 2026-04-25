package loop

import (
	"context"
	"testing"
)

func TestFallbackContentFromContext(t *testing.T) {
	t.Parallel()

	if got := FallbackContent(context.Background()); got != "" {
		t.Fatalf("FallbackContent(background) = %q, want empty", got)
	}

	ctx := withFallbackContent(context.Background(), "please try again")
	if got := FallbackContent(ctx); got != "please try again" {
		t.Fatalf("FallbackContent(ctx) = %q, want %q", got, "please try again")
	}
}
