package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/models"
	"github.com/nugget/thane-ai-agent/internal/router"
)

const estimatedImageContextTokens = 1536

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

func (l *Loop) currentModelCatalog() *models.Catalog {
	if l == nil {
		return nil
	}
	if l.modelRegistry != nil {
		return l.modelRegistry.Catalog()
	}
	return l.usageCatalog
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
	if !models.CanExpandLoadedContext(dep, contextSize) {
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

	prep, err := l.modelRuntime.PrepareExplicitModel(ctx, dep.ID, contextSize)
	if err != nil {
		return false, err
	}
	if prep != nil && prep.Changed && l.router != nil && l.modelRegistry != nil {
		l.router.UpdateConfig(l.modelRegistry.Catalog().RouterConfig(0))
	}
	return prep != nil && prep.Changed, nil
}

func (l *Loop) maybeRetryExplicitModelAfterProviderContextError(
	ctx context.Context,
	model string,
	err error,
	msgs []llm.Message,
	toolDefs []map[string]any,
	stream llm.StreamCallback,
) (*llm.ChatResponse, string, error, bool) {
	if l == nil || l.modelRuntime == nil || err == nil {
		return nil, "", nil, false
	}
	if !isLMStudioLoadedContextError(err) {
		return nil, "", nil, false
	}

	cat := l.currentModelCatalog()
	if cat == nil {
		return nil, "", nil, false
	}
	dep, resolveErr := cat.ResolveDeploymentRef(model)
	if resolveErr != nil {
		return nil, "", nil, false
	}
	if strings.TrimSpace(dep.Provider) != "lmstudio" {
		return nil, "", nil, false
	}

	changed := false
	retryModel := dep.ID
	retryUpstreamModel := strings.TrimSpace(dep.LoadedInstanceID)
	refreshResolvedModel := func() error {
		result, refreshErr := l.modelRuntime.Refresh(ctx)
		if refreshErr != nil {
			return refreshErr
		}
		if result != nil && result.Changed {
			changed = true
			if l.router != nil && l.modelRegistry != nil {
				l.router.UpdateConfig(l.modelRegistry.Catalog().RouterConfig(0))
			}
		}
		refreshedCat := l.currentModelCatalog()
		if refreshedCat == nil {
			return nil
		}
		refreshedDep, resolveErr := refreshedCat.ResolveDeploymentRef(model)
		if resolveErr != nil {
			return resolveErr
		}
		dep = refreshedDep
		retryModel = dep.ID
		retryUpstreamModel = strings.TrimSpace(dep.LoadedInstanceID)
		return nil
	}
	if refreshErr := refreshResolvedModel(); refreshErr != nil {
		return nil, "", refreshErr, true
	}
	if dep.MaxContextWindow > dep.LoadedContextWindow && dep.MaxContextWindow > 0 {
		prep, prepErr := l.modelRuntime.PrepareExplicitModel(ctx, dep.ID, dep.MaxContextWindow)
		if prepErr != nil {
			return nil, "", prepErr, true
		}
		if prep != nil {
			changed = prep.Changed
			if strings.TrimSpace(prep.Resolved) != "" {
				retryModel = prep.Resolved
			}
			if strings.TrimSpace(prep.Instance) != "" {
				retryUpstreamModel = strings.TrimSpace(prep.Instance)
			}
			if changed && l.router != nil && l.modelRegistry != nil {
				l.router.UpdateConfig(l.modelRegistry.Catalog().RouterConfig(0))
			}
		}
	}

	retryCall := func(tools []map[string]any) (*llm.ChatResponse, error) {
		if retryUpstreamModel != "" {
			if client := l.modelRuntime.LMStudioClient(dep.ResourceID); client != nil {
				resp, err := client.ChatStream(ctx, retryUpstreamModel, msgs, tools, stream)
				if resp != nil {
					resp.Model = retryModel
				}
				return resp, err
			}
		}
		return l.llm.ChatStream(ctx, retryModel, msgs, tools, stream)
	}

	if changed {
		resp, retryErr := retryCall(toolDefs)
		if retryErr == nil {
			return resp, retryModel, nil, true
		}
		if len(toolDefs) == 0 || dep.TrainedForToolUse || !isLMStudioLoadedContextError(retryErr) {
			return nil, "", retryErr, true
		}
	}

	if len(toolDefs) > 0 && !dep.TrainedForToolUse && strings.TrimSpace(retryModel) != "" {
		resp, retryErr := retryCall(nil)
		if retryErr == nil {
			return resp, retryModel, nil, true
		}
		if changed || isLMStudioLoadedContextError(retryErr) {
			return nil, "", retryErr, true
		}
	}

	if !changed {
		return nil, "", nil, false
	}
	return nil, "", nil, false
}

func messagesNeedImages(msgs []Message) bool {
	for _, msg := range msgs {
		if len(msg.Images) > 0 {
			return true
		}
	}
	return false
}

func estimateRequestContextTokens(systemPrompt string, msgs []Message) int {
	total := roughTokenCount(systemPrompt)
	for _, msg := range msgs {
		total += roughTokenCount(msg.Content)
		total += len(msg.Images) * estimatedImageContextTokens
	}
	return total
}

func roughTokenCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

func isLMStudioLoadedContextError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "tokens to keep from the initial prompt is greater than the context length") &&
		strings.Contains(msg, "load the model with a larger context length")
}

func noEligibleImageRoutingError(cat *models.Catalog, decision *router.Decision) error {
	err := &NoEligibleModelError{
		Requirement: "image inputs",
		Suggestions: imageCapableDeploymentSuggestions(cat, 5),
	}
	if imageRoutingLimitedByContext(decision) {
		err.Hint = imageRoutingContextHint(cat, decision)
	}
	return err
}

func contextWindowReason(dep models.Deployment, contextSize int) string {
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

func imageCapableDeploymentSuggestions(cat *models.Catalog, limit int) []string {
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
		if dep.PolicyState == models.DeploymentPolicyStateInactive {
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

func imageRoutingContextHint(cat *models.Catalog, decision *router.Decision) string {
	base := "the available image-capable routed deployments are too small for the current prompt; try a shorter request or use a larger explicit vision deployment"
	if !imageRoutingLimitedByLoadedWindow(cat, decision) {
		return base
	}
	return base + "; at least one vision deployment advertises a larger max window than is currently loaded on the runner"
}

func imageRoutingLimitedByLoadedWindow(cat *models.Catalog, decision *router.Decision) bool {
	if cat == nil || decision == nil || len(decision.RejectedModels) == 0 {
		return false
	}
	deployments := make(map[string]models.Deployment, len(cat.Deployments))
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
