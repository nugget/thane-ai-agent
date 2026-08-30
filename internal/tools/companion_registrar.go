package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
)

const (
	// companionToolSource marks synthesized companion tools so the catalog
	// and audits can tell them apart from native and MCP tools.
	companionToolSource = "companion"

	// companionDefaultTag is forced onto every synthesized companion tool,
	// regardless of what tags the Mac authored. It is the single static
	// trailhead the model navigates to reach whatever the connected
	// companion currently offers, so a brand-new Mac capability is
	// reachable with no server-side change.
	companionDefaultTag = "companion"

	// maxCompanionToolDescriptionBytes caps a Mac-authored description so a
	// misbehaving client cannot bloat the prompt. Generous enough that real
	// descriptions are never truncated.
	maxCompanionToolDescriptionBytes = 4096

	// maxCompanionToolResultBytes bounds a dispatched tool result so a
	// high-volume or misbehaving companion (e.g. a full contact dump) can't
	// blow the model's context/cost budget. Mirrors the ~16KB ceiling other
	// JSON tool outputs in this package enforce.
	maxCompanionToolResultBytes = 16_000
)

// companionProviderLister enumerates the currently-connected providers.
// A func (rather than an interface) keeps the registrar's dependencies
// consistent with companionCallFunc and testable without a fake type.
type companionProviderLister func() []companion.ProviderInfo

// companionResultFormatter renders a raw companion result into the string
// the model sees. The default is JSON passthrough; named formatters exist
// where the server adds derivation the raw payload lacks.
type companionResultFormatter func(json.RawMessage) (string, error)

// CompanionRegistrar synthesizes model-facing tools from the tool
// definitions that connected macOS companion apps author in
// register_capabilities, and dispatches their invocations back to the
// owning capability. It is the [agent.DynamicToolSource] for companion
// tools: because companions connect and disconnect at will, the
// synthesized set is rebuilt on every registry change and layered onto
// each run rather than registered on the startup-static tool registry.
//
// The Mac authors each tool's schema, description, and tag hints; the
// registrar owns only the uniform dispatch and the forced companion tag.
// Because the Mac authors the schema and owns the decode, the Go and
// Swift sides of the contract cannot drift.
type CompanionRegistrar struct {
	list       companionProviderLister
	call       companionCallFunc
	logger     *slog.Logger
	home       *time.Location
	formatters map[string]companionResultFormatter

	mu           sync.RWMutex
	synthesized  []*Tool
	tagAdditions map[string][]string
}

// NewCompanionRegistrar builds a registrar over the given companion
// registry. Wire its Rebuild method to companion.Registry.SetOnChange and
// install it via Loop.SetDynamicToolSource. The initial snapshot is empty
// until a companion connects and registers capabilities.
// The home location is the household zone every calendar time is
// rendered in; pass nil to fall back to the host's local zone.
func NewCompanionRegistrar(registry *companion.Registry, home *time.Location, logger *slog.Logger) *CompanionRegistrar {
	return newCompanionRegistrar(registry.List, registry.Call, home, logger)
}

// newCompanionRegistrar is the func-injected constructor used by tests to
// supply a fake provider list and caller.
func newCompanionRegistrar(list companionProviderLister, call companionCallFunc, home *time.Location, logger *slog.Logger) *CompanionRegistrar {
	if logger == nil {
		logger = slog.Default()
	}
	if home == nil {
		home = time.Local
	}
	cr := &CompanionRegistrar{
		list:   list,
		call:   call,
		home:   home,
		logger: logger.With("component", "companion_registrar"),
	}
	cr.formatters = map[string]companionResultFormatter{
		// Calendar results are re-derived rather than passed through: the
		// wire carries event-zone instants and dates, and the server owns
		// the household frame, the deltas, and the divergence readings the
		// model needs (see companion_calendar_format.go).
		"macos_calendar_events": cr.calendarResultFormatter,
	}
	cr.Rebuild()
	return cr
}

// Snapshot implements agent.DynamicToolSource. It returns the current
// synthesized tools and their tag→name additions. The returned values are
// immutable after a rebuild (Rebuild always installs fresh slices/maps),
// so callers may read them without copying.
func (cr *CompanionRegistrar) Snapshot() ([]*Tool, map[string][]string) {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	return cr.synthesized, cr.tagAdditions
}

// Rebuild re-synthesizes the tool set from the currently connected
// providers. Safe to call from connection goroutines; it snapshots the
// provider list first, then swaps the result in under the write lock.
func (cr *CompanionRegistrar) Rebuild() {
	infos := cr.list()

	// Order providers by the stable identity the operator configured, so a
	// tool name claimed by more than one companion resolves to the same one
	// on every rebuild. Registry.List iterates a map, so its order is
	// nondeterministic, and the provider ID is minted fresh per connection —
	// tie-breaking on either would let the winning definition flip whenever a
	// laptop reconnects, churning the manifest with no registry change.
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Account != infos[j].Account {
			return infos[i].Account < infos[j].Account
		}
		if infos[i].ClientID != infos[j].ClientID {
			return infos[i].ClientID < infos[j].ClientID
		}
		return infos[i].ID < infos[j].ID
	})

	// Collect every provider's claim on each tool name. Several companions
	// advertising the same names is the ordinary multi-Mac case, not a fault:
	// the duplicate only matters when the claimants disagree about what the
	// tool is, which reportDivergentClaims decides below.
	claims := make(map[string][]companionToolClaim)
	for _, info := range infos {
		for _, cap := range info.Capabilities {
			for _, def := range cap.Tools {
				claim := companionToolClaim{
					tool:       cr.synthesize(cap.Name, def),
					def:        def,
					capability: cap.Name,
					account:    info.Account,
					clientID:   info.ClientID,
				}
				claims[claim.tool.Name] = append(claims[claim.tool.Name], claim)
			}
		}
	}

	names := make([]string, 0, len(claims))
	for name := range claims {
		names = append(names, name)
	}
	sort.Strings(names)

	synth := make([]*Tool, 0, len(names))
	tagAdds := make(map[string][]string)
	for _, name := range names {
		// Last stable writer wins, matching Registry.Register.
		group := claims[name]
		winner := group[len(group)-1]
		if len(group) > 1 {
			cr.reportDivergentClaims(name, group)
		}
		synth = append(synth, winner.tool)
		for _, tag := range winner.tool.Tags {
			tagAdds[tag] = append(tagAdds[tag], name)
		}
	}

	cr.mu.Lock()
	cr.synthesized = synth
	cr.tagAdditions = tagAdds
	cr.mu.Unlock()
}

// companionToolClaim is one provider's authored definition of a tool name,
// paired with the model-facing tool synthesized from it and the stable
// identity of the companion that authored it.
type companionToolClaim struct {
	tool       *Tool
	def        companion.ToolDefinition
	capability string
	account    string
	clientID   string
}

// label identifies the claiming companion by the identity an operator
// configured, never by the provider ID (re-minted on every connection, so
// useless for telling one warning from the next).
func (c companionToolClaim) label() string {
	return c.account + "/" + c.clientID
}

// reportDivergentClaims warns when the companions claiming one tool name do
// not agree on what it is. Matching claims are silent: households run more
// than one Mac by design, and identical definitions carry no information.
// Divergence does, because it means the companions are running different
// builds — the manifest shows the winner's contract while Registry.Call
// still routes by capability/method and may reach a companion that authored
// a different one, so the model can read one schema and dispatch against
// another.
func (cr *CompanionRegistrar) reportDivergentClaims(name string, group []companionToolClaim) {
	winner := group[len(group)-1]

	var (
		fields  []string
		seen    = make(map[string]bool)
		ignored []string
	)
	for _, other := range group[:len(group)-1] {
		diff := companionToolDefinitionDiff(winner, other)
		if len(diff) == 0 {
			continue
		}
		for _, field := range diff {
			if !seen[field] {
				seen[field] = true
				fields = append(fields, field)
			}
		}
		ignored = append(ignored, other.label())
	}
	if len(fields) == 0 {
		return
	}
	sort.Strings(fields)

	cr.logger.Warn("companion providers disagree on a tool definition; using one",
		"tool", name,
		"differs", strings.Join(fields, ","),
		"using", winner.label(),
		"ignoring", strings.Join(ignored, ","),
	)
}

// companionToolDefinitionDiff names the fields in which two providers'
// definitions of the same tool name disagree. An empty result means the two
// companions authored the same contract, so which one wins cannot be
// observed by the model.
func companionToolDefinitionDiff(a, b companionToolClaim) []string {
	var fields []string
	if a.capability != b.capability {
		fields = append(fields, "capability")
	}
	if a.def.Method != b.def.Method {
		fields = append(fields, "method")
	}
	if a.def.Description != b.def.Description {
		fields = append(fields, "description")
	}
	if !equalStringSets(a.def.Tags, b.def.Tags) {
		fields = append(fields, "tags")
	}
	// Both schemas are JSON-derived (map/[]any/float64/string/bool/nil), the
	// value space reflect.DeepEqual compares exactly.
	if !reflect.DeepEqual(a.def.InputSchema, b.def.InputSchema) {
		fields = append(fields, "input_schema")
	}
	return fields
}

// equalStringSets reports whether two tag lists carry the same members.
// Tags are a set everywhere they are consumed — tag indexing and gating —
// so companions that authored the same tags in a different order have not
// disagreed about anything the model can observe, and comparing them in
// order would report the ordinary multi-Mac case as skew.
func equalStringSets(a, b []string) bool {
	return maps.Equal(stringSet(a), stringSet(b))
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}

// synthesize turns one companion-authored tool definition into a
// model-facing tool bound to its capability/method.
func (cr *CompanionRegistrar) synthesize(capability string, def companion.ToolDefinition) *Tool {
	desc := def.Description
	if len(desc) > maxCompanionToolDescriptionBytes {
		cr.logger.Warn("companion tool description truncated",
			"tool", def.Name, "bytes", len(desc), "cap", maxCompanionToolDescriptionBytes)
		desc = truncateUTF8(desc, maxCompanionToolDescriptionBytes)
	}

	// Force the companion tag so every Mac-authored tool is reachable via
	// the one static trailhead, then keep whatever extra tags the Mac
	// supplied (e.g. people, scheduling).
	tags := mergeUniqueStrings([]string{companionDefaultTag}, def.Tags)

	capName := capability
	method := def.Method
	toolName := def.Name
	formatter := cr.formatterFor(toolName)

	return &Tool{
		Name:        toolName,
		Description: desc,
		Parameters:  augmentSchemaWithRouting(def.InputSchema),
		Tags:        tags,
		Source:      companionToolSource,
		CanonicalID: "companion:" + toolName,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return cr.dispatch(ctx, capName, method, args, formatter)
		},
	}
}

func (cr *CompanionRegistrar) formatterFor(toolName string) companionResultFormatter {
	if f, ok := cr.formatters[toolName]; ok {
		return f
	}
	return jsonPassthroughFormatter
}

// dispatch forwards the model's tool arguments to the owning capability
// method, threading account/client_id as routing hints (not capability
// params) so a multi-account household can target a specific Mac.
func (cr *CompanionRegistrar) dispatch(ctx context.Context, capability, method string, args map[string]any, formatter companionResultFormatter) (string, error) {
	account := strings.TrimSpace(stringArg(args, "account"))
	clientID := strings.TrimSpace(stringArg(args, "client_id"))

	payload, err := json.Marshal(forwardParams(args))
	if err != nil {
		return "", fmt.Errorf("marshal companion request: %w", err)
	}

	result, err := callCompanion(ctx, cr.call, companion.CallRequest{
		Account:    account,
		ClientID:   clientID,
		Capability: capability,
		Method:     method,
		Params:     payload,
	})
	if err != nil {
		return "", err
	}
	out, err := formatter(result)
	if err != nil {
		return "", err
	}
	return capCompanionResult(out), nil
}

// capCompanionResult bounds a formatted tool result, marking truncation
// explicitly so the model knows to narrow its request rather than assuming
// it saw everything.
func capCompanionResult(s string) string {
	if len(s) <= maxCompanionToolResultBytes {
		return s
	}
	const note = "\n\n[... companion result truncated; narrow the query or limit ...]"
	allowed := maxCompanionToolResultBytes - len(note)
	if allowed < 0 {
		allowed = 0
	}
	return truncateUTF8(s, allowed) + note
}

// augmentSchemaWithRouting returns a copy of the Mac-authored input schema
// with optional account/client_id targeting hints added, so the model can
// recover from a multi-account ambiguity error. The Mac's schema and the
// shared snapshot map are never mutated.
func augmentSchemaWithRouting(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema)+1)
	for k, v := range schema {
		out[k] = v
	}
	if out["type"] == nil {
		out["type"] = "object"
	}

	props := make(map[string]any)
	if existing, ok := out["properties"].(map[string]any); ok {
		for k, v := range existing {
			props[k] = v
		}
	}
	if _, exists := props["account"]; !exists {
		props["account"] = map[string]any{
			"type":        "string",
			"description": "Optional account identity to target when multiple companion accounts are connected.",
		}
	}
	if _, exists := props["client_id"]; !exists {
		props["client_id"] = map[string]any{
			"type":        "string",
			"description": "Optional specific device/client_id to target when an account has multiple hosts connected.",
		}
	}
	out["properties"] = props
	return out
}

// forwardParams strips the server-side routing hints from the model's
// arguments, leaving only the capability parameters the Mac's decoder
// expects.
func forwardParams(args map[string]any) map[string]any {
	params := make(map[string]any, len(args))
	for k, v := range args {
		if k == "account" || k == "client_id" {
			continue
		}
		params[k] = v
	}
	return params
}

// jsonPassthroughFormatter returns the raw companion result verbatim, the
// default for synthesized tools (generated runtime data defaults to JSON).
func jsonPassthroughFormatter(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	return string(raw), nil
}

// calendarResultFormatter re-derives calendar results in the household
// zone the registrar was built with: a framing header plus one JSON
// object per event, with deltas and event-local readings attached.
func (cr *CompanionRegistrar) calendarResultFormatter(raw json.RawMessage) (string, error) {
	var resp companionCalendarResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("decode companion calendar response: %w", err)
	}
	return formatCompanionCalendarResponse(resp, cr.home, time.Now()), nil
}
