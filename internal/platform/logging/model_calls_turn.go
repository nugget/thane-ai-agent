package logging

import "sync"

// ModelCallTurn is one request's capture state. It numbers the
// request's calls and remembers which blobs it has already queued, so
// each call's queue item carries only blobs new to the turn. A blob is
// remembered only once the item carrying it is queued: a dropped item
// leaves the next one to carry its blobs, so no stored row references a
// blob that was never offered to the writer.
type ModelCallTurn struct {
	store     *ModelCallStore
	requestID string

	mu   sync.Mutex
	seq  int
	sent map[string]struct{}
	// lost records that a call never reached the writer, so the turn's
	// outcome row can say its capture is incomplete.
	lost bool
}

// Turn begins capture for one request. A nil store returns a nil turn,
// whose methods do nothing, so disabled capture costs no hashing.
func (s *ModelCallStore) Turn(requestID string) *ModelCallTurn {
	if s == nil {
		return nil
	}
	return &ModelCallTurn{store: s, requestID: requestID, sent: make(map[string]struct{})}
}

// Capture hashes one call synchronously, so the caller may mutate its
// messages and response as soon as it returns, and queues it. It
// reports whether the call was queued.
func (t *ModelCallTurn) Capture(call ModelCall) bool {
	if t == nil {
		return false
	}
	hashed, err := hashModelCall(call.Messages, call.Tools, call.Response, call.Profile)
	if err != nil {
		t.store.warnPowersOfTwo(t.store.hashErrors.Add(1), "model call capture could not hash call", "hash_errors", err)
		t.mu.Lock()
		t.seq++
		t.lost = true
		t.mu.Unlock()
		return false
	}
	// A provider that sent sections the capture could not locate saw a
	// different prompt from the one stored, so the row cannot stand as a
	// faithful replay case.
	if hashed.sectionsNotSent {
		call.InputIncomplete = true
	}
	// The queued row keeps only owned values: the inputs live on as
	// hashed bytes, and the pointed-to facts are copied.
	call.Messages, call.Tools, call.Response = nil, nil, nil
	call.Profile.ChatTemplateKwargs = nil
	call.TTL, call.MaxTokens = copyPtr(call.TTL), copyPtr(call.MaxTokens)
	call.Stream, call.FirstEvent = copyPtr(call.Stream), copyPtr(call.FirstEvent)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	var fresh []modelBlob
	for _, h := range hashed.blobs.order {
		if _, ok := t.sent[h]; !ok {
			fresh = append(fresh, modelBlob{hash: h, data: hashed.blobs.data[h]})
		}
	}
	// The row keeps the hashes it references, never the blob bytes: only
	// the blobs new to the turn ride the queue item, so a turn repeating
	// the same history does not hold a full copy per queued call.
	refs := hashed.blobs.order
	hashed.blobs = nil
	row := &modelCallRow{requestID: t.requestID, callSeq: t.seq, call: call, hashed: hashed, refs: refs}
	if !t.store.enqueue(modelCallItem{call: row, blobs: fresh}) {
		t.lost = true
		return false
	}
	for _, b := range fresh {
		t.sent[b.hash] = struct{}{}
	}
	if hashed.segmentFallback {
		t.store.segmentFallbacks.Add(1)
	}
	return true
}

// Finish queues the request's outcome row and reports whether it was
// queued. Once written, it closes the request: the writer forgets any
// failed transaction recorded against it. When Finish returns false
// the row is lost, and a failed transaction stays remembered until
// the request has been idle for an hour.
func (t *ModelCallTurn) Finish(turn ModelTurn) bool {
	if t == nil {
		return false
	}
	// The drain marshals the outcomes later, on its own goroutine, so the
	// row takes its own copy: a caller reusing its ledger after Finish
	// returns must neither change what is stored nor race the marshal.
	turn.ToolOutcomes = copyToolOutcomes(turn.ToolOutcomes)
	t.mu.Lock()
	row := &modelTurnRow{requestID: t.requestID, turn: turn, callCount: t.seq, lost: t.lost}
	t.mu.Unlock()
	return t.store.enqueue(modelCallItem{turn: row})
}

// copyToolOutcomes copies an outcome ledger down to each record's
// per-target map, or returns nil for an empty one.
func copyToolOutcomes(in map[string]ToolOutcomeRecord) map[string]ToolOutcomeRecord {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ToolOutcomeRecord, len(in))
	for name, rec := range in {
		if rec.Targets != nil {
			targets := make(map[string]ToolTargetRecord, len(rec.Targets))
			for target, tally := range rec.Targets {
				targets[target] = tally
			}
			rec.Targets = targets
		}
		out[name] = rec
	}
	return out
}
