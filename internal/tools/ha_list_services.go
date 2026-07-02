package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

const haListServicesTruncationNote = "Result exceeded the tool byte cap; pass domain (and optionally service) to narrow to the detail you need."

// haServicesIndex is the no-argument shape: a compact directory of
// every domain and its service names, sized to answer "what can I
// call" without shipping every field schema in the install.
type haServicesIndex struct {
	DomainCount int                      `json:"domain_count"`
	Truncated   bool                     `json:"truncated,omitempty"`
	Note        string                   `json:"note"`
	Domains     []haServiceDomainSummary `json:"domains"`
}

type haServiceDomainSummary struct {
	Domain   string   `json:"domain"`
	Services []string `json:"services"`
}

// haServicesDetail is the domain- or service-scoped shape: full field
// schemas and target support for the requested slice of the catalog.
type haServicesDetail struct {
	Domain    string            `json:"domain"`
	Count     int               `json:"count"`
	Truncated bool              `json:"truncated,omitempty"`
	Services  []haServiceDetail `json:"services"`
}

type haServiceDetail struct {
	// Service is the callable domain.service identifier — exactly what
	// ha_call_service takes.
	Service       string           `json:"service"`
	Name          string           `json:"name,omitempty"`
	Description   string           `json:"description,omitempty"`
	AcceptsTarget bool             `json:"accepts_target"`
	Fields        []haServiceField `json:"fields,omitempty"`
}

type haServiceField struct {
	Field       string `json:"field"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Example     any    `json:"example,omitempty"`
}

// registerHAListServices wires the ha_list_services tool: the service
// catalog as a discovery surface, so a caller learns what
// ha_call_service can do — names, fields, target support — instead of
// guessing and burning a failed call. Progressive disclosure keeps the
// payload honest: the bare call returns a name directory; domain (and
// service) scope in the full field schemas.
func (r *Registry) registerHAListServices() {
	if r.ha == nil {
		return
	}
	r.Register(&Tool{
		Name: "ha_list_services",
		Description: "Discover what Home Assistant services can be called. " +
			"Without arguments: a directory of every domain and its service names. " +
			"With domain: full detail for that domain's services — description, fields (with required/example), and whether the service accepts a target block (area/floor/label/device/entity). " +
			"With a specific service — either domain plus service, or just service in the combined \"light.turn_on\" form — returns that single service. " +
			"Use this before ha_call_service when unsure of a service name or its fields.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"domain": map[string]any{
					"type":        "string",
					"description": "Scope to one domain (e.g. \"light\", \"climate\") and return full field detail.",
				},
				"service": map[string]any{
					"type":        "string",
					"description": "Return just this one service. Accepts \"turn_on\" (paired with domain) or the self-contained combined form \"light.turn_on\" with no domain argument needed.",
				},
			},
		},
		Handler: r.handleHAListServices,
	})
}

func (r *Registry) handleHAListServices(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	domain, _ := args["domain"].(string)
	service, _ := args["service"].(string)
	domain = strings.TrimSpace(domain)
	service = strings.TrimSpace(service)

	// Tolerate the combined form the model naturally reaches for:
	// service "light.turn_on" implies (and must agree with) the domain.
	if d, s, ok := strings.Cut(service, "."); ok {
		if domain != "" && domain != d {
			return "", fmt.Errorf("service %q names domain %q but domain argument says %q — drop one or make them agree", service, d, domain)
		}
		domain, service = d, s
	}
	if service != "" && domain == "" {
		return "", fmt.Errorf("service requires a domain (pass domain, or use the combined \"domain.service\" form)")
	}

	catalog, err := r.ha.GetServices(ctx)
	if err != nil {
		return "", err
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Domain < catalog[j].Domain })

	if domain == "" {
		return haServicesIndexResult(catalog), nil
	}

	for _, d := range catalog {
		if d.Domain != domain {
			continue
		}
		detail, err := haServicesDomainResult(d, service)
		if err != nil {
			return "", err
		}
		return detail, nil
	}

	known := make([]string, 0, len(catalog))
	for _, d := range catalog {
		known = append(known, d.Domain)
	}
	return "", fmt.Errorf("unknown domain %q; known domains: %s", domain, strings.Join(known, ", "))
}

func haServicesIndexResult(catalog []homeassistant.ServiceDomain) string {
	out := haServicesIndex{
		DomainCount: len(catalog),
		Note:        "Names only. Pass domain (and optionally service) for fields and target support.",
		Domains:     make([]haServiceDomainSummary, 0, len(catalog)),
	}
	for _, d := range catalog {
		names := make([]string, 0, len(d.Services))
		for name := range d.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		out.Domains = append(out.Domains, haServiceDomainSummary{Domain: d.Domain, Services: names})
	}
	return toIndentedJSONWithTruncationNote(out, haListServicesTruncationNote)
}

func haServicesDomainResult(d homeassistant.ServiceDomain, service string) (string, error) {
	names := make([]string, 0, len(d.Services))
	for name := range d.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	out := haServicesDetail{Domain: d.Domain}
	for _, name := range names {
		if service != "" && name != service {
			continue
		}
		desc := d.Services[name]
		detail := haServiceDetail{
			Service:       d.Domain + "." + name,
			Name:          desc.Name,
			Description:   desc.Description,
			AcceptsTarget: desc.Target != nil,
		}
		fieldKeys := make([]string, 0, len(desc.Fields))
		for k := range desc.Fields {
			fieldKeys = append(fieldKeys, k)
		}
		sort.Strings(fieldKeys)
		for _, k := range fieldKeys {
			f := desc.Fields[k]
			detail.Fields = append(detail.Fields, haServiceField{
				Field:       k,
				Name:        f.Name,
				Description: f.Description,
				Required:    f.Required,
				Example:     f.Example,
			})
		}
		out.Services = append(out.Services, detail)
	}
	if service != "" && len(out.Services) == 0 {
		return "", fmt.Errorf("domain %q has no service %q; it has: %s", d.Domain, service, strings.Join(names, ", "))
	}
	out.Count = len(out.Services)
	return toIndentedJSONWithTruncationNote(out, haListServicesTruncationNote), nil
}
