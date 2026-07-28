package api

import (
	"context"
	"net/http"

	"github.com/nugget/thane-ai-agent/internal/platform/identity"
)

// IdentityEvidenceReader returns the live core-backed identity evidence for
// this instance.
type IdentityEvidenceReader func(context.Context) (identity.Evidence, error)

// UseIdentityEvidence configures the native identity-evidence surface.
func (s *Server) UseIdentityEvidence(reader IdentityEvidenceReader) {
	s.identityEvidence = reader
}

// handleIdentity returns the durable public identity and local provenance
// posture of the running instance. [GET /v1/identity]
func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	if s.identityEvidence == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "identity evidence not available")
		return
	}
	evidence, err := s.identityEvidence(r.Context())
	if err != nil {
		s.logger.Warn("identity evidence unavailable", "error", err)
		s.errorResponse(w, http.StatusServiceUnavailable, "identity evidence not available")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, evidence, s.logger)
}
