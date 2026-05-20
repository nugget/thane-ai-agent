package loop

import (
	"sort"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// LoopExistsByID satisfies the messages.LoopResolver contract — used by
// event-source subscription tools at follow-time to verify a wake target
// resolves before persisting the subscription.
func (r *Registry) LoopExistsByID(loopID string) bool {
	if r == nil {
		return false
	}
	return r.Get(loopID) != nil
}

// LoopExistsByName satisfies the messages.LoopResolver contract.
func (r *Registry) LoopExistsByName(name string) bool {
	if r == nil {
		return false
	}
	return r.GetByName(name) != nil
}

// KnownLoopNames satisfies the messages.LoopResolver contract by
// returning the names of currently-running loops in sorted order.
// Used to populate "did you mean" error messages when a wake target
// fails to resolve.
func (r *Registry) KnownLoopNames() []string {
	if r == nil {
		return nil
	}
	statuses := r.Statuses()
	names := make([]string, 0, len(statuses))
	for _, st := range statuses {
		if st.Name != "" {
			names = append(names, st.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Compile-time assertion that *Registry implements messages.LoopResolver.
var _ messages.LoopResolver = (*Registry)(nil)
