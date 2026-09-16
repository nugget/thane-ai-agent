package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/libdns/libdns"

	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

// linodeProvider solves DNS-01 challenges against the Linode DNS API
// (v4 domains and records endpoints) through the libdns interfaces
// certmagic consumes. It is written in-tree rather than taken from the
// libdns/linode module because that module, at v0.5.0, swallows every
// per-record creation error and returns success with nothing created,
// so an expired token surfaces only as a propagation timeout minutes
// later; and its first call replaces the process-wide slog default with
// a stdout text handler, which would silently detach Thane's logging
// pipeline after the first issuance. This implementation returns every
// API error to the caller and logs only through the logger it is given.
//
// Settings are the same field names the upstream provider documents, so
// a config written against it keeps working.
type linodeProvider struct {
	// APIToken is a Linode personal access token with domains read/write.
	APIToken string `json:"api_token"`
	// APIURL overrides the API origin; default https://api.linode.com.
	APIURL string `json:"api_url,omitempty"`
	// APIVersion overrides the API version path segment; default v4.
	APIVersion string `json:"api_version,omitempty"`

	client *http.Client
	logger *slog.Logger
}

// linodeAPIPageSize is the largest page the records endpoint serves.
const linodeAPIPageSize = 500

func (p *linodeProvider) ready(logger *slog.Logger) {
	if p.client == nil {
		p.client = httpkit.NewClient()
	}
	if p.logger == nil {
		if logger == nil {
			logger = slog.Default()
		}
		p.logger = logger.With("subsystem", "tls", "dns_provider", "linode")
	}
	if p.APIURL == "" {
		p.APIURL = "https://api.linode.com"
	}
	if p.APIVersion == "" {
		p.APIVersion = "v4"
	}
}

// AppendRecords creates every record in the zone and returns those
// created. The first failure aborts and is returned; records created
// before it stay in place for DeleteRecords to clean up, which is how
// certmagic treats a failed presentation.
func (p *linodeProvider) AppendRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	domainID, err := p.domainID(ctx, zone)
	if err != nil {
		return nil, err
	}
	added := make([]libdns.Record, 0, len(records))
	for _, record := range records {
		rr := record.RR()
		body := linodeRecord{
			Type:   rr.Type,
			Name:   linodeRelativeName(rr.Name),
			Target: rr.Data,
			TTLSec: int(rr.TTL / time.Second),
		}
		var created linodeRecord
		if err := p.call(ctx, http.MethodPost, fmt.Sprintf("/domains/%d/records", domainID), body, &created); err != nil {
			return added, fmt.Errorf("linode: create %s record %q in %s: %w", rr.Type, rr.Name, zone, err)
		}
		p.logger.Debug("dns record created", "zone", zone, "name", rr.Name, "type", rr.Type, "record_id", created.ID)
		added = append(added, created.libdns(zone))
	}
	return added, nil
}

// DeleteRecords deletes the records in the zone that match the inputs on
// name, type, and data (and TTL when the input carries one), returning
// those deleted. Inputs with no match are ignored, per the libdns
// contract.
func (p *linodeProvider) DeleteRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	domainID, err := p.domainID(ctx, zone)
	if err != nil {
		return nil, err
	}
	existing, err := p.listRecords(ctx, domainID)
	if err != nil {
		return nil, err
	}
	deleted := make([]libdns.Record, 0, len(records))
	gone := make(map[int]bool, len(records))
	for _, record := range records {
		want := record.RR()
		for _, have := range existing {
			if gone[have.ID] || !have.matches(want) {
				continue
			}
			if err := p.call(ctx, http.MethodDelete, fmt.Sprintf("/domains/%d/records/%d", domainID, have.ID), nil, nil); err != nil {
				return deleted, fmt.Errorf("linode: delete %s record %q in %s: %w", want.Type, want.Name, zone, err)
			}
			gone[have.ID] = true
			p.logger.Debug("dns record deleted", "zone", zone, "name", want.Name, "type", want.Type, "record_id", have.ID)
			deleted = append(deleted, have.libdns(zone))
		}
	}
	return deleted, nil
}

// domainID resolves a libdns zone (absolute, trailing dot) to the Linode
// domain that serves it.
func (p *linodeProvider) domainID(ctx context.Context, zone string) (int, error) {
	domain := strings.TrimSuffix(libdns.AbsoluteName("@", zone), ".")
	filter, err := json.Marshal(map[string]string{"domain": domain})
	if err != nil {
		return 0, fmt.Errorf("linode: encode domain filter: %w", err)
	}
	var page struct {
		Data []struct {
			ID     int    `json:"id"`
			Domain string `json:"domain"`
		} `json:"data"`
	}
	req := func(r *http.Request) { r.Header.Set("X-Filter", string(filter)) }
	if err := p.callWith(ctx, http.MethodGet, "/domains", nil, &page, req); err != nil {
		return 0, fmt.Errorf("linode: look up zone %q: %w", domain, err)
	}
	switch len(page.Data) {
	case 0:
		return 0, fmt.Errorf("linode: zone %q is not a domain on this account", domain)
	case 1:
		return page.Data[0].ID, nil
	default:
		return 0, fmt.Errorf("linode: zone %q matched %d domains", domain, len(page.Data))
	}
}

// listRecords pages through every record in the domain.
func (p *linodeProvider) listRecords(ctx context.Context, domainID int) ([]linodeRecord, error) {
	var all []linodeRecord
	for pageNum := 1; ; pageNum++ {
		var page struct {
			Data  []linodeRecord `json:"data"`
			Pages int            `json:"pages"`
		}
		path := fmt.Sprintf("/domains/%d/records?page=%d&page_size=%d", domainID, pageNum, linodeAPIPageSize)
		if err := p.call(ctx, http.MethodGet, path, nil, &page); err != nil {
			return nil, fmt.Errorf("linode: list records: %w", err)
		}
		all = append(all, page.Data...)
		if pageNum >= page.Pages || len(page.Data) == 0 {
			return all, nil
		}
	}
}

func (p *linodeProvider) call(ctx context.Context, method, path string, body, out any) error {
	return p.callWith(ctx, method, path, body, out, nil)
}

// callWith performs one API request, returning any non-2xx response as
// an error carrying the API's own reason text so an expired token reads
// as an expired token.
func (p *linodeProvider) callWith(ctx context.Context, method, path string, body, out any, decorate func(*http.Request)) error {
	p.ready(nil)
	endpoint, err := url.JoinPath(p.APIURL, p.APIVersion, path)
	if err != nil {
		return fmt.Errorf("build url: %w", err)
	}
	// JoinPath escapes the query separator; restore it for list calls.
	endpoint = strings.Replace(endpoint, "%3F", "?", 1)
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.APIToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if decorate != nil {
		decorate(req)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %s", method, path, linodeErrorText(resp))
	}
	if out == nil {
		httpkit.DrainAndClose(resp.Body, 64<<10)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// linodeErrorText renders the API's error envelope, falling back to the
// status line when the body is not the documented shape.
func linodeErrorText(resp *http.Response) string {
	raw := httpkit.ReadErrorBody(resp.Body, 16<<10)
	var envelope struct {
		Errors []struct {
			Reason string `json:"reason"`
			Field  string `json:"field"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err == nil && len(envelope.Errors) > 0 {
		reasons := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			if e.Field != "" {
				reasons = append(reasons, e.Field+": "+e.Reason)
			} else {
				reasons = append(reasons, e.Reason)
			}
		}
		return resp.Status + ": " + strings.Join(reasons, "; ")
	}
	return resp.Status
}

// linodeRecord is the API's record shape, for both requests and
// responses.
type linodeRecord struct {
	ID     int    `json:"id,omitempty"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Target string `json:"target"`
	TTLSec int    `json:"ttl_sec"`
}

// libdns converts an API record to the libdns type certmagic expects.
func (r linodeRecord) libdns(zone string) libdns.Record {
	name := r.Name
	if name == "" {
		name = "@"
	}
	ttl := time.Duration(r.TTLSec) * time.Second
	switch strings.ToUpper(r.Type) {
	case "TXT":
		return libdns.TXT{Name: name, TTL: ttl, Text: r.Target, ProviderData: strconv.Itoa(r.ID)}
	default:
		return libdns.RR{Name: name, TTL: ttl, Type: r.Type, Data: r.Target}
	}
}

// matches reports whether the API record is the one a libdns input
// describes: same relative name, type, and data, and the same TTL when
// the input specifies one.
func (r linodeRecord) matches(want libdns.RR) bool {
	if !strings.EqualFold(r.Type, want.Type) || r.Target != want.Data {
		return false
	}
	if r.Name != linodeRelativeName(want.Name) {
		return false
	}
	if want.TTL != 0 && int(want.TTL/time.Second) != r.TTLSec {
		return false
	}
	return true
}

// linodeRelativeName maps libdns's apex marker to the empty name Linode
// uses for the zone root.
func linodeRelativeName(name string) string {
	if name == "@" {
		return ""
	}
	return name
}
