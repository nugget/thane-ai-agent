package mcp

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/buildinfo"
)

// Protocol constants shared by every stage.
const (
	modernProtocolVersion = "2026-07-28"
	handshakeOfferVersion = "2025-11-25" // offered in initialize
	// headerVersionFloor: a legacy session sends MCP-Protocol-Version
	// only when the negotiated version is >= this.
	headerVersionFloor     = "2025-06-18"
	clientInfoName         = "thane"
	maxMessageBytes        = 32 << 20 // one stdio line / one SSE event / one JSON body
	maxListPages           = 100
	maxInputRequiredRounds = 3
)

var (
	modernVersions    = []string{modernProtocolVersion}
	handshakeVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}
)

// Per-request _meta keys (modern era only; never sent in legacy).
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// Method names.
const (
	methodDiscover         = "server/discover"
	methodInitialize       = "initialize"
	methodInitialized      = "notifications/initialized"
	methodPing             = "ping"
	methodToolsList        = "tools/list"
	methodToolsCall        = "tools/call"
	methodCancelled        = "notifications/cancelled"
	methodToolsListChanged = "notifications/tools/list_changed"
	methodProgress         = "notifications/progress"
	methodLogMessage       = "notifications/message"
	methodRootsList        = "roots/list"
	methodSampling         = "sampling/createMessage"
	methodElicitation      = "elicitation/create"
)

// JSON-RPC / MCP error codes.
const (
	codeInvalidRequest             = -32600
	codeMethodNotFound             = -32601
	codeInvalidParams              = -32602
	codeHeaderMismatch             = -32020
	codeMissingClientCapability    = -32021
	codeUnsupportedProtocolVersion = -32022
)

// resultType values; absent means complete.
const (
	resultTypeComplete      = "complete"
	resultTypeInputRequired = "input_required"
)

// era is the negotiated protocol generation of one connection.
type era int

const (
	eraUnknown era = iota
	eraLegacy
	eraModern
)

// String returns "unknown", "legacy" or "modern".
func (e era) String() string {
	switch e {
	case eraLegacy:
		return "legacy"
	case eraModern:
		return "modern"
	default:
		return "unknown"
	}
}

// isHandshakeVersion reports whether v is an initialize-handshake
// protocol version Thane speaks.
func isHandshakeVersion(v string) bool { return slices.Contains(handshakeVersions, v) }

// versionAtLeast compares YYYY-MM-DD version strings lexically.
func versionAtLeast(v, floor string) bool { return v >= floor }

// highestMutualVersion returns the newest version present in both
// offered and ours, and false when they share none. Versions are
// YYYY-MM-DD strings, so lexical order is chronological order.
func highestMutualVersion(offered, ours []string) (string, bool) {
	best := ""
	for _, v := range offered {
		if slices.Contains(ours, v) && v > best {
			best = v
		}
	}
	return best, best != ""
}

// implementation is MCP's Implementation object (clientInfo/serverInfo).
type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

// thaneClientInfo returns {Name: clientInfoName, Version: buildinfo.Version}.
func thaneClientInfo() implementation {
	return implementation{Name: clientInfoName, Version: buildinfo.Version}
}

// modernMeta returns a fresh _meta map with exactly the three modern
// keys: protocolVersion=modernProtocolVersion, clientInfo=thaneClientInfo(),
// clientCapabilities={} (Thane declares no client capabilities).
func modernMeta() map[string]any {
	return map[string]any{
		metaProtocolVersion:    modernProtocolVersion,
		metaClientInfo:         thaneClientInfo(),
		metaClientCapabilities: map[string]any{},
	}
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type serverCapabilities struct {
	Tools *toolsCapability `json:"tools,omitempty"`
}

// discoverResult: valid (A1) iff it unmarshals, Capabilities != nil and
// SupportedVersions shares a version with modernVersions.
type discoverResult struct {
	ResultType        string                     `json:"resultType,omitempty"`
	SupportedVersions []string                   `json:"supportedVersions"`
	Capabilities      *serverCapabilities        `json:"capabilities"`
	Instructions      string                     `json:"instructions,omitempty"`
	TTLMs             *int64                     `json:"ttlMs,omitempty"`
	CacheScope        string                     `json:"cacheScope,omitempty"`
	Meta              map[string]json.RawMessage `json:"_meta,omitempty"`
}

// serverInfo returns the io.modelcontextprotocol/serverInfo object from
// the result's _meta, and false when it is absent or malformed.
func (r *discoverResult) serverInfo() (implementation, bool) {
	raw, ok := r.Meta[metaServerInfo]
	if !ok {
		return implementation{}, false
	}
	var info implementation
	if err := json.Unmarshal(raw, &info); err != nil {
		return implementation{}, false
	}
	return info, true
}

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// unsupportedVersionData is error.data for -32022 (and the A11 shape of
// an initialize error carrying data.supported).
type unsupportedVersionData struct {
	Supported []string `json:"supported"`
	Requested string   `json:"requested,omitempty"`
}

// toolsListResult keeps each tool raw so one malformed definition is
// skipped (WARN) instead of failing the page. NextCursor nil = end;
// "" is a valid cursor.
type toolsListResult struct {
	ResultType string            `json:"resultType,omitempty"`
	Tools      []json.RawMessage `json:"tools"`
	NextCursor *string           `json:"nextCursor,omitempty"`
	TTLMs      *int64            `json:"ttlMs,omitempty"`
	CacheScope string            `json:"cacheScope,omitempty"`
}

// toolDefinition is one MCP Tool. Annotations are untrusted hints that
// are kept for reference only; Thane policy never relaxes on them.
type toolDefinition struct {
	Name         string                     `json:"name"`
	Title        string                     `json:"title,omitempty"`
	Description  string                     `json:"description,omitempty"`
	InputSchema  map[string]any             `json:"inputSchema"`
	OutputSchema json.RawMessage            `json:"outputSchema,omitempty"`
	Annotations  *toolAnnotations           `json:"annotations,omitempty"`
	Meta         map[string]json.RawMessage `json:"_meta,omitempty"`
}

type toolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// callToolResult covers CallToolResult and InputRequiredResult.
// StructuredContent of JSON null counts as absent. Content blocks stay
// raw so a malformed block renders as a marker instead of failing.
type callToolResult struct {
	ResultType        string                     `json:"resultType,omitempty"`
	Content           []json.RawMessage          `json:"content"`
	StructuredContent json.RawMessage            `json:"structuredContent,omitempty"`
	IsError           bool                       `json:"isError,omitempty"`
	InputRequests     map[string]json.RawMessage `json:"inputRequests,omitempty"`
	RequestState      *string                    `json:"requestState,omitempty"`
}

// contentBlock is the decoded union of text, image, audio,
// resource_link and resource blocks.
type contentBlock struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	Data     string            `json:"data,omitempty"`
	MimeType string            `json:"mimeType,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Name     string            `json:"name,omitempty"`
	Resource *embeddedResource `json:"resource,omitempty"`
}

type embeddedResource struct {
	URI      string  `json:"uri"`
	MimeType string  `json:"mimeType,omitempty"`
	Text     *string `json:"text,omitempty"`
	Blob     *string `json:"blob,omitempty"`
}

// Base64 sentinel markers for header values (streamable-http
// "Value Encoding"). Case-sensitive.
const (
	headerB64Prefix = "=?base64?"
	headerB64Suffix = "?="
)

// encodeHeaderValue returns v unchanged when it is only visible ASCII,
// space and tab with no leading/trailing whitespace and does not already
// look like the sentinel; otherwise "=?base64?" + StdEncoding(v) + "?=".
func encodeHeaderValue(v string) string {
	if headerValueSafe(v) {
		return v
	}
	return headerB64Prefix + base64.StdEncoding.EncodeToString([]byte(v)) + headerB64Suffix
}

// headerValueSafe reports whether v may be sent as a plain header value.
func headerValueSafe(v string) bool {
	if v == "" {
		return true
	}
	if headerSpace(v[0]) || headerSpace(v[len(v)-1]) {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < 0x21 || c > 0x7E) && !headerSpace(c) {
			return false
		}
	}
	// A plain value that looks like the sentinel must be encoded too.
	return !strings.HasPrefix(v, headerB64Prefix) || !strings.HasSuffix(v, headerB64Suffix)
}

func headerSpace(c byte) bool { return c == ' ' || c == '\t' }

// isHeaderToken reports RFC 9110 token syntax (1*tchar).
func isHeaderToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !headerTChar(s[i]) {
			return false
		}
	}
	return true
}

// headerTChar reports RFC 9110 tchar: "!" / "#" / "$" / "%" / "&" / "'" /
// "*" / "+" / "-" / "." / "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA.
func headerTChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}
