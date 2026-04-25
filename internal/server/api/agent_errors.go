package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
)

func agentErrorDetails(err error) (int, string) {
	var ambiguous *llm.AmbiguousModelError
	if errors.As(err, &ambiguous) {
		return http.StatusBadRequest, err.Error()
	}
	var incompatible *agent.IncompatibleModelError
	if errors.As(err, &incompatible) {
		return http.StatusBadRequest, err.Error()
	}
	var noEligible *agent.NoEligibleModelError
	if errors.As(err, &noEligible) {
		return http.StatusBadRequest, err.Error()
	}
	if fleet.IsUnknownModel(err) {
		return http.StatusBadRequest, err.Error()
	}

	msg := strings.TrimSpace(err.Error())
	switch {
	case strings.HasPrefix(msg, "API error 400:"):
		return http.StatusBadRequest, msg
	case strings.Contains(msg, "empty assistant completion"):
		return http.StatusBadGateway, msg
	default:
		return http.StatusInternalServerError, "agent error"
	}
}
