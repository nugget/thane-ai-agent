package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Transport kinds as clientConfig.transport and ServerConfig.Transport
// spell them. They select the stdio or HTTP probe rules.
const (
	transportStdio = "stdio"
	transportHTTP  = "http"
)

// verdictStep is what negotiation does next after one server/discover
// or initialize reply.
type verdictStep int

const (
	// verdictFail: this connect attempt fails and backs off; the era
	// stays as it was. err is set.
	verdictFail verdictStep = iota
	// verdictModern: speak the modern era at version. discover is set.
	verdictModern
	// verdictLegacy: initialize succeeded; speak the legacy era at
	// version. initialize is set. Only classifyInitialize returns it.
	verdictLegacy
	// verdictInitialize: send initialize offering version, on the same
	// transport. It is the legacy fallback: no mutual modern version, a
	// non-modern error, a version list naming only handshake versions,
	// or a stdio probe timeout.
	verdictInitialize
	// verdictDiscover: send server/discover again at version, because a
	// -32022 to discover or initialize named a mutual modern version.
	verdictDiscover
	// verdictRespawn: stdio only. The process exited or closed stdout
	// during the probe, as an old server may on an unknown method
	// before initialize: build a new transport once and go straight
	// to initialize offering version.
	verdictRespawn
)

// String returns a short name for logs.
func (s verdictStep) String() string {
	switch s {
	case verdictModern:
		return "modern"
	case verdictLegacy:
		return "legacy"
	case verdictInitialize:
		return "initialize"
	case verdictDiscover:
		return "discover"
	case verdictRespawn:
		return "respawn"
	default:
		return "fail"
	}
}

// eraVerdict is the pure outcome of one negotiation reply. The Client
// owns retry bounds (at most one verdictDiscover/verdictInitialize retry
// and one verdictRespawn per attempt) and all I/O.
type eraVerdict struct {
	step       verdictStep
	version    string            // verdictModern/verdictLegacy: negotiated; retries: version to offer
	discover   *discoverResult   // verdictModern only
	initialize *initializeResult // verdictLegacy only
	err        error             // verdictFail only; wraps an errors.go class where one fits

	// thaneBug is true when the server blamed Thane's own framing
	// (-32020 HeaderMismatch, HTTP 406 or 415). Log it at ERROR: no
	// retry fixes it.
	thaneBug bool
}

// supportedListCap bounds how many server-supplied versions an error
// names, and versionCapBytes how long each may be.
const (
	supportedListCap = 8
	versionCapBytes  = 32
)

// classifyDiscover classifies the reply to a server/discover probe
// (2026-07-28 versioning.mdx and the stdio and streamable-http
// "Backward Compatibility" sections). transport is transportStdio or
// transportHTTP; any other value gets the HTTP rules.
//
// A DiscoverResult with a mutual modern version is modern; one naming
// only handshake versions, or a result that does not parse, is legacy.
// A recognized modern error (-32020, -32021, -32022) is modern; any
// other JSON-RPC error is legacy. On stdio a probe timeout is legacy on
// the same process, and an exit or EOF respawns once for initialize. On
// HTTP only a 2xx body or the body of a 400/404/405 decides the era:
// network errors, 401, 403, 408, 429 and 5xx are outages or auth
// failures, and 406 and 415 are Thane's own bugs, so none is a verdict.
//
// negotiate drives the connection with this function and
// classifyInitialize, so the rules live in one place.
func classifyDiscover(transport string, resp *Response, err error) eraVerdict {
	if err != nil {
		return classifyDiscoverErr(transport, err)
	}
	if resp == nil {
		return failVerdict(fmt.Errorf("%w: no response", errProtocol))
	}
	if resp.Error != nil {
		return classifyDiscoverRPC(resp.Error)
	}
	var res discoverResult
	if json.Unmarshal(resp.Result, &res) != nil || res.Capabilities == nil {
		// Not a DiscoverResult: no modern evidence, so legacy.
		return initializeVerdict(handshakeOfferVersion)
	}
	if v, ok := highestMutualVersion(res.SupportedVersions, modernVersions); ok {
		return eraVerdict{step: verdictModern, version: v, discover: &res}
	}
	// A discover-answering server that lists only handshake versions is
	// a legacy advertisement, since those versions are reachable only
	// through initialize: initialize at the best of them.
	if v, ok := highestMutualVersion(res.SupportedVersions, handshakeVersions); ok {
		return initializeVerdict(v)
	}
	return initializeVerdict(handshakeOfferVersion)
}

func classifyDiscoverErr(transport string, err error) eraVerdict {
	if errors.Is(err, context.Canceled) {
		return failVerdict(err)
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		switch classifyStatus(statusErr.Status) {
		case statusEraEvidence:
			if statusErr.RPC != nil {
				return classifyDiscoverRPC(statusErr.RPC)
			}
			// An empty or non-JSON 400/404/405 is a legacy server
			// rejecting an unknown method or missing session
			// (streamable-http "Backward Compatibility").
			return initializeVerdict(handshakeOfferVersion)
		case statusThaneBug:
			return eraVerdict{step: verdictFail, err: err, thaneBug: true}
		default:
			return failVerdict(err)
		}
	}
	if transport == transportStdio {
		switch {
		case errors.Is(err, errConnectionLost), errors.Is(err, errUnreachable):
			return eraVerdict{step: verdictRespawn, version: handshakeOfferVersion}
		case errors.Is(err, context.DeadlineExceeded):
			// A probe timeout means legacy, on the same process. A slow
			// modern server that answers discover late then answers
			// initialize with -32022, which re-probes.
			return initializeVerdict(handshakeOfferVersion)
		}
	}
	return failVerdict(err)
}

// classifyDiscoverRPC classifies a JSON-RPC error answering discover.
// Only the three modern-only codes are modern evidence; every other code
// means legacy, because the stdio fallback "MUST NOT be keyed to one
// specific error code".
func classifyDiscoverRPC(rpcErr *RPCError) eraVerdict {
	switch rpcErr.Code {
	case codeUnsupportedProtocolVersion:
		return classifyUnsupportedDiscover(rpcErr)
	case codeMissingClientCapability:
		return failVerdict(fmt.Errorf("%w: the server requires client capabilities Thane does not declare: %w", errIncompatible, rpcErr))
	case codeHeaderMismatch:
		return eraVerdict{
			step:     verdictFail,
			err:      fmt.Errorf("%w: the server rejected Thane's request headers: %w", errProtocol, rpcErr),
			thaneBug: true,
		}
	default:
		return initializeVerdict(handshakeOfferVersion)
	}
}

// classifyUnsupportedDiscover handles a -32022 answering discover: retry
// discover at a mutual modern version in data.supported, else initialize
// at a mutual handshake version, else fail naming the server's list. An
// unparseable data falls back to initialize.
func classifyUnsupportedDiscover(rpcErr *RPCError) eraVerdict {
	supported, ok := parseSupportedVersions(rpcErr.Data)
	if !ok {
		return initializeVerdict(handshakeOfferVersion)
	}
	if v, ok := highestMutualVersion(supported, modernVersions); ok {
		return eraVerdict{step: verdictDiscover, version: v}
	}
	if v, ok := highestMutualVersion(supported, handshakeVersions); ok {
		return initializeVerdict(v)
	}
	return failVerdict(unsupportedVersionsErr(supported, rpcErr))
}

// classifyInitialize classifies the reply to the legacy initialize
// handshake (2025-11-25 lifecycle.mdx "Version Negotiation"). A result
// must name a handshake version Thane speaks; errors go to
// classifyInitializeRPC.
func classifyInitialize(resp *Response, err error) eraVerdict {
	if err != nil {
		return classifyInitializeErr(err)
	}
	if resp == nil {
		return failVerdict(fmt.Errorf("%w: no response", errProtocol))
	}
	if resp.Error != nil {
		return classifyInitializeRPC(resp.Error)
	}
	var res initializeResult
	if uerr := json.Unmarshal(resp.Result, &res); uerr != nil {
		return failVerdict(fmt.Errorf("%w: malformed result: %w", errProtocol, uerr))
	}
	if !isHandshakeVersion(res.ProtocolVersion) {
		return failVerdict(fmt.Errorf("%w: the server chose protocol version %q",
			errIncompatible, truncateUTF8(res.ProtocolVersion, versionCapBytes)))
	}
	return eraVerdict{step: verdictLegacy, version: res.ProtocolVersion, initialize: &res}
}

func classifyInitializeErr(err error) eraVerdict {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return failVerdict(err)
	}
	switch classifyStatus(statusErr.Status) {
	case statusEraEvidence:
		if statusErr.RPC != nil {
			return classifyInitializeRPC(statusErr.RPC)
		}
		return failVerdict(fmt.Errorf("%w (the server may use the deprecated HTTP+SSE transport, which Thane does not support; point url at a Streamable HTTP endpoint)", err))
	case statusThaneBug:
		return eraVerdict{step: verdictFail, err: err, thaneBug: true}
	default:
		return failVerdict(err)
	}
}

// classifyInitializeRPC classifies an error answering initialize. A
// -32022 naming a modern version is modern evidence from a dual-era
// server that already locked the connection to modern, so re-probe
// discover. Any error whose data.supported names a handshake version
// Thane speaks gets one re-offer of that version.
func classifyInitializeRPC(rpcErr *RPCError) eraVerdict {
	unsupported := rpcErr.Code == codeUnsupportedProtocolVersion
	supported, ok := parseSupportedVersions(rpcErr.Data)
	switch {
	case ok:
		if v, mutual := highestMutualVersion(supported, modernVersions); mutual && unsupported {
			return eraVerdict{step: verdictDiscover, version: v}
		}
		if v, mutual := highestMutualVersion(supported, handshakeVersions); mutual {
			return initializeVerdict(v)
		}
		return failVerdict(unsupportedVersionsErr(supported, rpcErr))
	case unsupported:
		return failVerdict(fmt.Errorf("%w: %w", errIncompatible, rpcErr))
	default:
		return failVerdict(fmt.Errorf("rejected: %w", rpcErr))
	}
}

// parseSupportedVersions decodes error.data.supported. ok is false when
// data is absent, malformed, or has no "supported" list; an explicit
// empty list is returned as ok.
func parseSupportedVersions(data json.RawMessage) ([]string, bool) {
	if len(data) == 0 {
		return nil, false
	}
	var d unsupportedVersionData
	if err := json.Unmarshal(data, &d); err != nil || d.Supported == nil {
		return nil, false
	}
	return d.Supported, true
}

// unsupportedVersionsErr names the server's versions for the operator.
func unsupportedVersionsErr(supported []string, rpcErr *RPCError) error {
	shown := supported
	if len(shown) > supportedListCap {
		shown = shown[:supportedListCap]
	}
	quoted := make([]string, len(shown))
	for i, v := range shown {
		quoted[i] = fmt.Sprintf("%q", truncateUTF8(v, versionCapBytes))
	}
	list := "[" + strings.Join(quoted, ", ")
	if len(supported) > len(shown) {
		list += ", …"
	}
	list += "]"
	return fmt.Errorf("%w: Thane speaks %s and %s, the server supports %s: %w",
		errIncompatible, modernProtocolVersion, strings.Join(handshakeVersions, ", "), list, rpcErr)
}

func failVerdict(err error) eraVerdict { return eraVerdict{step: verdictFail, err: err} }

func initializeVerdict(version string) eraVerdict {
	return eraVerdict{step: verdictInitialize, version: version}
}
