package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Bridging limits.
const (
	// maxToolNameBytes is the tool-name limit most providers enforce.
	maxToolNameBytes = 64
	// toolNameHashHex is how many hex digits of the name hash a
	// shortened name keeps.
	toolNameHashHex = 8
	// maxToolDescriptionBytes caps a server-authored description, like
	// the companion registrar does for Mac-authored ones.
	maxToolDescriptionBytes = 4096
	// maxToolSchemaBytes caps a tool's input schema as offered to the
	// model (normalized, then marshalled). A larger schema would crowd
	// the prompt of every request that offers the tool, so the tool is
	// not bridged.
	maxToolSchemaBytes = 64 << 10
	// toolSource is tools.Tool.Source for every bridged tool; tag
	// attribution relies on it.
	toolSource = "mcp"
)

// sanitize lowercases name, turns every rune outside [a-z0-9_] into
// '_', collapses runs of '_' and trims leading and trailing '_'. Config
// validation keeps a copy (sanitizeMCPName) to reject server names that
// collide or vanish; a parity test keeps the two in step.
func sanitize(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// thaneToolName is the model-facing name of an MCP tool:
// mcp_{server}_{tool}, sanitized. A name longer than maxToolNameBytes
// is cut and suffixed with a short hash of the raw server/tool pair, so
// it stays stable across restarts and distinct for distinct tools.
func thaneToolName(server, tool string) string {
	name := "mcp_" + sanitize(server) + "_" + sanitize(tool)
	if len(name) <= maxToolNameBytes {
		return name
	}
	sum := sha256.Sum256([]byte(server + "/" + tool))
	suffix := hex.EncodeToString(sum[:])[:toolNameHashHex]
	keep := maxToolNameBytes - 1 - toolNameHashHex
	// The sanitized name is ASCII, so a byte cut is a rune cut.
	return strings.TrimRight(name[:keep], "_") + "_" + suffix
}

// canonicalToolID identifies the raw tool independent of sanitizing.
func canonicalToolID(server, tool string) string {
	return "mcp:" + url.PathEscape(server) + "/" + url.PathEscape(tool)
}

// bridgedTool is one server tool bridged into a Thane tool.
type bridgedTool struct {
	raw  string      // raw MCP tool name
	tool *tools.Tool // immutable once built
	fp   string      // fingerprint of the model-facing definition
}

// serverBridge turns one server's tool list into Thane tools. Its
// methods run under the Manager lock (or on Start's goroutine before
// any rebuild can run), so the log state needs only its own small
// locks for safety.
type serverBridge struct {
	server    string
	include   map[string]bool
	exclude   map[string]bool
	overrides map[string]ToolOverride
	tags      []string // server tags, cleaned
	client    *serverClient
	logger    *slog.Logger

	logged logDeduper // logOnce

	mu          sync.Mutex
	lastUnknown string // the unknown-key set last warned about
}

func newServerBridge(cfg ServerConfig, client *serverClient, logger *slog.Logger) *serverBridge {
	return &serverBridge{
		server:    cfg.Name,
		include:   stringSet(cfg.Include),
		exclude:   stringSet(cfg.Exclude),
		overrides: cfg.ToolOverrides,
		tags:      cleanTags(cfg.Tags),
		client:    client,
		logger:    logger,
		logged:    logDeduper{max: maxLoggedKeys},
	}
}

// maxLoggedKeys bounds logOnce's memory against a server that invents
// endless distinct tool names.
const maxLoggedKeys = 1024

// logOnce logs at level the first time key is seen, at Debug after.
func (b *serverBridge) logOnce(level slog.Level, key, msg string, args ...any) {
	b.logged.log(b.logger, level, key, msg, args...)
}

// included applies the operator filters: an override's enabled flag
// decides when set (true force-includes, false excludes); otherwise an
// include list wins, else the exclude list.
func (b *serverBridge) included(raw string) bool {
	if o, ok := b.overrides[raw]; ok && o.Enabled != nil {
		return *o.Enabled
	}
	if len(b.include) > 0 {
		return b.include[raw]
	}
	return !b.exclude[raw]
}

// bridge converts defs (one server's current list) into Thane tools,
// sorted by raw name. A tool whose schema exceeds maxToolSchemaBytes is
// skipped with a WARN. Two raw names that map to one Thane name keep the
// first in that order; the other is skipped with a WARN. Deterministic
// for a given list.
func (b *serverBridge) bridge(defs []toolDefinition) []bridgedTool {
	b.warnUnknownKeys(defs)
	sorted := make([]toolDefinition, len(defs))
	copy(sorted, defs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	out := make([]bridgedTool, 0, len(sorted))
	taken := make(map[string]string, len(sorted))
	for _, def := range sorted {
		if !b.included(def.Name) {
			continue
		}
		bt, schemaBytes := b.bridgeOne(def)
		if schemaBytes > maxToolSchemaBytes {
			b.logOnce(slog.LevelWarn, "schema-cap:"+def.Name,
				"mcp: tool input schema too large to offer to the model; tool skipped",
				"tool", logText(def.Name), "bytes", schemaBytes, "cap", maxToolSchemaBytes)
			continue
		}
		if first, dup := taken[bt.tool.Name]; dup {
			b.logOnce(slog.LevelWarn, "collide-local:"+def.Name,
				"mcp: two tools from this server map to the same Thane tool name; keeping the first",
				"tool", bt.tool.Name, "kept", logText(first), "skipped", logText(def.Name))
			continue
		}
		taken[bt.tool.Name] = def.Name
		out = append(out, bt)
	}
	return out
}

// bridgeOne builds the Thane tool for one included definition. Its
// handler routes through the live client, so reconnects are invisible
// to callers and a server that is down answers tools.ErrUnavailable.
// schemaBytes is the size of the marshalled input schema.
func (b *serverBridge) bridgeOne(def toolDefinition) (bt bridgedTool, schemaBytes int) {
	raw := def.Name
	name := thaneToolName(b.server, raw)
	override := b.overrides[raw]
	desc := b.description(def, override)
	schema := b.schema(def)

	tags := b.tags
	if t := cleanTags(override.Tags); len(t) > 0 {
		tags = t
	}
	client := b.client
	tool := &tools.Tool{
		Name:        name,
		Description: desc,
		Parameters:  schema,
		CanonicalID: canonicalToolID(b.server, raw),
		Source:      toolSource,
		Origin:      b.server,
		Tags:        append([]string(nil), tags...),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			res, err := client.callTool(ctx, raw, args)
			if err != nil {
				modelErr := modelFacingCallError(name, err)
				b.logCallStatus(name, err, modelErr)
				return "", modelErr
			}
			if res.IsError {
				return "", modelFacingToolError(name, res.Text)
			}
			return res.Text, nil
		},
	}
	params, err := json.Marshal(schema)
	if err != nil {
		params = nil
	}
	return bridgedTool{raw: raw, tool: tool, fp: desc + "\x00" + string(params)}, len(params)
}

// logCallStatus logs the HTTP status details of a failed call (the
// WWW-Authenticate header and body, which the model never sees) once per
// status class: at WARN when the operator has to act, because
// credentials were refused or the model was told the details are in the
// logs; at Debug otherwise.
func (b *serverBridge) logCallStatus(tool string, err, modelErr error) {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return
	}
	const msg = "mcp: tool call failed with an HTTP error status"
	args := append([]any{"tool", tool, "error", err}, httpStatusLogArgs(err)...)
	class := classifyStatus(statusErr.Status)
	var mfe *promptfmt.ModelFacingError
	unusable := errors.As(modelErr, &mfe) && mfe.Text == textUnusable
	if class != statusAuth && !unusable {
		b.logger.Debug(msg, args...)
		return
	}
	b.logOnce(slog.LevelWarn, "call-status:"+class.String(), msg, args...)
}

// description picks the operator override, else the server's text
// (falling back to its title), capped at maxToolDescriptionBytes.
func (b *serverBridge) description(def toolDefinition, override ToolOverride) string {
	desc := strings.TrimSpace(override.Description)
	if desc == "" {
		desc = def.Description
	}
	if strings.TrimSpace(desc) == "" {
		desc = def.Title
	}
	if len(desc) > maxToolDescriptionBytes {
		b.logOnce(slog.LevelWarn, "desc-cap:"+def.Name, "mcp: tool description truncated",
			"tool", logText(def.Name), "bytes", len(desc), "cap", maxToolDescriptionBytes)
		desc = truncateUTF8(desc, maxToolDescriptionBytes)
	}
	return desc
}

// schema normalizes the input schema and logs each distinct change once.
func (b *serverBridge) schema(def toolDefinition) map[string]any {
	schema, changes := normalizeInputSchema(def.InputSchema)
	for _, change := range changes {
		b.logOnce(slog.LevelInfo, "schema:"+def.Name+":"+change,
			"mcp: adjusted a tool's input schema for model-provider compatibility",
			"tool", logText(def.Name), "change", change)
	}
	return schema
}

// warnUnknownKeys warns when include_tools, exclude_tools or tools:
// names a tool the server does not report (usually a typo), once per
// distinct set.
func (b *serverBridge) warnUnknownKeys(defs []toolDefinition) {
	known := make(map[string]struct{}, len(defs))
	for _, d := range defs {
		known[d.Name] = struct{}{}
	}
	unknown := map[string]struct{}{}
	for name := range b.include {
		unknown[name] = struct{}{}
	}
	for name := range b.exclude {
		unknown[name] = struct{}{}
	}
	for name := range b.overrides {
		unknown[name] = struct{}{}
	}
	for name := range known {
		delete(unknown, name)
	}
	names := make([]string, 0, len(unknown))
	for name := range unknown {
		names = append(names, name)
	}
	sort.Strings(names)
	key := strings.Join(names, "\x00")

	b.mu.Lock()
	changed := key != b.lastUnknown
	b.lastUnknown = key
	b.mu.Unlock()
	if changed && len(names) > 0 {
		b.logger.Warn("mcp: include_tools, exclude_tools or tools: names tools the server does not offer; check the spelling against the server's raw tool names",
			"unknown_tools", names, "server_tools", len(defs))
	}
}

// cleanTags trims tags and drops empty and repeated ones, keeping order.
func cleanTags(in []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(in))
	for _, tag := range in {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

// stringSet converts a list to a set; nil for an empty list.
func stringSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}
