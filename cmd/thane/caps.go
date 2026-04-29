package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
)

// runCaps implements the `thane caps [tag] [--excluded]` subcommand.
// It is a thin client over the live daemon's /api/capabilities
// endpoint: the daemon owns the resolver and emits the canonical
// view, the CLI just formats it. This keeps the CLI free of any
// resolver bootstrap and guarantees it shows the same shape the web
// UI sees.
func runCaps(ctx context.Context, stdout io.Writer, configPath, outputFmt string, args []string) error {
	tag, includeExcluded := parseCapsArgs(args)

	cfg, _, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	endpoint, err := capsEndpoint(cfg.Listen.Address, cfg.Listen.Port, tag, includeExcluded)
	if err != nil {
		return err
	}

	body, err := capsFetch(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("query %s: %w (is the daemon running?)", endpoint, err)
	}

	switch outputFmt {
	case "json":
		_, _ = stdout.Write(body)
		if len(body) > 0 && body[len(body)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
		return nil
	default:
		return renderCapsText(stdout, body, tag, includeExcluded)
	}
}

func parseCapsArgs(args []string) (tag string, includeExcluded bool) {
	for _, a := range args {
		switch a {
		case "--excluded", "-x":
			includeExcluded = true
		default:
			if strings.HasPrefix(a, "-") {
				continue
			}
			if tag == "" {
				tag = a
			}
		}
	}
	return
}

func capsEndpoint(addr string, port int, tag string, includeExcluded bool) (string, error) {
	host := addr
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if port == 0 {
		return "", fmt.Errorf("listen.port is not configured")
	}
	base := fmt.Sprintf("http://%s/api/capabilities", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if tag != "" {
		base += "/" + url.PathEscape(tag)
	}
	if includeExcluded {
		base += "?include=excluded"
	}
	return base, nil
}

func capsFetch(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyText, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyText)))
	}
	return io.ReadAll(resp.Body)
}

func renderCapsText(w io.Writer, body []byte, tag string, includeExcluded bool) error {
	if tag == "" {
		var view toolcatalog.CapabilityCatalogView
		if err := json.Unmarshal(body, &view); err != nil {
			return fmt.Errorf("decode catalog: %w", err)
		}
		return renderCapsCatalog(w, view, includeExcluded)
	}
	var entry toolcatalog.CapabilityCatalogEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		return fmt.Errorf("decode entry: %w", err)
	}
	return renderCapsEntry(w, entry, includeExcluded)
}

func renderCapsCatalog(w io.Writer, view toolcatalog.CapabilityCatalogView, includeExcluded bool) error {
	if len(view.Capabilities) == 0 {
		fmt.Fprintln(w, "No capability tags resolved.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TAG\tSTATUS\tTOOLS\tDESCRIPTION")
	for _, e := range view.Capabilities {
		desc := strings.TrimSpace(e.Description)
		if len(desc) > 60 {
			desc = desc[:60] + "…"
		}
		count := fmt.Sprintf("%d", len(e.Tools))
		if includeExcluded && len(e.ExcludedTools) > 0 {
			count = fmt.Sprintf("%d (+%d excluded)", len(e.Tools), len(e.ExcludedTools))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Tag, e.Status, count, desc)
	}
	return tw.Flush()
}

func renderCapsEntry(w io.Writer, entry toolcatalog.CapabilityCatalogEntry, includeExcluded bool) error {
	fmt.Fprintf(w, "Tag:         %s\n", entry.Tag)
	fmt.Fprintf(w, "Status:      %s\n", entry.Status)
	if entry.Description != "" {
		fmt.Fprintf(w, "Description: %s\n", entry.Description)
	}
	if entry.AlwaysActive {
		fmt.Fprintln(w, "Always active: yes")
	}
	if entry.Protected {
		fmt.Fprintln(w, "Protected:    yes")
	}
	if entry.AdHoc {
		fmt.Fprintln(w, "Ad-hoc:       yes")
	}

	if len(entry.ToolEntries) == 0 && len(entry.ExcludedTools) == 0 {
		fmt.Fprintln(w, "\nNo tools.")
		return nil
	}

	active := append([]toolcatalog.CapabilityToolEntry(nil), entry.ToolEntries...)
	sort.Slice(active, func(i, j int) bool { return active[i].Name < active[j].Name })
	if len(active) > 0 {
		fmt.Fprintln(w, "\nActive tools:")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tSOURCE")
		for _, t := range active {
			fmt.Fprintf(tw, "  %s\t%s\n", t.Name, sourceLabel(t.Source))
		}
		_ = tw.Flush()
	}

	if !includeExcluded {
		return nil
	}
	excluded := append([]toolcatalog.CapabilityToolEntry(nil), entry.ExcludedTools...)
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].Name < excluded[j].Name })
	if len(excluded) > 0 {
		fmt.Fprintln(w, "\nExcluded tools:")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  NAME\tSOURCE\tREASON")
		for _, t := range excluded {
			reason := ""
			if t.State != nil {
				reason = t.State.Reason
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", t.Name, sourceLabel(t.Source), reason)
		}
		_ = tw.Flush()
	}
	return nil
}

func sourceLabel(s toolcatalog.CapabilityToolSource) string {
	if s.Origin == "" {
		return s.Kind
	}
	return s.Kind + ":" + s.Origin
}
