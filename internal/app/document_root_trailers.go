package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// withTurnProvenance appends the current turn's identifying facts to a commit
// message as git trailers. Every git-backed root write funnels through this
// writer, so this is the one place that can record which model wrote a
// revision, under which loop, session, and request.
//
// These facts are only knowable at write time. Recovering them afterwards
// means joining the conversation store against the request log and hoping both
// retained the window, which is why they are committed here instead: a root
// whose history names the mind behind each revision is better evidence than
// one that leaves the reader to reconstruct it.
//
// The trailers are written into the message rather than attached as notes so
// that the commit signature covers them.
func (w *documentRootProvenanceWriter) withTurnProvenance(ctx context.Context, message string) string {
	trailers := make([]string, 0, 8)
	add := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			trailers = append(trailers, key+": "+value)
		}
	}

	add(documents.TrailerModel, tools.ModelFromContext(ctx))
	add(documents.TrailerLoopID, tools.LoopIDFromContext(ctx))
	// ConversationIDFromContext reports "default" outside a conversation, which
	// identifies nothing worth committing.
	if conversationID := tools.ConversationIDFromContext(ctx); conversationID != "default" {
		add(documents.TrailerConversation, conversationID)
	}
	add(documents.TrailerSession, tools.SessionIDFromContext(ctx))
	add(documents.TrailerRequest, tools.RequestIDFromContext(ctx))
	add(documents.TrailerToolCall, tools.ToolCallIDFromContext(ctx))
	if iteration, ok := tools.IterationIndexFromContext(ctx); ok {
		add(documents.TrailerIteration, strconv.Itoa(iteration))
	}
	add(documents.TrailerCoreHead, w.coreHeadAt(ctx))

	if len(trailers) == 0 {
		return message
	}
	return strings.TrimRight(message, "\n") + "\n\n" + strings.Join(trailers, "\n") + "\n"
}

// coreHeadAt reports the core root's HEAD so a revision records which identity
// snapshot produced it. What Thane wrote is only fully legible beside the
// axioms, persona, mission, and talents it was working from, and those live in
// a root that moves independently of this one. Pinning core's commit here is
// what lets a reader recover that pairing years later.
//
// Empty on core's own writes, where it would restate the parent commit, and
// empty when core has no history or cannot be read — this is enrichment, and
// failing to read it must never fail the write that carries it.
func (w *documentRootProvenanceWriter) coreHeadAt(ctx context.Context) string {
	if w == nil || w.corePath == "" || w.root == coreDocumentRoot {
		return ""
	}
	head, err := provenance.HeadCommit(ctx, w.corePath)
	if err != nil {
		return ""
	}
	return head
}
