package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

func agentErrorDetails(err error) (int, string) {
	var ambiguous *llm.AmbiguousModelError
	if errors.As(err, &ambiguous) {
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
