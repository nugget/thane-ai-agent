package awareness

import (
	"fmt"
	"sort"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// previewSampleSize is how many member ids a target-expansion preview
// carries as a concrete sample alongside the count.
const previewSampleSize = 5

// maxIngestEntries caps the ingestion registry (#1192): the guardrail
// that keeps a runaway subscription loop from feeding the state-change
// window an unbounded glob set. Globs make the cap generous — one
// pattern covers a family of entities.
const maxIngestEntries = 64

// targetExpansion is the author-time membership preview for a glob or
// registry-backed subscription target: how many entities it matches right
// now and a sample of them.
type targetExpansion struct {
	Count  int
	Sample []string
}

// previewTargetExpansion resolves a glob or area/label/floor target's
// current membership against the registry so add/list can advertise the
// expansion and flag a zero-member target as a likely mistake. It returns
// nil (no error) when there is no registry client wired, or when the
// target is a concrete entity_id that is its own membership and needs no
// preview. Membership is registry truth, not state-filtered: a member
// that is momentarily stateless is still a real member, and "matches
// zero" is exactly the typo signal worth surfacing.
func previewTargetExpansion(registries *renderRegistries, target SubscriptionTarget) (*targetExpansion, error) {
	if registries == nil {
		return nil, nil
	}
	var members []string
	switch target.Kind {
	case TargetArea, TargetLabel, TargetFloor:
		resolver, err := newMembershipResolver(registries)
		if err != nil {
			return nil, err
		}
		members = resolver.members(target)
	case TargetGlob:
		entities, err := registries.entities()
		if err != nil {
			return nil, err
		}
		for id := range entities {
			ok, err := homeassistant.MatchEntityGlob(target.Value, id)
			if err != nil {
				return nil, err
			}
			if ok {
				members = append(members, id)
			}
		}
		sort.Strings(members)
	default:
		return nil, nil
	}
	sample := members
	if len(sample) > previewSampleSize {
		sample = sample[:previewSampleSize]
	}
	return &targetExpansion{Count: len(members), Sample: append([]string(nil), sample...)}, nil
}

// entityNoun agrees "entity"/"entities" with a count.
func entityNoun(n int) string {
	if n == 1 {
		return "entity"
	}
	return "entities"
}

// moreMembersSuffix renders " (+N more)" when a sample is shorter than
// the full membership, and "" when the sample is the whole set.
func moreMembersSuffix(total, shown int) string {
	if total > shown {
		return fmt.Sprintf(" (+%d more)", total-shown)
	}
	return ""
}
