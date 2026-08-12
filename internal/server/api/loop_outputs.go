package api

import (
	"context"
	"net/http"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// DocumentReader is the narrow document dependency the loop-output
// endpoint needs: read one managed document by ref. A function rather
// than the store so the API server stays decoupled from the documents
// package's write surface and tests can stub a document in one line.
type DocumentReader func(ctx context.Context, ref string) (*documents.DocumentRecord, error)

// UseDocumentReader wires the managed-document read function that
// backs GET /v1/loops/{name}/outputs/{output}.
func (s *Server) UseDocumentReader(read DocumentReader) {
	s.documentRead = read
}

// loopOutputJSON is the typed representation of one loop output: the
// contract's fields as fields, never section markup. Facet keys absent
// from the document are omitted rather than sent empty.
type loopOutputJSON struct {
	Loop       string            `json:"loop"`
	Output     string            `json:"output"`
	Ref        string            `json:"ref"`
	ModifiedAt string            `json:"modified_at,omitempty"`
	Facets     map[string]string `json:"facets,omitempty"`
	Full       string            `json:"full"`
}

// handleLoopOutputGet serves one declared loop output at the fidelity
// the caller's Accept header asks for — the #1250 read surface at the
// HTTP boundary. text/plain returns the status_line alone (speakable,
// no markup); application/json returns the typed contract;
// text/markdown (and any other Accept, including none) returns the
// document body. A plain-text request against a document that has
// published no status_line is 406 with the available representations
// named, because inventing a one-liner would misrepresent the loop.
func (s *Server) handleLoopOutputGet(w http.ResponseWriter, r *http.Request) {
	if s.loopDefinitionRegistry == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "loop definition registry not configured")
		return
	}
	if s.documentRead == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "document reader not configured")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	outputName := strings.TrimSpace(r.PathValue("output"))
	if name == "" || outputName == "" {
		s.errorResponse(w, http.StatusBadRequest, "loop name and output name are required")
		return
	}
	spec, ok := s.loopDefinitionRegistry.Get(name)
	if !ok {
		s.errorResponse(w, http.StatusNotFound, (&looppkg.UnknownDefinitionError{Name: name}).Error())
		return
	}
	var output *looppkg.OutputSpec
	for i := range spec.Outputs {
		if spec.Outputs[i].Name == outputName {
			output = &spec.Outputs[i]
			break
		}
	}
	if output == nil {
		s.errorResponse(w, http.StatusNotFound, "loop "+name+" declares no output named "+outputName)
		return
	}
	record, err := s.documentRead(r.Context(), output.Ref)
	if err != nil {
		if documents.IsNotFound(err) {
			s.errorResponse(w, http.StatusNotFound, "output document "+output.Ref+" has not been written yet")
			return
		}
		s.errorResponse(w, http.StatusInternalServerError, "read output document: "+err.Error())
		return
	}

	payload, faceted := looppkg.ParseFacetSections(record.Body)
	accept := strings.ToLower(r.Header.Get("Accept"))
	switch {
	case strings.Contains(accept, "text/plain"):
		verdict := ""
		if faceted {
			if line, ok := payload.FacetByKey(string(looppkg.OutputFacetStatusLine)); ok {
				verdict = strings.TrimSpace(line)
			}
		}
		if verdict == "" {
			s.errorResponse(w, http.StatusNotAcceptable, "output "+outputName+" has published no status_line; available representations: application/json, text/markdown")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(verdict + "\n"))
	case strings.Contains(accept, "application/json"):
		out := loopOutputJSON{
			Loop:       name,
			Output:     outputName,
			Ref:        output.Ref,
			ModifiedAt: record.ModifiedAt,
			Full:       record.Body,
		}
		if faceted {
			out.Full = payload.Full
			facets := make(map[string]string)
			for _, key := range looppkg.FacetKeys() {
				if key == "full" {
					continue
				}
				if value, ok := payload.FacetByKey(key); ok && strings.TrimSpace(value) != "" {
					facets[key] = value
				}
			}
			if len(facets) > 0 {
				out.Facets = facets
			}
		}
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, out, s.logger)
	default:
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(record.Body))
	}
}
