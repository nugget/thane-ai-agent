package tools

import (
	"github.com/nugget/thane-ai-agent/internal/integrations/media"
)

// SetMediaFeedTools adds RSS/Atom feed management tools to the registry.
func (r *Registry) SetMediaFeedTools(ft *media.FeedTools) {
	r.registerMediaFeedTools(ft)
}

func (r *Registry) registerMediaFeedTools(ft *media.FeedTools) {
	if ft == nil {
		return
	}

	r.Register(&Tool{
		Name:        "media_follow",
		Description: "Follow an RSS/Atom feed or YouTube channel. New entries are detected during periodic polling and dispatched as event-source wakes to the built-in media-default-handler loop. Pass an explicit `wake_loop` to route wakes to a custom handler (e.g. a thane_curate-managed document) instead. Accepts direct RSS/Atom URLs or YouTube channel URLs (e.g., https://www.youtube.com/@ChannelName).",
		Parameters:  media.FollowDefinition(),
		Handler:     ft.FollowHandler(),
	})

	r.Register(&Tool{
		Name:        "media_unfollow",
		Description: "Stop following a feed. Use media_feeds to find the subscription_id.",
		Parameters:  media.UnfollowDefinition(),
		Handler:     ft.UnfollowHandler(),
	})

	r.Register(&Tool{
		Name:        "media_feeds",
		Description: "List all currently followed feeds with their status, last check time, and latest entry title.",
		Parameters:  media.FeedsDefinition(),
		Handler:     ft.FeedsHandler(),
	})
}
