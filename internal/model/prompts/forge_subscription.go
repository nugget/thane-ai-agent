package prompts

import "fmt"

const forgeSubscriptionWakeTemplate = `New updates were detected from followed code forge repositories. Review and act on them.

Each project section includes account, repo, branch when applicable, and subscription_id.

For each update:
1. Decide whether the release or change is relevant enough to investigate
2. Use forge tools for follow-up when you need issue, PR, check, or repository context
3. Summarize noteworthy releases or changes for the owner
4. Ignore routine churn when no action is needed

%s`

// ForgeSubscriptionWakePrompt returns the wake prompt for repo
// subscription polling.
func ForgeSubscriptionWakePrompt(contentSummary string) string {
	return fmt.Sprintf(forgeSubscriptionWakeTemplate, contentSummary)
}
