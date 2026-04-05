package app

import (
	"context"
)

func modelResourceRefreshCallbacks(ctx context.Context, resourceID string, refresh func(context.Context, string)) (func(), func(error)) {
	return func() {
			refresh(ctx, "resource_ready:"+resourceID)
		}, func(error) {
			refresh(ctx, "resource_down:"+resourceID)
		}
}
