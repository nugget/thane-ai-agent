package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

type setLoopDefinitionRequest struct {
	Spec looppkg.Spec `json:"spec"`
}

type loopDefinitionResponse struct {
	Status     string                     `json:"status"`
	Generation int64                      `json:"generation"`
	Definition looppkg.DefinitionSnapshot `json:"definition"`
}

func (s *Server) handleLoopDefinitions(w http.ResponseWriter, _ *http.Request) {
	if s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition registry not configured")
		return
	}
	snapshot := s.loopDefinitionRegistry.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, snapshot, s.logger)
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
	snapshot := s.loopDefinitionRegistry.Snapshot()
	def, found := findAPILoopDefinition(snapshot, name)
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
	snapshot = s.loopDefinitionRegistry.Snapshot()
	def, found := findAPILoopDefinition(snapshot, req.Spec.Name)
	if !found {
		s.errorResponse(w, http.StatusInternalServerError, "loop definition stored but snapshot is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, loopDefinitionResponse{
		Status:     "ok",
		Generation: snapshot.Generation,
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
	snapshot = s.loopDefinitionRegistry.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"status":     "ok",
		"generation": snapshot.Generation,
		"name":       name,
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
