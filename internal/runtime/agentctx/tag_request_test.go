package agentctx

import (
	"context"
	"errors"
	"testing"
)

func TestTagRequester(t *testing.T) {
	t.Parallel()

	if err := RequestTag(context.Background(), "x"); !errors.Is(err, ErrNoTagRequester) {
		t.Errorf("RequestTag without a requester = %v, want ErrNoTagRequester", err)
	}

	base := context.Background()
	if got := WithTagRequester(base, nil); got != base {
		t.Error("WithTagRequester with a nil requester changed the context")
	}

	refused := errors.New("refused")
	var got []string
	ctx := WithTagRequester(base, func(tag string) error {
		got = append(got, tag)
		if tag == "protected" {
			return refused
		}
		return nil
	})
	if err := RequestTag(ctx, "attachments"); err != nil {
		t.Errorf("RequestTag(attachments) = %v, want nil", err)
	}
	if err := RequestTag(ctx, "protected"); !errors.Is(err, refused) {
		t.Errorf("RequestTag(protected) = %v, want the requester's error", err)
	}
	if len(got) != 2 || got[0] != "attachments" || got[1] != "protected" {
		t.Errorf("requester saw %v, want [attachments protected]", got)
	}
}
