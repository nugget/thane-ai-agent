package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
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

// The three representations GET /v1/loops/{name}/outputs/{output} can
// serve, as negotiation results. Markdown is the endpoint's default: the
// output is a document, so the document body is what an undecided
// client gets.
const (
	loopOutputPlain    = "text/plain"
	loopOutputJSONType = "application/json"
	loopOutputMarkdown = "text/markdown"
)

// acceptClause is one parsed entry of an Accept header: a lowered
// type/subtype (either may be "*") and its quality weight.
type acceptClause struct {
	typ, sub string
	q        float64
}

// parseAcceptClauses splits an Accept header into its media-range
// clauses. Malformed entries are skipped rather than failing the
// request — a compat surface has no business 400ing over a sloppy
// header — and q defaults to 1 with values clamped into [0,1].
func parseAcceptClauses(header string) []acceptClause {
	var clauses []acceptClause
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		mediaType := strings.ToLower(strings.TrimSpace(fields[0]))
		typ, sub, ok := strings.Cut(mediaType, "/")
		if !ok || typ == "" || sub == "" {
			continue
		}
		clause := acceptClause{typ: typ, sub: sub, q: 1}
		for _, param := range fields[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			if q, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				clause.q = min(max(q, 0), 1)
			}
		}
		clauses = append(clauses, clause)
	}
	return clauses
}

// negotiateLoopOutputAccept picks the representation the Accept header
// earns, honestly: clauses are ranked by q-value (q=0 refuses a type),
// then match specificity (exact beats text/* beats */*), then listed
// order — so axios's default "application/json, text/plain, */*" gets
// the typed contract, not a bare verdict line. When several
// representations remain tied (a lone */*), the server prefers its
// default markdown body, then JSON, then plain: the narrow speakable
// line is served only to callers that actually singled it out.
//
// statusLineAvailable gates text/plain: when the document has published
// no status_line the negotiation falls through to the caller's next
// acceptable type instead of 406ing past a servable one. The return is
// one of the loopOutput* content types, or "" when nothing acceptable
// is servable. A header naming none of the three (or no header at all)
// gets markdown — this endpoint serves a document, not a 406 quiz —
// unless the header explicitly refused it with q=0.
func negotiateLoopOutputAccept(header string, statusLineAvailable bool) string {
	representations := []struct {
		mediaType string
		typ, sub  string
		pref      int
	}{
		{loopOutputMarkdown, "text", "markdown", 2},
		{loopOutputJSONType, "application", "json", 1},
		{loopOutputPlain, "text", "plain", 0},
	}

	clauses := parseAcceptClauses(header)
	type candidate struct {
		mediaType  string
		q          float64
		spec, pos  int
		preference int
	}
	var candidates []candidate
	markdownRefused := false
	for _, rep := range representations {
		bestSpec, bestPos, q := -1, 0, 0.0
		for pos, clause := range clauses {
			spec := -1
			switch {
			case clause.typ == rep.typ && clause.sub == rep.sub:
				spec = 2
			case clause.typ == rep.typ && clause.sub == "*":
				spec = 1
			case clause.typ == "*" && clause.sub == "*":
				spec = 0
			}
			if spec > bestSpec {
				bestSpec, bestPos, q = spec, pos, clause.q
			}
		}
		if bestSpec < 0 {
			continue // never mentioned, not even by a wildcard
		}
		if q <= 0 {
			if rep.mediaType == loopOutputMarkdown {
				markdownRefused = true
			}
			continue // explicitly refused
		}
		candidates = append(candidates, candidate{rep.mediaType, q, bestSpec, bestPos, rep.pref})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].q != candidates[j].q {
			return candidates[i].q > candidates[j].q
		}
		if candidates[i].spec != candidates[j].spec {
			return candidates[i].spec > candidates[j].spec
		}
		if candidates[i].pos != candidates[j].pos {
			return candidates[i].pos < candidates[j].pos
		}
		return candidates[i].preference > candidates[j].preference
	})
	for _, c := range candidates {
		if c.mediaType == loopOutputPlain && !statusLineAvailable {
			continue
		}
		return c.mediaType
	}
	if len(candidates) == 0 && !markdownRefused {
		return loopOutputMarkdown
	}
	return ""
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
// text/markdown (and any unrecognized or absent Accept) returns the
// document body. The header is negotiated for real — q-values,
// wildcards, listed order; see [negotiateLoopOutputAccept] — so a
// client listing several types gets the best one it asked for. A
// plain-only request against a document that has published no
// status_line is 406 with the available representations named, because
// inventing a one-liner would misrepresent the loop; a client that also
// accepted another type gets that type instead of the 406.
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

	// The declared contract gates every representation: this endpoint
	// is addressed by an output name on a definition, so what it serves
	// is what that definition declares. A hand-edited body that happens
	// to contain a reserved heading is content, not contract — parsed
	// sections count only when the spec declared facets.
	payload, sectioned := looppkg.ParseFacetSections(record.Body)
	faceted := sectioned && output.HasFacets()
	declared := make(map[string]bool, len(output.Facets))
	for _, facet := range output.Facets {
		declared[string(facet.Name)] = true
	}

	verdict := ""
	if faceted && declared[string(looppkg.OutputFacetStatusLine)] {
		if line, ok := payload.FacetByKey(string(looppkg.OutputFacetStatusLine)); ok {
			verdict = strings.TrimSpace(line)
		}
	}

	// Negotiated response: the representation depends on Accept, so
	// caches must key on it.
	w.Header().Set("Vary", "Accept")
	accept := strings.Join(r.Header.Values("Accept"), ",")
	switch negotiateLoopOutputAccept(accept, verdict != "") {
	case loopOutputPlain:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(verdict + "\n"))
	case loopOutputJSONType:
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
			for key := range declared {
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
	case loopOutputMarkdown:
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(record.Body))
	default:
		// Nothing acceptable is servable. The common shape is a
		// plain-only request against a document with no published
		// status_line; the degenerate shape is a header that q=0
		// refused everything. Either way, name what IS available.
		available := "application/json, text/markdown"
		if verdict != "" {
			available = "text/plain, application/json, text/markdown"
			s.errorResponse(w, http.StatusNotAcceptable, "the Accept header refuses every available representation: "+available)
			return
		}
		s.errorResponse(w, http.StatusNotAcceptable, "output "+outputName+" declares or has published no status_line; available representations: "+available)
	}
}
