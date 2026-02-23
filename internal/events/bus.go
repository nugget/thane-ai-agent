// Package events provides a publish/subscribe event bus for operational
// observability. Events flow from components (agent loop, signal bridge,
// scheduler, etc.) to subscribers (WebSocket handler, future metrics
// collector). The bus is nil-safe: calling Publish on a nil *Bus is a
// no-op, so components do not need guard checks.
package events

import (
	"sync"
	"time"
)

// Source constants identify which component published an event.
const (
	// SourceAgent identifies events from the core agent loop.
	SourceAgent = "agent"
	// SourceSignal identifies events from the Signal bridge.
	SourceSignal = "signal"
	// SourceDelegate identifies events from delegate task execution.
	SourceDelegate = "delegate"
	// SourceEmail identifies events from the email poller.
	SourceEmail = "email"
	// SourceMetacog identifies events from the metacognitive loop.
	SourceMetacog = "metacog"
	// SourceScheduler identifies events from the task scheduler.
	SourceScheduler = "scheduler"
)

// Kind constants describe the type of event within a source.
const (
	// KindRequestStart signals the beginning of an agent request.
	// Data: request_id, conversation_id, channel.
	KindRequestStart = "request_start"
	// KindLLMCall signals the start of an LLM API call.
	// Data: request_id, iter, model.
	KindLLMCall = "llm_call"
	// KindLLMResponse signals completion of an LLM API call.
	// Data: request_id, iter, model, tokens_in, tokens_out,
	// cost_usd, tool_calls.
	KindLLMResponse = "llm_response"
	// KindToolCall signals the start of a tool execution.
	// Data: request_id, tool.
	KindToolCall = "tool_call"
	// KindToolDone signals completion of a tool execution.
	// Data: request_id, tool, ok, duration_ms.
	KindToolDone = "tool_done"
	// KindRequestComplete signals the end of an agent request.
	// Data: request_id, model, iterations, total_tokens_in,
	// total_tokens_out, total_cost_usd, elapsed_ms.
	KindRequestComplete = "request_complete"

	// KindMessageReceived signals an incoming Signal message.
	// Data: sender, conversation_id, message_len.
	KindMessageReceived = "message_received"
	// KindReactionReceived signals an incoming Signal reaction.
	// Data: sender, emoji.
	KindReactionReceived = "reaction_received"
	// KindSessionRotated signals a session was rotated due to inactivity.
	// Data: conversation_id, sender.
	KindSessionRotated = "session_rotated"

	// KindSpawn signals a delegate task was spawned.
	// Data: delegate_id, profile, task_len.
	KindSpawn = "spawn"
	// KindComplete signals a delegate task completed.
	// Data: delegate_id, iterations, total_tokens_in,
	// total_tokens_out, total_cost_usd, exhausted.
	KindComplete = "complete"

	// KindPollStart signals the start of an email poll cycle.
	// Data: accounts.
	KindPollStart = "poll_start"
	// KindPollComplete signals the end of an email poll cycle.
	// Data: new_messages, accounts.
	KindPollComplete = "poll_complete"

	// KindIterationStart signals the start of a metacognitive iteration.
	// Data: conversation_id, supervisor.
	KindIterationStart = "iteration_start"
	// KindSleepAdjust signals a metacognitive sleep duration change.
	// Data: sleep_seconds.
	KindSleepAdjust = "sleep_adjust"

	// KindTaskFired signals a scheduled task has begun executing.
	// Data: task_id, task_name.
	KindTaskFired = "task_fired"
	// KindTaskComplete signals a scheduled task has finished executing.
	// Data: task_id, task_name, ok, duration_ms.
	KindTaskComplete = "task_complete"
)

// Event represents a single operational event published by a component.
type Event struct {
	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"ts"`
	// Source identifies the component that published the event.
	Source string `json:"source"`
	// Kind describes the type of event within the source.
	Kind string `json:"kind"`
	// Data holds event-specific key/value pairs.
	Data map[string]any `json:"data,omitempty"`
}

// Bus is a non-blocking broadcast event bus. Subscribers receive events
// on buffered channels; slow subscribers miss events rather than
// blocking publishers.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
	// recvToSend maps the receive-only channel returned by Subscribe
	// back to the bidirectional channel stored in subs. This allows
	// Unsubscribe to accept <-chan Event (the caller's view) without
	// an illegal type conversion.
	recvToSend map[<-chan Event]chan Event
}

// New creates a new event bus ready for use.
func New() *Bus {
	return &Bus{
		subs:       make(map[chan Event]struct{}),
		recvToSend: make(map[<-chan Event]chan Event),
	}
}

// Publish sends an event to all subscribers. Non-blocking: if a
// subscriber's channel is full, the event is dropped for that
// subscriber. Safe to call on a nil receiver (no-op).
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			// Subscriber is full — drop the event rather than block.
		}
	}
}

// Subscribe returns a channel that receives published events. The
// caller must eventually call Unsubscribe to avoid resource leaks.
// bufSize controls the channel buffer; 64 is a reasonable default for
// WebSocket consumers.
func (b *Bus) Subscribe(bufSize int) <-chan Event {
	ch := make(chan Event, bufSize)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[ch] = struct{}{}
	b.recvToSend[ch] = ch
	return ch
}

// Unsubscribe removes a subscription and closes the channel. Safe to
// call with a channel that is already unsubscribed (no-op).
func (b *Bus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sendCh, ok := b.recvToSend[ch]
	if !ok {
		return
	}
	delete(b.subs, sendCh)
	delete(b.recvToSend, ch)
	close(sendCh)
}

// SubscriberCount returns the number of active subscribers.
func (b *Bus) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
