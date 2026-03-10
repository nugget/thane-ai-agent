package forge

import (
	"context"
	"testing"
)

func TestContextProvider(t *testing.T) {
	t.Parallel()

	const want = "### Forge Accounts\n```json\n{}\n```\n"
	p := NewContextProvider(want)

	got, err := p.GetContext(context.Background(), "any message")
	if err != nil {
		t.Fatalf("GetContext() unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("GetContext() = %q, want %q", got, want)
	}
}

func TestContextProvider_Empty(t *testing.T) {
	t.Parallel()

	p := NewContextProvider("")

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatalf("GetContext() unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("GetContext() = %q, want empty string", got)
	}
}
