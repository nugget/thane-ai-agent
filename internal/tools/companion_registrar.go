package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

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
)

// companionProviderLister enumerates the currently-connected providers.
// A func (rather than an interface) keeps the registrar's dependencies
// consistent with companionCallFunc and testable without a fake type.
type companionProviderLister func() []companion.ProviderInfo

// companionResultFormatter renders a raw companion result into the string
// the model sees. The default is JSON passthrough; named formatters exist
// only to preserve prose output for specific legacy tools.
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
	formatters map[string]companionResultFormatter

	mu           sync.RWMutex
	synthesized  []*Tool
	tagAdditions map[string][]string
}

// NewCompanionRegistrar builds a registrar over the given companion
// registry. Wire its Rebuild method to companion.Registry.SetOnChange and
// install it via Loop.SetDynamicToolSource. The initial snapshot is empty
// until a companion connects and registers capabilities.
func NewCompanionRegistrar(registry *companion.Registry, logger *slog.Logger) *CompanionRegistrar {
	return newCompanionRegistrar(registry.List, registry.Call, logger)
}

// newCompanionRegistrar is the func-injected constructor used by tests to
// supply a fake provider list and caller.
func newCompanionRegistrar(list companionProviderLister, call companionCallFunc, logger *slog.Logger) *CompanionRegistrar {
	if logger == nil {
		logger = slog.Default()
	}
	cr := &CompanionRegistrar{
		list:   list,
		call:   call,
		logger: logger.With("component", "companion_registrar"),
		formatters: map[string]companionResultFormatter{
			// Preserve the pretty calendar prose for the legacy tool name
			// while everything else defaults to JSON passthrough.
			"macos_calendar_events": calendarResultFormatter,
		},
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

	// Dedup by tool name across providers (last writer wins, matching
	// Registry.Register). Order is resolved deterministically below so the
	// per-run tool list — and thus the Anthropic cache prefix — is stable.
	byName := make(map[string]*Tool)
	for _, info := range infos {
		for _, cap := range info.Capabilities {
			for _, def := range cap.Tools {
				tool := cr.synthesize(cap.Name, def)
				if existing := byName[tool.Name]; existing != nil {
					cr.logger.Warn("companion tool name collision; replacing",
						"tool", tool.Name)
				}
				byName[tool.Name] = tool
			}
		}
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	synth := make([]*Tool, 0, len(names))
	tagAdds := make(map[string][]string)
	for _, name := range names {
		tool := byName[name]
		synth = append(synth, tool)
		for _, tag := range tool.Tags {
			tagAdds[tag] = append(tagAdds[tag], name)
		}
	}

	cr.mu.Lock()
	cr.synthesized = synth
	cr.tagAdditions = tagAdds
	cr.mu.Unlock()
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

	result, err := cr.call(ctx, companion.CallRequest{
		Account:    account,
		ClientID:   clientID,
		Capability: capability,
		Method:     method,
		Params:     payload,
	})
	if err != nil {
		return "", err
	}
	return formatter(result)
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

// calendarResultFormatter preserves the human-readable calendar prose for
// the legacy macos_calendar_events tool name.
func calendarResultFormatter(raw json.RawMessage) (string, error) {
	var resp companionCalendarResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("decode companion calendar response: %w", err)
	}
	return formatCompanionCalendarResponse(resp), nil
}
