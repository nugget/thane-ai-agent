package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

type setLoopDefinitionRequest struct {
	Spec looppkg.Spec `json:"spec"`
}

type setLoopDefinitionPolicyRequest struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type launchLoopDefinitionRequest struct {
	Launch looppkg.Launch `json:"launch"`
}

type loopDefinitionResponse struct {
	Status     string                 `json:"status"`
	Generation int64                  `json:"generation"`
	Definition looppkg.DefinitionView `json:"definition"`
}

type loopDefinitionLaunchResponse struct {
	Status     string                 `json:"status"`
	Generation int64                  `json:"generation"`
	Definition looppkg.DefinitionView `json:"definition"`
	Result     looppkg.LaunchResult   `json:"result"`
}

func (s *Server) currentLoopDefinitionView() *looppkg.DefinitionRegistryView {
	if s.loopDefinitionView != nil {
		if view := s.loopDefinitionView(); view != nil {
			return view
		}
	}
	if s.loopDefinitionRegistry == nil {
		return nil
	}
	return looppkg.BuildDefinitionRegistryView(s.loopDefinitionRegistry.Snapshot(), nil)
}

func (s *Server) handleLoopDefinitions(w http.ResponseWriter, _ *http.Request) {
	if s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition registry not configured")
		return
	}
	view := s.currentLoopDefinitionView()
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, view, s.logger)
}

func (s *Server) handleLoopDefinitionGet(w http.ResponseWriter, r *http.Request) {
	if s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition registry not configured")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "name is required")
		return
	}
	view := s.currentLoopDefinitionView()
	def, found := findAPILoopDefinitionView(view, name)
	if !found {
		s.errorResponse(w, http.StatusNotFound, (&looppkg.UnknownDefinitionError{Name: name}).Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, def, s.logger)
}

func (s *Server) handleLoopDefinitionSet(w http.ResponseWriter, r *http.Request) {
	if s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition registry not configured")
		return
	}
	var req setLoopDefinitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Spec.Name = strings.TrimSpace(req.Spec.Name)
	if req.Spec.Name == "" {
		s.errorResponse(w, http.StatusBadRequest, "spec.name is required")
		return
	}
	snapshot := s.loopDefinitionRegistry.Snapshot()
	if existing, found := findAPILoopDefinition(snapshot, req.Spec.Name); found && existing.Source == looppkg.DefinitionSourceConfig {
		s.errorResponse(w, http.StatusConflict, (&looppkg.ImmutableDefinitionError{Name: req.Spec.Name}).Error())
		return
	}

	updatedAt := time.Now().UTC()
	if s.persistLoopDefinition != nil {
		if err := s.persistLoopDefinition(req.Spec, updatedAt); err != nil {
			s.logger.Error("persist loop definition failed", "name", req.Spec.Name, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to persist loop definition")
			return
		}
	}
	if err := s.loopDefinitionRegistry.Upsert(req.Spec, updatedAt); err != nil {
		var immutable *looppkg.ImmutableDefinitionError
		switch {
		case errors.As(err, &immutable):
			s.errorResponse(w, http.StatusConflict, err.Error())
		default:
			s.errorResponse(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if s.reconcileLoopDefinition != nil {
		if err := s.reconcileLoopDefinition(r.Context(), req.Spec.Name); err != nil {
			s.logger.Error("reconcile loop definition failed", "name", req.Spec.Name, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to reconcile loop definition")
			return
		}
	}
	view := s.currentLoopDefinitionView()
	def, found := findAPILoopDefinitionView(view, req.Spec.Name)
	if !found {
		s.errorResponse(w, http.StatusInternalServerError, "loop definition stored but snapshot is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, loopDefinitionResponse{
		Status:     "ok",
		Generation: view.Generation,
		Definition: def,
	}, s.logger)
}

func (s *Server) handleLoopDefinitionDelete(w http.ResponseWriter, r *http.Request) {
	if s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition registry not configured")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "name is required")
		return
	}
	snapshot := s.loopDefinitionRegistry.Snapshot()
	if existing, found := findAPILoopDefinition(snapshot, name); found && existing.Source == looppkg.DefinitionSourceConfig {
		s.errorResponse(w, http.StatusConflict, (&looppkg.ImmutableDefinitionError{Name: name}).Error())
		return
	} else if !found {
		s.errorResponse(w, http.StatusNotFound, (&looppkg.UnknownDefinitionError{Name: name}).Error())
		return
	}
	if s.deleteLoopDefinition != nil {
		if err := s.deleteLoopDefinition(name); err != nil {
			s.logger.Error("delete persisted loop definition failed", "name", name, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to delete persisted loop definition")
			return
		}
	}
	if err := s.loopDefinitionRegistry.Delete(name, time.Now().UTC()); err != nil {
		var immutable *looppkg.ImmutableDefinitionError
		var unknown *looppkg.UnknownDefinitionError
		switch {
		case errors.As(err, &immutable):
			s.errorResponse(w, http.StatusConflict, err.Error())
		case errors.As(err, &unknown):
			s.errorResponse(w, http.StatusNotFound, err.Error())
		default:
			s.errorResponse(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if s.reconcileLoopDefinition != nil {
		if err := s.reconcileLoopDefinition(r.Context(), name); err != nil {
			s.logger.Error("reconcile loop definition failed", "name", name, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to reconcile loop definition")
			return
		}
	}
	view := s.currentLoopDefinitionView()
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"status":     "ok",
		"generation": view.Generation,
		"name":       name,
	}, s.logger)
}

func (s *Server) handleLoopDefinitionPolicySet(w http.ResponseWriter, r *http.Request) {
	if s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition registry not configured")
		return
	}
	var req setLoopDefinitionPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		s.errorResponse(w, http.StatusBadRequest, "name is required")
		return
	}
	if _, found := findAPILoopDefinition(s.loopDefinitionRegistry.Snapshot(), req.Name); !found {
		s.errorResponse(w, http.StatusNotFound, (&looppkg.UnknownDefinitionError{Name: req.Name}).Error())
		return
	}
	state, err := looppkg.ParseDefinitionPolicyState(req.State)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	policy := looppkg.DefinitionPolicy{
		State:     state,
		Reason:    req.Reason,
		UpdatedAt: time.Now().UTC(),
	}
	if s.persistLoopDefinitionPolicy != nil {
		if err := s.persistLoopDefinitionPolicy(req.Name, policy); err != nil {
			s.logger.Error("persist loop definition policy failed", "name", req.Name, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to persist loop definition policy")
			return
		}
	}
	if err := s.loopDefinitionRegistry.ApplyPolicy(req.Name, policy, policy.UpdatedAt); err != nil {
		var unknown *looppkg.UnknownDefinitionError
		if errors.As(err, &unknown) {
			s.errorResponse(w, http.StatusNotFound, err.Error())
			return
		}
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.reconcileLoopDefinition != nil {
		if err := s.reconcileLoopDefinition(r.Context(), req.Name); err != nil {
			s.logger.Error("reconcile loop definition failed", "name", req.Name, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to reconcile loop definition")
			return
		}
	}
	view := s.currentLoopDefinitionView()
	def, found := findAPILoopDefinitionView(view, req.Name)
	if !found {
		s.errorResponse(w, http.StatusInternalServerError, "loop definition policy applied but snapshot is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, loopDefinitionResponse{
		Status:     "ok",
		Generation: view.Generation,
		Definition: def,
	}, s.logger)
}

func (s *Server) handleLoopDefinitionPolicyDelete(w http.ResponseWriter, r *http.Request) {
	if s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition registry not configured")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "name is required")
		return
	}
	if _, found := findAPILoopDefinition(s.loopDefinitionRegistry.Snapshot(), name); !found {
		s.errorResponse(w, http.StatusNotFound, (&looppkg.UnknownDefinitionError{Name: name}).Error())
		return
	}
	if s.deleteLoopDefinitionPolicy != nil {
		if err := s.deleteLoopDefinitionPolicy(name); err != nil {
			s.logger.Error("delete persisted loop definition policy failed", "name", name, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to delete loop definition policy")
			return
		}
	}
	if err := s.loopDefinitionRegistry.ClearPolicy(name, time.Now().UTC()); err != nil {
		var unknown *looppkg.UnknownDefinitionError
		if errors.As(err, &unknown) {
			s.errorResponse(w, http.StatusNotFound, err.Error())
			return
		}
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.reconcileLoopDefinition != nil {
		if err := s.reconcileLoopDefinition(r.Context(), name); err != nil {
			s.logger.Error("reconcile loop definition failed", "name", name, "error", err)
			s.errorResponse(w, http.StatusInternalServerError, "failed to reconcile loop definition")
			return
		}
	}
	view := s.currentLoopDefinitionView()
	def, found := findAPILoopDefinitionView(view, name)
	if !found {
		s.errorResponse(w, http.StatusInternalServerError, "loop definition policy cleared but snapshot is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, loopDefinitionResponse{
		Status:     "ok",
		Generation: view.Generation,
		Definition: def,
	}, s.logger)
}

func (s *Server) handleLoopDefinitionLaunch(w http.ResponseWriter, r *http.Request) {
	if s.launchLoopDefinition == nil || s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition launch is not configured")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		s.errorResponse(w, http.StatusBadRequest, "name is required")
		return
	}
	var req launchLoopDefinitionRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			s.errorResponse(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	result, err := s.launchLoopDefinition(r.Context(), name, req.Launch)
	if err != nil {
		var unknown *looppkg.UnknownDefinitionError
		var inactive *looppkg.InactiveDefinitionError
		var paused *looppkg.PausedDefinitionError
		var ineligible *looppkg.IneligibleDefinitionError
		switch {
		case errors.As(err, &unknown):
			s.errorResponse(w, http.StatusNotFound, err.Error())
		case errors.As(err, &inactive):
			s.errorResponse(w, http.StatusConflict, err.Error())
		case errors.As(err, &paused):
			s.errorResponse(w, http.StatusConflict, err.Error())
		case errors.As(err, &ineligible):
			s.errorResponse(w, http.StatusConflict, err.Error())
		default:
			s.errorResponse(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	view := s.currentLoopDefinitionView()
	def, found := findAPILoopDefinitionView(view, name)
	if !found {
		s.errorResponse(w, http.StatusInternalServerError, "loop definition launched but snapshot is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, loopDefinitionLaunchResponse{
		Status:     "ok",
		Generation: view.Generation,
		Definition: def,
		Result:     result,
	}, s.logger)
}

func findAPILoopDefinition(snapshot *looppkg.DefinitionRegistrySnapshot, name string) (looppkg.DefinitionSnapshot, bool) {
	if snapshot == nil {
		return looppkg.DefinitionSnapshot{}, false
	}
	for _, def := range snapshot.Definitions {
		if def.Name == name {
			return def, true
		}
	}
	return looppkg.DefinitionSnapshot{}, false
}

func findAPILoopDefinitionView(view *looppkg.DefinitionRegistryView, name string) (looppkg.DefinitionView, bool) {
	if view == nil {
		return looppkg.DefinitionView{}, false
	}
	for _, def := range view.Definitions {
		if def.Name == name {
			return def, true
		}
	}
	return looppkg.DefinitionView{}, false
}
