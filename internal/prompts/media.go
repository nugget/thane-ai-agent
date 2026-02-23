package prompts

import (
	"fmt"
	"strings"
)

// chunkSummaryTemplate is the prompt sent to a local LLM for each chunk
// during the map phase of transcript summarization. Format verbs:
// 1: chunk index, 2: total chunks, 3: transcript chunk text.
const chunkSummaryTemplate = `Summarize this section of a media transcript (part %d of %d).

Extract the key points, arguments, and noteworthy details. Preserve specific
numbers, names, dates, and claims. Aim for roughly 1/5 the length of the input.

Transcript section:
%s

Summary:`

// chunkFocusSection is appended to the chunk summary prompt when the caller
// specifies a focus topic. The single format verb is the focus string.
const chunkFocusSection = `

Focus on: %s
Preserve specific details relevant to this focus area. Content unrelated to the
focus can be mentioned briefly but should not dominate the summary.`

// reduceSummaryTemplate is the prompt sent to a local LLM during the reduce
// phase to combine chunk summaries into a unified result. The single format
// verb is the concatenated chunk summaries.
const reduceSummaryTemplate = `Combine these section summaries into a single coherent summary of the full
media transcript. Maintain chronological flow and eliminate redundancy.

Section summaries:
%s

Combined summary:`

// reduceFocusSection is appended to the reduce prompt when a focus topic
// was used during the map phase. The format verb is the focus string.
const reduceFocusSection = `

Focus on: %s
Prioritize threads and details relevant to this focus area when assembling
the combined summary.`

// reduceBriefSection is appended to the reduce prompt when the caller
// requests a brief summary.
const reduceBriefSection = `

Be very concise. Produce a summary of roughly 500 characters — just the
essential metadata and key takeaway points.`

// reduceSummarySection is appended to the reduce prompt for the default
// summary detail level.
const reduceSummarySection = `

Produce a thorough summary of 2000-3000 characters. Cover all major topics
and preserve key details.`

// TranscriptChunkSummaryPrompt returns the prompt for summarizing a single
// chunk of a transcript during the map phase. When focus is non-empty, the
// prompt instructs the model to emphasize content related to the focus topic.
func TranscriptChunkSummaryPrompt(chunk, focus string, chunkIndex, totalChunks int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(chunkSummaryTemplate, chunkIndex, totalChunks, chunk))
	if focus != "" {
		sb.WriteString(fmt.Sprintf(chunkFocusSection, focus))
	}
	return sb.String()
}

// TranscriptReducePrompt returns the prompt for combining chunk summaries
// into a final unified summary during the reduce phase. The detail parameter
// controls output length ("brief" for ~500 chars, "summary" for ~2-3K chars).
// When focus is non-empty, the prompt prioritizes relevant threads.
func TranscriptReducePrompt(chunkSummaries, focus, detail string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(reduceSummaryTemplate, chunkSummaries))
	if focus != "" {
		sb.WriteString(fmt.Sprintf(reduceFocusSection, focus))
	}
	if detail == "brief" {
		sb.WriteString(reduceBriefSection)
	} else {
		sb.WriteString(reduceSummarySection)
	}
	return sb.String()
}
