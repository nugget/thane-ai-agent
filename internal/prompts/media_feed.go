package prompts

import "fmt"

// mediaFeedPollWakeTemplate is prepended to the feed poller's wake
// message to give the agent context on how to handle new feed entries.
// The single format verb receives the poller's content summary.
const mediaFeedPollWakeTemplate = `New content detected from followed feeds. Review and act on it.

For each new entry:
1. Consider whether the content is interesting or relevant to the owner
2. If it looks worthwhile, use media_transcript to fetch and summarize it
3. Notify the owner about noteworthy new content with a brief summary

Use your judgment — not every new video or podcast episode needs attention.
Prioritize content that aligns with known interests and preferences.

%s`

// MediaFeedPollWakePrompt returns the feed poll wake prompt with the
// poller's content summary injected.
func MediaFeedPollWakePrompt(contentSummary string) string {
	return fmt.Sprintf(mediaFeedPollWakeTemplate, contentSummary)
}
