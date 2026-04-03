package agent

import (
	"fmt"
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
