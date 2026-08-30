package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// The advertisement budget is deliberately much smaller than the outer
// generated-context cap. Advertisements are a competition for attention, not
// another path to fill every bucket. Legacy eager providers remain under the
// existing per-bucket limit while they migrate onto this contract.
const (
	maxSelectedContextAdvertisements = 8
	maxAdvertisedContextBytes        = 16 * 1024
	contextContentSeparatorBytes     = len("\n\n---\n\n")

	// contextMaterializationBudget bounds the whole lazy-materialization
	// phase, detached from the assembly walk's shared deadline.
	// Materialization runs at the tail of the serial walk, when the
	// shared 2s budget is most depleted — structurally, the
	// highest-ranked content would otherwise be the first casualty of a
	// slow turn. Same shape, same figure as the self-assessment
	// provider's detach, and for the same reason.
	contextMaterializationBudget = 1500 * time.Millisecond
)

type contextAdvertisementCandidate struct {
	advertiser    ContextAdvertiser
	advertisement agentctx.ContextAdvertisement
}

type contextAdvertisementSelection struct {
	advertiser ContextAdvertiser
	selection  agentctx.ContextSelection
	match      agentctx.ContextMatchSignal
}

// selectContextAdvertisements is the deterministic discriminator. Providers
// decide what they can offer and how the current request matches; this pass
// owns cross-provider evidence ordering, deduplication, projection choice,
// count, and byte limits. Complete detail is never selected automatically.
// selectContextAdvertisements picks the winning offers. The second return
// counts identities that made a genuinely selectable offer — a projection
// automatic selection could have taken on an empty rail — but lost to the
// offer cap or the byte budget. Losing must be observable: a turn that
// silently renders eight offers out of twenty reads as "this is
// everything", and the one thing an attention budget must never buy is a
// false sense of completeness.
func selectContextAdvertisements(candidates []contextAdvertisementCandidate) ([]contextAdvertisementSelection, int) {
	valid := make([]contextAdvertisementCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.advertiser == nil || candidate.advertisement.Validate() != nil {
			continue
		}
		valid = append(valid, candidate)
	}
	sort.Slice(valid, func(i, j int) bool {
		left, right := valid[i], valid[j]
		leftMatch, leftTier := bestContextMatch(left.advertisement.Matches)
		rightMatch, rightTier := bestContextMatch(right.advertisement.Matches)
		if leftTier != rightTier {
			return leftTier > rightTier
		}
		if leftMatch.Strength != rightMatch.Strength {
			return leftMatch.Strength > rightMatch.Strength
		}
		if left.advertisement.Source != right.advertisement.Source {
			return left.advertisement.Source < right.advertisement.Source
		}
		if left.advertisement.ID != right.advertisement.ID {
			return left.advertisement.ID < right.advertisement.ID
		}
		leftFingerprint := contextAdvertisementFingerprint(left.advertisement)
		rightFingerprint := contextAdvertisementFingerprint(right.advertisement)
		if leftFingerprint != rightFingerprint {
			return leftFingerprint < rightFingerprint
		}
		return fmt.Sprintf("%T", left.advertiser) < fmt.Sprintf("%T", right.advertiser)
	})

	selected := make([]contextAdvertisementSelection, 0, min(len(valid), maxSelectedContextAdvertisements))
	seen := make(map[string]struct{}, len(valid))
	withheld := make(map[string]struct{})
	remaining := maxAdvertisedContextBytes
	for _, candidate := range valid {
		key := candidate.advertisement.Source + "\x00" + candidate.advertisement.ID
		if _, duplicate := seen[key]; duplicate {
			delete(withheld, key)
			continue
		}
		match, _ := bestContextMatch(candidate.advertisement.Matches)
		if len(selected) == maxSelectedContextAdvertisements || remaining <= 0 {
			// Past capacity. Anything that could have been chosen on an
			// empty rail is a withheld offer, not a non-offer.
			if _, ok := chooseContextProjection(candidate.advertisement.Projections, match.Kind, maxAdvertisedContextBytes); ok {
				withheld[key] = struct{}{}
			}
			continue
		}
		separator := 0
		if len(selected) > 0 {
			separator = contextContentSeparatorBytes
		}
		projection, ok := chooseContextProjection(candidate.advertisement.Projections, match.Kind, remaining-separator)
		if !ok {
			// Not marked seen: this claimant offered nothing selectable
			// here, and a lower-ranked claimant of the same identity may
			// still carry a projection that fits. Marking the identity
			// would let the emptiest offer suppress every usable one
			// behind it. But if the offer would have fit an empty rail,
			// the budget is what refused it — count it withheld unless a
			// later claimant of the identity gets selected after all.
			if _, fits := chooseContextProjection(candidate.advertisement.Projections, match.Kind, maxAdvertisedContextBytes); fits {
				withheld[key] = struct{}{}
			}
			continue
		}
		seen[key] = struct{}{}
		delete(withheld, key)
		selected = append(selected, contextAdvertisementSelection{
			advertiser: candidate.advertiser,
			selection: agentctx.ContextSelection{
				Advertisement: candidate.advertisement,
				Projection:    projection,
			},
			match: match,
		})
		remaining -= separator + projection.EstimatedBytes
	}
	return selected, len(withheld)
}

func bestContextMatch(matches []agentctx.ContextMatchSignal) (agentctx.ContextMatchSignal, int) {
	var best agentctx.ContextMatchSignal
	bestTier := 0
	for _, match := range matches {
		tier := contextMatchTier(match.Kind)
		if tier > bestTier || (tier == bestTier && match.Strength > best.Strength) {
			best, bestTier = match, tier
		}
	}
	return best, bestTier
}

func contextMatchTier(kind agentctx.ContextMatchKind) int {
	switch kind {
	case agentctx.ContextMatchExactSubject:
		return 5
	case agentctx.ContextMatchAlias:
		return 4
	case agentctx.ContextMatchSemantic:
		return 3
	case agentctx.ContextMatchLexical:
		return 2
	case agentctx.ContextMatchAmbient:
		return 1
	default:
		return 0
	}
}

func chooseContextProjection(projections []agentctx.ContextProjection, match agentctx.ContextMatchKind, remaining int) (agentctx.ContextProjection, bool) {
	if remaining <= 0 {
		return agentctx.ContextProjection{}, false
	}
	roles := []agentctx.ContextProjectionRole{agentctx.ContextRoleContext, agentctx.ContextRoleSignal}
	preferSmall := false
	if match == agentctx.ContextMatchAmbient {
		roles = []agentctx.ContextProjectionRole{agentctx.ContextRoleSignal, agentctx.ContextRoleContext}
		preferSmall = true
	}
	for _, role := range roles {
		available := make([]agentctx.ContextProjection, 0, len(projections))
		for _, projection := range projections {
			if projection.Role == role && projection.EstimatedBytes <= remaining {
				available = append(available, projection)
			}
		}
		if len(available) == 0 {
			continue
		}
		sort.Slice(available, func(i, j int) bool {
			if available[i].EstimatedBytes != available[j].EstimatedBytes {
				if preferSmall {
					return available[i].EstimatedBytes < available[j].EstimatedBytes
				}
				return available[i].EstimatedBytes > available[j].EstimatedBytes
			}
			return available[i].Name < available[j].Name
		})
		return available[0], true
	}
	return agentctx.ContextProjection{}, false
}

func contextAdvertisementFingerprint(ad agentctx.ContextAdvertisement) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s\x00%s\x00%s\x00%s\x00", ad.Kind, ad.Ref, ad.Bucket, ad.Summary)
	for _, match := range ad.Matches {
		fmt.Fprintf(&out, "%s:%g\x00", match.Kind, match.Strength)
	}
	for _, projection := range ad.Projections {
		fmt.Fprintf(&out, "%s:%s:%s:%d\x00", projection.Name, projection.Role, projection.Format, projection.EstimatedBytes)
	}
	return out.String()
}

// materializeContextAdvertisements renders selected projections under the
// real byte budget. Estimates decide admission; actual output still has to fit
// whole. An oversized projection is dropped rather than clipped into an
// unmarked fragment.
func (a *TagContextAssembler) materializeContextAdvertisements(ctx context.Context, req agentctx.ContextRequest, candidates []contextAdvertisementCandidate) map[agentctx.ContextBucket]string {
	selected, withheld := selectContextAdvertisements(candidates)
	if len(selected) == 0 && withheld == 0 {
		return nil
	}

	// Values (loop id, logging) survive WithoutCancel; only the walk's
	// depleted deadline is replaced with this phase's own bound.
	materializeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contextMaterializationBudget)
	defer cancel()

	buffers := make(map[agentctx.ContextBucket]*strings.Builder)
	used := 0
	for _, item := range selected {
		started := time.Now()
		content, err := item.advertiser.MaterializeContextAdvertisement(materializeCtx, req, item.selection)
		warnSlowContextSource(a.logger, "context_advertisement_materialization", item.selection.Advertisement.Source+"/"+item.selection.Advertisement.ID, started)
		if err != nil {
			a.logger.Warn("context advertisement materialization failed",
				"source", item.selection.Advertisement.Source,
				"id", item.selection.Advertisement.ID,
				"projection", item.selection.Projection.Name,
				"error", err)
			continue
		}
		if content == "" {
			continue
		}
		if len(content) > item.selection.Projection.EstimatedBytes {
			// The estimate is what selection reserved; admitting a larger
			// payload spends capacity that later, honestly-estimated
			// winners were promised, and whether they then fit becomes an
			// accident of materialization order. An offer that overruns
			// its own declaration is dropped — the same policy as an
			// oversized projection, and the pressure that keeps estimates
			// honest.
			a.logger.Warn("context advertisement dropped: exceeded its own byte estimate",
				"source", item.selection.Advertisement.Source,
				"id", item.selection.Advertisement.ID,
				"projection", item.selection.Projection.Name,
				"estimated_bytes", item.selection.Projection.EstimatedBytes,
				"actual_bytes", len(content))
			continue
		}
		bucket := item.selection.Advertisement.Bucket
		buf := buffers[bucket]
		separator := 0
		if buf != nil && buf.Len() > 0 {
			separator = contextContentSeparatorBytes
		}
		if used+separator+len(content) > maxAdvertisedContextBytes {
			a.logger.Warn("context advertisement exceeded materialization budget",
				"source", item.selection.Advertisement.Source,
				"id", item.selection.Advertisement.ID,
				"projection", item.selection.Projection.Name,
				"estimated_bytes", item.selection.Projection.EstimatedBytes,
				"actual_bytes", len(content),
				"limit_bytes", maxAdvertisedContextBytes)
			continue
		}
		if buf == nil {
			buf = &strings.Builder{}
			buffers[bucket] = buf
		}
		if separator > 0 {
			buf.WriteString("\n\n---\n\n")
		}
		buf.WriteString(content)
		used += separator + len(content)
		a.logger.Debug("context advertisement materialized",
			"source", item.selection.Advertisement.Source,
			"id", item.selection.Advertisement.ID,
			"projection", item.selection.Projection.Name,
			"match_kind", item.match.Kind,
			"match_strength", item.match.Strength,
			"bytes", len(content))
	}
	// Withheld offers get one explicit line so a capped rail never reads
	// as a complete one. It rides the Related bucket — "lightweight and
	// clearly optional" is that bucket's charter, and the door it names
	// is a pull the model can choose.
	if withheld > 0 {
		buf := buffers[agentctx.ContextBucketRelated]
		if buf == nil {
			buf = &strings.Builder{}
			buffers[agentctx.ContextBucketRelated] = buf
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		fmt.Fprintf(buf, "[%d context offer(s) withheld by the advertisement budget; doc_search reaches the full corpus]", withheld)
	}

	out := make(map[agentctx.ContextBucket]string, len(buffers))
	for bucket, buf := range buffers {
		if buf.Len() > 0 {
			out[bucket] = buf.String()
		}
	}
	return out
}
