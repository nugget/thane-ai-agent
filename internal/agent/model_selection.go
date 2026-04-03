package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/models"
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
}

func (e *NoEligibleModelError) Error() string {
	requirement := strings.TrimSpace(e.Requirement)
	if requirement == "" {
		requirement = "this request"
	}
	if len(e.Suggestions) == 0 {
		return fmt.Sprintf("no eligible routed model supports %s; configure an eligible deployment", requirement)
	}
	return fmt.Sprintf(
		"no eligible routed model supports %s; use an explicit deployment such as %q or configure one as routable",
		requirement,
		e.Suggestions[0],
	)
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

func (l *Loop) preflightExplicitModel(ref string, needsTools, needsStreaming, needsImages bool) (string, error) {
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
	if len(reasons) > 0 {
		return "", &IncompatibleModelError{
			Model:   dep.ID,
			Reasons: reasons,
		}
	}
	return dep.ID, nil
}

func messagesNeedImages(msgs []Message) bool {
	for _, msg := range msgs {
		if len(msg.Images) > 0 {
			return true
		}
	}
	return false
}

func noEligibleImageRoutingError(cat *models.Catalog) error {
	return &NoEligibleModelError{
		Requirement: "image inputs",
		Suggestions: imageCapableDeploymentSuggestions(cat, 5),
	}
}

func imageCapableDeploymentSuggestions(cat *models.Catalog, limit int) []string {
	if cat == nil || limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	for _, dep := range cat.Deployments {
		if !dep.SupportsImages {
			continue
		}
		if dep.PolicyState == models.DeploymentPolicyStateInactive {
			continue
		}
		out = append(out, dep.ID)
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
