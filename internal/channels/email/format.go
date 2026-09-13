package email

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// The formatters below render tool results as prose. They are the
// pre-JSON contract and are replaced wholesale when the handlers move
// to structured results; nothing outside the handlers calls them.

// maxListOutput and maxReadOutput cap the prose results at the sizes
// AGENTS.md sets for tool output: search results 16 KB, transcripts
// 32 KB. maxSubjectOutput keeps one oversized header from filling a
// list on its own.
const (
	maxListOutput    = 16 * 1024
	maxReadOutput    = 32 * 1024
	maxSubjectOutput = 1024
)

func formatEnvelopeList(listed ListResult) string {
	now := time.Now()
	var sb strings.Builder
	if listed.Truncated() {
		sb.WriteString(fmt.Sprintf("Showing %d of %d message(s) in %s:\n\n", len(listed.Envelopes), listed.TotalMatched, listed.Folder))
	} else {
		sb.WriteString(fmt.Sprintf("Found %d message(s) in %s:\n\n", len(listed.Envelopes), listed.Folder))
	}

	shown := 0
	for _, env := range listed.Envelopes {
		var block strings.Builder
		block.WriteString(fmt.Sprintf("UID: %d\n", env.UID))
		block.WriteString(fmt.Sprintf("From: %s\n", truncateUTF8(env.From.String(), maxSubjectOutput)))
		block.WriteString(fmt.Sprintf("Subject: %s\n", truncateUTF8(env.Subject, maxSubjectOutput)))
		block.WriteString(fmt.Sprintf("Date: %s\n", promptfmt.FormatDelta(env.Date, now)))

		if len(env.Flags) > 0 {
			block.WriteString(fmt.Sprintf("Flags: %s\n", strings.Join(env.Flags, ", ")))
		}
		block.WriteString(fmt.Sprintf("Size: %d bytes\n", env.Size))
		block.WriteString("\n")
		// Leave room for the marker below so the capped result still
		// fits in maxListOutput.
		if sb.Len()+block.Len() > maxListOutput-256 {
			break
		}
		sb.WriteString(block.String())
		shown++
	}
	if rest := len(listed.Envelopes) - shown; rest > 0 {
		sb.WriteString(fmt.Sprintf("[%d more message(s) not shown: the result is capped at %d KB; narrow the search or lower limit]\n", rest, maxListOutput/1024))
	}

	return sb.String()
}

func formatMessage(msg *Message) string {
	now := time.Now()
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("From: %s\n", msg.From.String()))
	sb.WriteString(fmt.Sprintf("To: %s\n", strings.Join(addressStrings(msg.To), ", ")))
	if len(msg.Cc) > 0 {
		sb.WriteString(fmt.Sprintf("Cc: %s\n", strings.Join(addressStrings(msg.Cc), ", ")))
	}
	if len(msg.ReplyTo) > 0 {
		sb.WriteString(fmt.Sprintf("Reply-To: %s\n", strings.Join(addressStrings(msg.ReplyTo), ", ")))
	}
	sb.WriteString(fmt.Sprintf("Subject: %s\n", msg.Subject))
	sb.WriteString(fmt.Sprintf("Date: %s\n", promptfmt.FormatDelta(msg.Date, now)))
	if len(msg.Flags) > 0 {
		sb.WriteString(fmt.Sprintf("Flags: %s\n", strings.Join(msg.Flags, ", ")))
	}
	if msg.MessageID != "" {
		sb.WriteString(fmt.Sprintf("Message-ID: %s\n", msg.MessageID))
	}
	sb.WriteString(fmt.Sprintf("UID: %d | Size: %d bytes\n", msg.UID, msg.Size))
	if len(msg.Attachments) > 0 {
		sb.WriteString("Attachments:\n")
		for _, att := range msg.Attachments {
			name := att.Filename
			if name == "" {
				name = "(unnamed)"
			}
			kind := "attachment"
			if att.Inline {
				kind = "inline"
			}
			sb.WriteString(fmt.Sprintf("  - %s (%s, %d bytes, %s)\n", name, att.ContentType, att.Size, kind))
		}
		if msg.AttachmentsOmitted > 0 {
			sb.WriteString(fmt.Sprintf("  - and %d more part(s) not listed\n", msg.AttachmentsOmitted))
		}
	}
	sb.WriteString("\n---\n\n")

	switch {
	case msg.TextBody != "" && msg.BodySource == "html":
		sb.WriteString("[body rendered from HTML]\n\n")
		sb.WriteString(msg.TextBody)
	case msg.TextBody != "":
		sb.WriteString(msg.TextBody)
	default:
		sb.WriteString("[No text content available]")
	}
	if msg.BodyTruncated {
		sb.WriteString(fmt.Sprintf("\n\n[truncated — body exceeds %d KB]", maxBodySize/1024))
	}
	if msg.RawTruncated {
		sb.WriteString(fmt.Sprintf("\n\n[message exceeds %d MB; parts past the cut were not parsed, so the attachment list may be incomplete]", maxRawMessageSize/(1024*1024)))
	}

	out := sb.String()
	if len(out) > maxReadOutput {
		marker := fmt.Sprintf("\n\n[output capped at %d KB]", maxReadOutput/1024)
		out = truncateUTF8(out, maxReadOutput-len(marker)) + marker
	}
	return out
}

func formatFolderList(folders []Folder) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d folder(s):\n\n", len(folders)))

	for _, f := range folders {
		sb.WriteString(fmt.Sprintf("%-30s  %d messages", f.Name, f.Messages))
		if f.Unseen > 0 {
			sb.WriteString(fmt.Sprintf(" (%d unseen)", f.Unseen))
		}
		if f.Role != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", f.Role))
		}
		if !f.Selectable {
			sb.WriteString(" [not selectable]")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
