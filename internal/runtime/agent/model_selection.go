package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// IncompatibleModelError reports that an explicit deployment cannot
// satisfy the request's required capabilities.
type IncompatibleModelError struct {
	Model   string
	Reasons []string
}

func (e *IncompatibleModelError) Error() string {
	reasons := make([]string, 0, len(e.Reasons))
	for _, reason := range e.Reasons {
		reason = strings.TrimSpace(reason)
		if reason != "" {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		return fmt.Sprintf("model %q cannot satisfy this request", e.Model)
	}
	return fmt.Sprintf("model %q cannot satisfy this request: %s", e.Model, strings.Join(reasons, "; "))
}

// NoEligibleModelError reports that automatic routing could not find
// any deployment capable of satisfying the request.
type NoEligibleModelError struct {
	Requirement string
	Suggestions []string
	Hint        string
}

func (e *NoEligibleModelError) Error() string {
	requirement := strings.TrimSpace(e.Requirement)
	if requirement == "" {
		requirement = "this request"
	}
	base := ""
	if len(e.Suggestions) == 0 {
		base = fmt.Sprintf("no eligible routed model supports %s; configure an eligible deployment", requirement)
	} else {
		base = fmt.Sprintf(
			"no eligible routed model supports %s; use an explicit deployment such as %q or configure one as routable",
			requirement,
			e.Suggestions[0],
		)
	}
	if hint := strings.TrimSpace(e.Hint); hint != "" {
		return base + "; " + hint
	}
	return base
}

func (l *Loop) currentModelCatalog() *fleet.Catalog {
	if l == nil {
		return nil
	}
	if l.modelRegistry != nil {
		return l.modelRegistry.Catalog()
	}
	return l.usageCatalog
}

// selectExplicitModel prepares and preflights an explicit deployment
// reference for one request and returns the resolved deployment ID. Both
// a request model and a conversation pin come through here, so the two
// are judged by exactly the same rules.
func (l *Loop) selectExplicitModel(ctx context.Context, ref string, needsTools, needsStreaming, needsImages bool, contextSize int) (string, error) {
	if _, err := l.maybePrepareExplicitModel(ctx, ref, needsTools, needsStreaming, needsImages, contextSize); err != nil {
		return "", err
	}
	return l.preflightExplicitModel(ref, needsTools, needsStreaming, needsImages, contextSize)
}

// explicitModelSkipReason renders why an explicit deployment could not
// serve a turn in the shortest form that still teaches: the preflight
// reasons alone when the verdict was a capability mismatch, the whole
// error otherwise.
func explicitModelSkipReason(err error) string {
	var incompatible *IncompatibleModelError
	if errors.As(err, &incompatible) && len(incompatible.Reasons) > 0 {
		return strings.Join(incompatible.Reasons, "; ")
	}
	return err.Error()
}

func (l *Loop) preflightExplicitModel(ref string, needsTools, needsStreaming, needsImages bool, contextSize int) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "thane" {
		return ref, nil
	}

	cat := l.currentModelCatalog()
	if cat == nil {
		return ref, nil
	}

	dep, err := cat.ResolveDeploymentRef(ref)
	if err != nil {
		return "", err
	}

	var reasons []string
	// Operator policy is judged here, per turn, and not only where a
	// reference is first accepted: a deployment or resource switched off
	// after a conversation pinned it must stop receiving that
	// conversation's traffic, the same as it stops receiving routed
	// traffic (see fleet routingEligible).
	if dep.PolicyState == fleet.DeploymentPolicyStateInactive {
		reasons = append(reasons, "this deployment is currently inactive by operator policy")
	}
	if dep.ResourcePolicyState == fleet.DeploymentPolicyStateInactive {
		reasons = append(reasons, "its resource is currently inactive by operator policy")
	}
	if needsTools {
		switch {
		case !dep.ProviderSupportsTools:
			reasons = append(reasons, "its provider does not support tool use")
		case !dep.SupportsTools:
			reasons = append(reasons, "this deployment is configured without tool support")
		}
	}
	if needsStreaming && !dep.SupportsStreaming {
		reasons = append(reasons, "it does not support streaming responses")
	}
	if needsImages && !dep.SupportsImages {
		reasons = append(reasons, "it does not support image inputs")
	}
	if contextSize > 0 && dep.ContextWindow > 0 && contextSize > dep.ContextWindow {
		reasons = append(reasons, contextWindowReason(dep, contextSize))
	}
	if len(reasons) > 0 {
		return "", &IncompatibleModelError{
			Model:   dep.ID,
			Reasons: reasons,
		}
	}
	return dep.ID, nil
}

func (l *Loop) maybePrepareExplicitModel(ctx context.Context, ref string, needsTools, needsStreaming, needsImages bool, contextSize int) (bool, error) {
	if l == nil || l.modelRuntime == nil || contextSize <= 0 {
		return false, nil
	}

	cat := l.currentModelCatalog()
	if cat == nil {
		return false, nil
	}
	dep, err := cat.ResolveDeploymentRef(ref)
	if err != nil {
		return false, nil
	}
	if dep.PolicyState == fleet.DeploymentPolicyStateInactive || dep.ResourcePolicyState == fleet.DeploymentPolicyStateInactive {
		return false, nil
	}
	// contextSize is what the request requires; the window worth loading for
	// it also holds the answer. Headroom is folded in here rather than by the
	// caller because the same figure feeds preflight, which must judge the
	// deployment on the requirement alone.
	loadSize := desiredLoadContextTokens(contextSize, dep.MaxContextWindow)
	if !fleet.CanExpandLoadedContext(dep, loadSize) {
		return false, nil
	}
	if needsTools {
		switch {
		case !dep.ProviderSupportsTools:
			return false, nil
		case !dep.SupportsTools:
			return false, nil
		}
	}
	if needsStreaming && !dep.SupportsStreaming {
		return false, nil
	}
	if needsImages && !dep.SupportsImages {
		return false, nil
	}

	prep, err := l.modelRuntime.PrepareExplicitModel(ctx, dep.ID, loadSize)
	if err != nil {
		return false, err
	}
	if prep != nil && prep.Changed && l.router != nil && l.modelRegistry != nil {
		l.router.UpdateConfig(l.modelRegistry.Catalog().RouterConfig(0))
	}
	return prep != nil && prep.Changed, nil
}

func messagesNeedImages(msgs []Message) bool {
	for _, msg := range msgs {
		if len(msg.Images) > 0 {
			return true
		}
	}
	return false
}

func isContextWindowIncompatible(err error) bool {
	var incompatible *IncompatibleModelError
	if !errors.As(err, &incompatible) {
		return false
	}
	for _, reason := range incompatible.Reasons {
		reason = strings.ToLower(strings.TrimSpace(reason))
		if strings.Contains(reason, "context window") || strings.Contains(reason, "token window") {
			return true
		}
	}
	return false
}

func noEligibleImageRoutingError(cat *fleet.Catalog, decision *router.Decision) error {
	err := &NoEligibleModelError{
		Requirement: "image inputs",
		Suggestions: imageCapableDeploymentSuggestions(cat, 5),
	}
	if imageRoutingLimitedByContext(decision) {
		err.Hint = imageRoutingContextHint(cat, decision)
	}
	return err
}

func contextWindowReason(dep fleet.Deployment, contextSize int) string {
	if dep.LoadedContextWindow > 0 && dep.MaxContextWindow > dep.LoadedContextWindow {
		if contextSize <= dep.MaxContextWindow {
			return fmt.Sprintf(
				"its currently loaded context window is too small for this request (estimated %d tokens > %d loaded token window; runner advertises max %d)",
				contextSize,
				dep.LoadedContextWindow,
				dep.MaxContextWindow,
			)
		}
		return fmt.Sprintf(
			"its context window is too small for this request (estimated %d tokens > %d max token window; %d currently loaded)",
			contextSize,
			dep.MaxContextWindow,
			dep.LoadedContextWindow,
		)
	}
	return fmt.Sprintf(
		"its context window is too small for this request (estimated %d tokens > %d token window)",
		contextSize,
		dep.ContextWindow,
	)
}

func imageCapableDeploymentSuggestions(cat *fleet.Catalog, limit int) []string {
	if cat == nil || limit <= 0 {
		return nil
	}
	type candidate struct {
		id            string
		contextWindow int
	}
	candidates := make([]candidate, 0, limit)
	for _, dep := range cat.Deployments {
		if !dep.SupportsImages {
			continue
		}
		if dep.PolicyState == fleet.DeploymentPolicyStateInactive {
			continue
		}
		candidates = append(candidates, candidate{id: dep.ID, contextWindow: dep.ContextWindow})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].contextWindow != candidates[j].contextWindow {
			return candidates[i].contextWindow > candidates[j].contextWindow
		}
		return candidates[i].id < candidates[j].id
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.id)
	}
	return out
}

func imageRoutingLimitedByContext(decision *router.Decision) bool {
	if decision == nil || len(decision.RejectedModels) == 0 {
		return false
	}
	sawImageCandidate := false
	for model, reasons := range decision.RejectedModels {
		hasContextRejection := false
		hasImageRejection := false
		for _, reason := range reasons {
			if strings.Contains(reason, "context window too small") {
				hasContextRejection = true
			}
			if strings.Contains(reason, "missing image support") {
				hasImageRejection = true
			}
		}
		if hasImageRejection {
			continue
		}
		if model != "" {
			sawImageCandidate = true
		}
		if !hasContextRejection {
			return false
		}
	}
	return sawImageCandidate
}

func imageRoutingContextHint(cat *fleet.Catalog, decision *router.Decision) string {
	base := "the available image-capable routed deployments are too small for the current prompt; try a shorter request or use a larger explicit vision deployment"
	if !imageRoutingLimitedByLoadedWindow(cat, decision) {
		return base
	}
	return base + "; at least one vision deployment advertises a larger max window than is currently loaded on the runner"
}

func imageRoutingLimitedByLoadedWindow(cat *fleet.Catalog, decision *router.Decision) bool {
	if cat == nil || decision == nil || len(decision.RejectedModels) == 0 {
		return false
	}
	deployments := make(map[string]fleet.Deployment, len(cat.Deployments))
	for _, dep := range cat.Deployments {
		deployments[dep.ID] = dep
	}
	for id, reasons := range decision.RejectedModels {
		hasContextRejection := false
		for _, reason := range reasons {
			if strings.Contains(reason, "context window too small") {
				hasContextRejection = true
				break
			}
		}
		if !hasContextRejection {
			continue
		}
		dep, ok := deployments[id]
		if !ok || !dep.SupportsImages {
			continue
		}
		if dep.LoadedContextWindow > 0 && dep.MaxContextWindow > dep.LoadedContextWindow {
			return true
		}
	}
	return false
}
