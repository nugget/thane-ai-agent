package logging

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// WholePromptSegment names the single segment a system prompt is
// stored as when its sections cannot be located in its text. A call
// stored this way is replayable, but its prompt has no section
// boundaries a treatment could act on.
const WholePromptSegment = "WHOLE"

// PromptSegment is one contiguous piece of a captured system prompt.
// Concatenating a call's segments in order reproduces the prompt byte
// for byte.
type PromptSegment struct {
	// Name is the prompt section this segment holds, empty for the
	// separator text between two sections, or [WholePromptSegment].
	Name string `json:"name"`
	// Hash addresses the segment's bytes in log_model_blobs.
	Hash string `json:"hash"`
	// CacheTTL is the section's provider cache hint, empty when the
	// section asked for none. Separators never carry one.
	CacheTTL string `json:"cache_ttl,omitempty"`
}

// ServingProfile is the part of a call's serving configuration that
// decides how the model saw the request. Two calls with equal profile
// hashes were served the same way, whatever build or process sent
// them; build, TTL and loaded context are provenance, not profile.
type ServingProfile struct {
	Provider      string `json:"provider"`
	Resource      string `json:"resource"`
	UpstreamModel string `json:"upstream_model"`
	// ChatTemplateKwargs are the kwargs as encoded on the wire. Nil
	// means the provider did not report them, which hashes apart from
	// an empty map, meaning none were sent.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
}

// canonicalMessage is the stored form of one non-system message. It
// keeps everything the provider encodes except image bytes, which are
// replaced by a digest.
type canonicalMessage struct {
	Role       string              `json:"role"`
	Content    string              `json:"content"`
	ToolCalls  []canonicalToolCall `json:"tool_calls,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	Images     []canonicalImage    `json:"images,omitempty"`
}

type canonicalToolCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// canonicalImage stands in for image bytes the store does not keep.
// A message carrying one makes its call image-dependent.
type canonicalImage struct {
	MediaType string `json:"media_type"`
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
}

// canonicalJSON encodes v deterministically: map keys sorted (as
// encoding/json always does), no HTML escaping, no trailing newline.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// blobHash returns the lowercase hex sha256 that addresses b.
func blobHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// blobSet collects the content-addressed blobs one call references,
// in first-reference order, deduplicated by hash.
type blobSet struct {
	order []string
	data  map[string][]byte
}

func newBlobSet() *blobSet {
	return &blobSet{data: make(map[string][]byte)}
}

// add stores b and returns its hash.
func (s *blobSet) add(b []byte) string {
	h := blobHash(b)
	if _, ok := s.data[h]; !ok {
		s.data[h] = b
		s.order = append(s.order, h)
	}
	return h
}

// addJSON canonicalizes v, stores it and returns its hash.
func (s *blobSet) addJSON(v any) (string, error) {
	b, err := canonicalJSON(v)
	if err != nil {
		return "", err
	}
	return s.add(b), nil
}

// canonicalizeMessage converts msg to its stored form and reports
// whether it carried images.
func canonicalizeMessage(msg llm.Message) (canonicalMessage, bool) {
	cm := canonicalMessage{
		Role:       msg.Role,
		Content:    msg.Content,
		ToolCallID: msg.ToolCallID,
	}
	for _, tc := range msg.ToolCalls {
		cm.ToolCalls = append(cm.ToolCalls, canonicalToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	for _, img := range msg.Images {
		sum := sha256.Sum256([]byte(img.Data))
		cm.Images = append(cm.Images, canonicalImage{
			MediaType: img.MediaType,
			SHA256:    hex.EncodeToString(sum[:]),
			Bytes:     len(img.Data),
		})
	}
	return cm, len(msg.Images) > 0
}

// segmentPrompt splits a system prompt into ordered segments: each
// section where it is found from a moving cursor, and the separator
// text between sections as unnamed segments. It reports false, and a
// single [WholePromptSegment], when a section cannot be found or the
// segments do not re-concatenate to content byte for byte. A prompt
// without sections is one [WholePromptSegment] and is not a fallback.
func segmentPrompt(content string, sections []llm.PromptSection) (segs []llm.PromptSection, ok bool) {
	whole := []llm.PromptSection{{Name: WholePromptSegment, Content: content}}
	if len(sections) == 0 {
		return whole, true
	}
	cursor := 0
	for _, sec := range sections {
		idx := strings.Index(content[cursor:], sec.Content)
		if idx < 0 {
			return whole, false
		}
		if idx > 0 {
			segs = append(segs, llm.PromptSection{Content: content[cursor : cursor+idx]})
		}
		segs = append(segs, llm.PromptSection{Name: sec.Name, Content: sec.Content, CacheTTL: sec.CacheTTL})
		cursor += idx + len(sec.Content)
	}
	if cursor < len(content) {
		segs = append(segs, llm.PromptSection{Content: content[cursor:]})
	}
	var rebuilt strings.Builder
	rebuilt.Grow(len(content))
	for _, s := range segs {
		rebuilt.WriteString(s.Content)
	}
	if rebuilt.String() != content {
		return whole, false
	}
	return segs, true
}

// stablePrefixHash digests the part of a request a provider prefix
// cache can reuse across calls: the tool surface, then the prompt
// segments up to and including the last named section of the leading
// run of sections that carry a cache TTL. The separators inside that
// run are included; the one after it is not. With no leading cached
// section the prefix is the tool surface alone.
func stablePrefixHash(surfaceHash string, segs []PromptSegment) string {
	end := 0
	for i, s := range segs {
		if s.Name == "" {
			continue
		}
		if s.CacheTTL == "" {
			break
		}
		end = i + 1
	}
	var b strings.Builder
	b.WriteString(surfaceHash)
	for _, s := range segs[:end] {
		b.WriteByte('\n')
		b.WriteString(s.Hash)
	}
	return blobHash([]byte(b.String()))
}

// hashedCall is everything capture derives from one call's inputs: the
// row's hash columns plus the blobs they reference.
type hashedCall struct {
	segments         []PromptSegment
	segmentFallback  bool
	messageHashes    []string
	toolSurfaceHash  string
	toolHashes       []string
	stablePrefixHash string // empty when the prompt fell back to WHOLE
	profileHash      string
	responseHash     string // empty when there was no response
	imageDependent   bool
	blobs            *blobSet
}

// hashModelCall canonicalizes and hashes one call's inputs. It runs
// synchronously at the call boundary, so everything it returns is
// owned bytes the caller may mutate its inputs after.
//
// The first message, when it is a system message, is stored as prompt
// segments; every other message, including any later system message,
// is hashed whole. The tool surface hash covers the canonical JSON of
// the full definitions array in offered order, descriptions and
// parameters included. That array is exactly "[" + the per-tool blobs
// joined by "," + "]", so it is reconstructible from them.
func hashModelCall(messages []llm.Message, tools []map[string]any, response *llm.Message, profile ServingProfile) (hashedCall, error) {
	hc := hashedCall{blobs: newBlobSet()}

	rest := messages
	if len(messages) > 0 && messages[0].Role == "system" {
		segs, ok := segmentPrompt(messages[0].Content, messages[0].Sections)
		hc.segmentFallback = !ok
		hc.segments = make([]PromptSegment, 0, len(segs))
		for _, s := range segs {
			hc.segments = append(hc.segments, PromptSegment{
				Name:     s.Name,
				Hash:     hc.blobs.add([]byte(s.Content)),
				CacheTTL: s.CacheTTL,
			})
		}
		rest = messages[1:]
	}

	hc.messageHashes = make([]string, 0, len(rest))
	for i, msg := range rest {
		cm, hasImages := canonicalizeMessage(msg)
		hc.imageDependent = hc.imageDependent || hasImages
		h, err := hc.blobs.addJSON(cm)
		if err != nil {
			return hashedCall{}, fmt.Errorf("canonicalize message %d: %w", i, err)
		}
		hc.messageHashes = append(hc.messageHashes, h)
	}

	surface := []byte{'['}
	hc.toolHashes = make([]string, 0, len(tools))
	for i, def := range tools {
		b, err := canonicalJSON(def)
		if err != nil {
			return hashedCall{}, fmt.Errorf("canonicalize tool %d: %w", i, err)
		}
		hc.toolHashes = append(hc.toolHashes, hc.blobs.add(b))
		if i > 0 {
			surface = append(surface, ',')
		}
		surface = append(surface, b...)
	}
	hc.toolSurfaceHash = blobHash(append(surface, ']'))

	var err error
	if !hc.segmentFallback {
		hc.stablePrefixHash = stablePrefixHash(hc.toolSurfaceHash, hc.segments)
	}

	if hc.profileHash, err = hc.blobs.addJSON(profile); err != nil {
		return hashedCall{}, fmt.Errorf("canonicalize serving profile: %w", err)
	}

	if response != nil {
		cm, _ := canonicalizeMessage(*response)
		if hc.responseHash, err = hc.blobs.addJSON(cm); err != nil {
			return hashedCall{}, fmt.Errorf("canonicalize response: %w", err)
		}
	}
	return hc, nil
}
