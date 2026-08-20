package agent

import (
	"context"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// Recovery from a runner rejecting a request its loaded context window
// cannot hold. Separate from model selection because it runs after the
// choice has been made and the provider has disagreed with it.

func (l *Loop) maybeRetryExplicitModelAfterProviderContextError(
	ctx context.Context,
	model string,
	err error,
	msgs []llm.Message,
	toolDefs []map[string]any,
	stream llm.StreamCallback,
	capture modelCallCaptureHooks,
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
	growTo := func(target int) error {
		if dep.MaxContextWindow <= 0 || target <= dep.LoadedContextWindow {
			return nil
		}
		prep, prepErr := l.modelRuntime.PrepareExplicitModel(ctx, dep.ID, target)
		if prepErr != nil {
			return prepErr
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
		return nil
	}

	retryContext := growLoadContextTokens(
		estimateRequestContextTokens(msgs, toolDefs),
		dep.LoadedContextWindow,
		dep.MaxContextWindow,
	)
	if growErr := growTo(retryContext); growErr != nil {
		return nil, "", growErr, true
	}

	// escalateToMax is the second and last growth this recovery will attempt.
	// The measured window is the cheap guess; if the runner rejects that too,
	// there is nothing left to infer from and the deployment's advertised
	// maximum is the only remaining answer — which is what this path always
	// reached for before the growth was bounded. Two loads is the ceiling, so
	// a wrong guess costs one extra load rather than an unbounded climb.
	escalatedToMax := false
	escalateToMax := func() (bool, error) {
		if escalatedToMax || dep.MaxContextWindow <= 0 || retryContext >= dep.MaxContextWindow {
			return false, nil
		}
		escalatedToMax = true
		if growErr := growTo(dep.MaxContextWindow); growErr != nil {
			return false, growErr
		}
		return true, nil
	}

	retryCall := func(tools []map[string]any) (*llm.ChatResponse, error) {
		capture.attempt(retryModel, msgs, tools)
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

	// Dropping the tool schemas is the cheaper lever than growing the window
	// again, and for a model LM Studio has to prompt into tool use they are
	// what inflated the prompt in the first place. So escalate here only when
	// that lever is unavailable; where it is available the tool-free retry
	// below runs first and escalates only if it, too, is still too large.
	toolsAreALever := len(toolDefs) > 0 && !dep.TrainedForToolUse

	if changed {
		resp, retryErr := retryCall(toolDefs)
		if retryErr == nil {
			return resp, retryModel, nil, true
		}
		if !toolsAreALever && isLMStudioLoadedContextError(retryErr) {
			escalated, escalateErr := escalateToMax()
			if escalateErr != nil {
				return nil, "", escalateErr, true
			}
			if escalated {
				resp, retryErr = retryCall(toolDefs)
				if retryErr == nil {
					return resp, retryModel, nil, true
				}
			}
		}
		if !toolsAreALever || !isLMStudioLoadedContextError(retryErr) {
			return nil, "", retryErr, true
		}
	}

	if toolsAreALever && strings.TrimSpace(retryModel) != "" {
		resp, retryErr := retryCall(nil)
		if retryErr == nil {
			return resp, retryModel, nil, true
		}
		if isLMStudioLoadedContextError(retryErr) {
			escalated, escalateErr := escalateToMax()
			if escalateErr != nil {
				return nil, "", escalateErr, true
			}
			if escalated {
				resp, retryErr = retryCall(nil)
				if retryErr == nil {
					return resp, retryModel, nil, true
				}
			}
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

func isLMStudioLoadedContextError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "tokens to keep from the initial prompt is greater than the context length") &&
		strings.Contains(msg, "load the model with a larger context length")
}
