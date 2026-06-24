//go:build uiharness

// Command uiharness serves the web console backed by synthetic /v1 data, for
// iterating on the UI without a real Thane. Build-tagged so it is excluded
// from normal builds; run with:
//
//	go run -tags uiharness ./cmd/uiharness --static internal/server/web/static
package main

import (
	"flag"
	"log"

	"github.com/nugget/thane-ai-agent/internal/server/api"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "listen address")
	static := flag.String("static", "internal/server/web/static", "console static asset directory")
	flag.Parse()

	log.Printf("ui harness: http://%s  (static: %s)", *addr, *static)
	if err := api.RunUIHarness(*addr, *static); err != nil {
		log.Fatalf("ui harness: %v", err)
	}
}
