package prompts

import "fmt"

// emailPollWakeTemplate is prepended to the email poller's wake message
// to give the agent context on how to handle new emails. The single
// format verb receives the poller's envelope summary.
const emailPollWakeTemplate = `New email has arrived. Triage and act on it.

For each message:
1. Read the full message body with email_read
2. Decide the appropriate action:
   - **Reply expected**: Draft and send a reply with email_reply
   - **Informational**: Note anything important, no reply needed
   - **Actionable**: Take the requested action, then reply confirming
   - **Spam/noise**: Ignore or move to trash with email_move
3. After processing, move handled messages out of INBOX (archive, folder, or trash)

Use your judgment. Not every email needs a reply. Consider the sender,
subject, and content. If you're unsure whether to reply, err on the side
of replying — silence from an AI assistant is worse than a brief acknowledgment.

When replying, write naturally in markdown. The email system converts to
proper HTML automatically. Keep replies concise and helpful.

%s`

// EmailPollWakePrompt returns the email wake prompt with the poller's
// envelope summary injected.
func EmailPollWakePrompt(envelopeSummary string) string {
	return fmt.Sprintf(emailPollWakeTemplate, envelopeSummary)
}
