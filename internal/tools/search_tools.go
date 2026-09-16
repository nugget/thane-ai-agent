package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Explicit discovery stays outside MemorySearch: adding a source here must
// not silently add that corpus to the automatic archive context provider.
type knowledgeSearchProvider interface {
	name() string
	permission() string
	search(context.Context, knowledgeSearchRequest) (knowledgeSourceResult, error)
}

type knowledgeSearchRequest struct {
	Query   string
	Root    string
	Limit   int
	Sources []string
}

type knowledgeRead struct {
	Tool      string            `json:"tool"`
	Arguments map[string]string `json:"arguments"`
}

type knowledgeHit struct {
	Source           string         `json:"source"`
	Kind             string         `json:"kind"`
	Evidence         string         `json:"evidence"`
	Ref              string         `json:"ref"`
	MessageID        string         `json:"message_id,omitempty"`
	Role             string         `json:"role,omitempty"`
	Origin           string         `json:"origin,omitempty"`
	ConversationID   string         `json:"conversation_id,omitempty"`
	Title            string         `json:"title,omitempty"`
	Excerpt          string         `json:"excerpt"`
	AuthoredSummary  string         `json:"authored_summary,omitempty"`
	SummaryTruncated bool           `json:"summary_truncated,omitempty"`
	MatchType        string         `json:"match_type,omitempty"`
	Age              string         `json:"age,omitempty"`
	TimeBasis        string         `json:"time_basis,omitempty"`
	Facets           []string       `json:"facets,omitempty"`
	WriteTool        string         `json:"write_tool,omitempty"`
	Read             *knowledgeRead `json:"read,omitempty"`
}

type knowledgeCoverage struct {
	Source              string          `json:"source"`
	Status              string          `json:"status"`
	Returned            int             `json:"returned"`
	Truncated           bool            `json:"truncated"`
	Detail              string          `json:"detail,omitempty"`
	Roots               []knowledgeRoot `json:"roots,omitempty"`
	RootsTruncated      bool            `json:"roots_truncated,omitempty"`
	UnavailableSurfaces []string        `json:"unavailable_surfaces,omitempty"`
}

type knowledgeRoot struct {
	Root   string `json:"root"`
	Body   bool   `json:"body"`
	Status string `json:"status"`
}

type knowledgeSourceResult struct {
	Hits                []knowledgeHit
	Truncated           bool
	Roots               []knowledgeRoot
	UnavailableSurfaces []string
}

type knowledgeSearchResult struct {
	Query     string              `json:"query"`
	Method    string              `json:"method"`
	Ordering  string              `json:"ordering"`
	Hits      []knowledgeHit      `json:"hits"`
	Coverage  []knowledgeCoverage `json:"coverage"`
	Partial   bool                `json:"partial"`
	Truncated bool                `json:"truncated"`
}

func (r *Registry) registerKnowledgeSearch() {
	providers := []knowledgeSearchProvider{
		archiveKnowledgeProvider{archive: r.archiveStore, working: r.workingMemoryStore},
		documentKnowledgeProvider{tools: r.documentSearch},
	}
	r.Register(&Tool{
		Name: "search",
		Description: "Find evidence and maintained knowledge across available conversation archives and indexed documents, including dossiers. " +
			"Uses lexical phrase/term matching, not semantic embeddings. Omit sources to search the sources available in this run; " +
			"root selects documents only unless sources is explicit. Document bodies participate only when their root has search_body enabled. " +
			"Check coverage before concluding absence: unavailable sources, on-request roots, errors, and truncation are explicit. " +
			"Hits distinguish original transcript artifacts (primary, not necessarily verified claims), synthesized memory, and documents of unspecified evidentiary status. " +
			"An excerpt is matching text; authored_summary is the document author's compact projection. Related hits sharing a ref are not independent corroboration. " +
			"Follow the read tool and arguments to inspect evidence. An archives hit whose ref is archive:session:<full-session-uuid> already names its session in the form a durable citation uses; copy it whole, never shortened. For archive time windows or surrounding conversation use archive_search/archive_range; " +
			"for document tag/frontmatter filters use doc_search. This is a bounded synchronous lookup; it does not launch a background task.",
		SkipContentResolve: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":   map[string]any{"type": "string", "description": "Topic or phrase to find. Required; at most 1024 characters."},
				"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"archives", "documents"}}, "description": "Optional source selection. Sources still require their archive or documents capability in this run."},
				"root":    map[string]any{"type": "string", "description": "Optional document root, e.g. dossiers. Explicitly includes an on_request root; never bypasses a never policy."},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "description": "Total hits across all sources, default 8. Output is capped at 16 KB."},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			request, err := parseKnowledgeSearch(args)
			if err != nil {
				return "", err
			}
			ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			result, err := runKnowledgeSearch(ctx, request, providers)
			if err != nil {
				return "", err
			}
			return fitKnowledgeSearch(result)
		},
	})
}

func parseKnowledgeSearch(args map[string]any) (knowledgeSearchRequest, error) {
	for name := range args {
		if name != "query" && name != "root" && name != "sources" && name != "limit" {
			return knowledgeSearchRequest{}, fmt.Errorf("unknown search argument %q; use query, sources, root, or limit", name)
		}
	}
	query, _ := args["query"].(string)
	request := knowledgeSearchRequest{Query: strings.TrimSpace(query), Limit: 8}
	if request.Query == "" || utf8.RuneCountInString(request.Query) > 1024 {
		return request, fmt.Errorf("query must contain 1 to 1024 characters; use a topic or phrase")
	}
	request.Root, _ = args["root"].(string)
	if value, ok := args["root"]; ok {
		if _, valid := value.(string); !valid {
			return request, fmt.Errorf("root must be a document root name string, e.g. dossiers")
		}
	}
	request.Root = strings.TrimSuffix(strings.TrimSpace(request.Root), ":")
	if raw, ok := args["limit"]; ok {
		n, valid := raw.(float64)
		if !valid || n < 1 || n > 20 || n != float64(int(n)) {
			return request, fmt.Errorf("limit must be an integer from 1 to 20")
		}
		request.Limit = int(n)
	}
	if raw, ok := args["sources"]; ok {
		values, valid := raw.([]any)
		if !valid || len(values) == 0 {
			return request, fmt.Errorf("sources must be a nonempty array containing archives and/or documents")
		}
		for _, value := range values {
			source, _ := value.(string)
			if source != "archives" && source != "documents" {
				return request, fmt.Errorf("unknown search source %q; choose archives or documents", source)
			}
			if !slices.Contains(request.Sources, source) {
				request.Sources = append(request.Sources, source)
			}
		}
	} else if request.Root != "" {
		request.Sources = []string{"documents"}
	}
	if request.Root != "" && !slices.Contains(request.Sources, "documents") {
		return request, fmt.Errorf("root scopes documents; include documents in sources or omit root")
	}
	return request, nil
}

func runKnowledgeSearch(ctx context.Context, request knowledgeSearchRequest, providers []knowledgeSearchProvider) (*knowledgeSearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &knowledgeSearchResult{Query: request.Query, Method: "lexical", Ordering: "interleaved_source_rank", Hits: []knowledgeHit{}, Coverage: []knowledgeCoverage{}}
	var batches [][]knowledgeHit
	var failures []error
	searched := 0
	for _, provider := range providers {
		if len(request.Sources) > 0 && !slices.Contains(request.Sources, provider.name()) {
			continue
		}
		coverage := knowledgeCoverage{Source: provider.name(), Status: "unavailable"}
		if !toolAvailableInExecution(ctx, provider.permission()) {
			coverage.Detail = "Requires " + provider.permission() + " in the current tool scope; activate the source capability if permitted."
			result.Partial = true
			result.Coverage = append(result.Coverage, coverage)
			continue
		}
		found, err := provider.search(ctx, request)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			coverage.Status = "error"
			coverage.Detail = truncateUTF8(err.Error(), 600)
			result.Partial = true
			failures = append(failures, fmt.Errorf("%s: %w", provider.name(), err))
		} else {
			searched++
			coverage.Status = "searched"
			coverage.UnavailableSurfaces = found.UnavailableSurfaces
			result.Partial = result.Partial || len(found.UnavailableSurfaces) > 0
			coverage.Truncated = found.Truncated
			coverage.Roots = found.Roots
			result.Truncated = result.Truncated || found.Truncated
			batches = append(batches, found.Hits)
		}
		result.Coverage = append(result.Coverage, coverage)
	}
	if searched == 0 {
		if len(failures) > 0 {
			return nil, fmt.Errorf("no search source completed: %w", errors.Join(failures...))
		}
		return nil, fmt.Errorf("no selected search source is available; activate archive or documents, and ensure archive_search or doc_search is allowed in this run")
	}
	// Scores from different corpora are not comparable. Preserve each owner's
	// ordering and interleave sources rather than claiming a universal rank.
	for index := 0; ; index++ {
		added := false
		for _, hits := range batches {
			if index < len(hits) {
				if len(result.Hits) == request.Limit {
					result.Truncated = true
					markKnowledgeTruncated(result, hits[index].Source)
					continue
				}
				result.Hits = append(result.Hits, hits[index])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return result, nil
}

func markKnowledgeTruncated(result *knowledgeSearchResult, source string) {
	for i := range result.Coverage {
		if result.Coverage[i].Source == source {
			result.Coverage[i].Truncated = true
		}
	}
}

func fitKnowledgeSearch(result *knowledgeSearchResult) (string, error) {
	for {
		for i := range result.Coverage {
			result.Coverage[i].Returned = 0
			for _, hit := range result.Hits {
				if hit.Source == result.Coverage[i].Source {
					result.Coverage[i].Returned++
				}
			}
		}
		data, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("encode search result: %w", err)
		}
		if len(data) <= archiveResultByteCap {
			return string(data), nil
		}
		result.Truncated = true
		if len(result.Hits) > 1 {
			markKnowledgeTruncated(result, result.Hits[len(result.Hits)-1].Source)
			result.Hits = result.Hits[:len(result.Hits)-1]
			continue
		}
		trimmed := false
		for i := range result.Coverage {
			if len(result.Coverage[i].Roots) > 0 {
				result.Coverage[i].Roots = result.Coverage[i].Roots[:len(result.Coverage[i].Roots)-1]
				result.Coverage[i].RootsTruncated = true
				trimmed = true
				break
			}
		}
		if !trimmed {
			return "", fmt.Errorf("search result metadata exceeds 16 KB; narrow the query to one source or document root")
		}
	}
}
