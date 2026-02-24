package tools

import (
	"github.com/nugget/thane-ai-agent/internal/media"
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
		Description: "Follow an RSS/Atom feed or YouTube channel. New entries will be detected during periodic polling and reported to you. Accepts direct RSS/Atom URLs or YouTube channel URLs (e.g., https://www.youtube.com/@ChannelName).",
		Parameters:  media.FollowDefinition(),
		Handler:     ft.FollowHandler(),
	})

	r.Register(&Tool{
		Name:        "media_unfollow",
		Description: "Stop following a feed. Use media_feeds to find the feed_id.",
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
