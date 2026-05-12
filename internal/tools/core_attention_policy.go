package tools

// DirectHumanEgressToolNames returns tool names that can directly contact a
// person or mutate an active human conversation. Delegated and subsystem loops
// should wake the core loop with request_core_attention instead of receiving
// these tools by default.
func DirectHumanEgressToolNames() []string {
	return append([]string(nil), directHumanEgressToolNames...)
}

var directHumanEgressToolNames = []string{
	"email_reply",
	"email_send",
	"ha_notify",
	"request_human_decision",
	"request_human_escalation",
	"send_notification",
	"send_reaction",
	"signal_send_message",
	"signal_send_reaction",
}
