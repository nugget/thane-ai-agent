package awareness

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func globTestHA() *fakeHA {
	mk := func(id, state string) *homeassistant.State {
		return &homeassistant.State{EntityID: id, State: state}
	}
	return &fakeHA{
		states: map[string]*homeassistant.State{
			"binary_sensor.front_door":     mk("binary_sensor.front_door", "off"),
			"binary_sensor.back_door":      mk("binary_sensor.back_door", "on"),
			"binary_sensor.kitchen_window": mk("binary_sensor.kitchen_window", "off"),
			"light.hall":                   mk("light.hall", "on"),
		},
	}
}

func TestProvider_GlobSubscriptionExpands(t *testing.T) {
	p, store := setupTestProvider(t, globTestHA())
	if err := store.Add("binary_sensor.*door*"); err != nil {
		t.Fatalf("add glob: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(got, "### Watched Entities") {
		t.Fatalf("missing header:\n%s", got)
	}
	for _, want := range []string{"binary_sensor.front_door", "binary_sensor.back_door"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %s in glob expansion:\n%s", want, got)
		}
	}
	for _, absent := range []string{"binary_sensor.kitchen_window", "light.hall"} {
		if strings.Contains(got, absent) {
			t.Errorf("did not expect %s (glob shouldn't match):\n%s", absent, got)
		}
	}
}

func TestProvider_GlobSubscriptionCapAndTruncation(t *testing.T) {
	p, store := setupTestProvider(t, globTestHA())
	p.SetMaxGlobExpansion(2)
	if err := store.Add("binary_sensor.*"); err != nil { // matches 3
		t.Fatalf("add glob: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	// Sorted: back_door, front_door come before kitchen_window, so the
	// first two render and kitchen_window is cut.
	if !strings.Contains(got, "binary_sensor.back_door") || !strings.Contains(got, "binary_sensor.front_door") {
		t.Errorf("expected the first two sorted matches:\n%s", got)
	}
	if strings.Contains(got, "binary_sensor.kitchen_window") {
		t.Errorf("third match should have been truncated:\n%s", got)
	}
	if !strings.Contains(got, `"truncated":true`) || !strings.Contains(got, `"matched":3`) || !strings.Contains(got, `"shown":2`) {
		t.Errorf("expected truncation marker (matched 3, shown 2):\n%s", got)
	}
}

func TestProvider_GlobSubscriptionEmptyIsSilent(t *testing.T) {
	p, store := setupTestProvider(t, globTestHA())
	if err := store.Add("climate.*"); err != nil { // matches nothing
		t.Fatalf("add glob: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if got != "" {
		t.Errorf("a glob matching nothing should render nothing (no bare header), got:\n%s", got)
	}
}

func TestProvider_GlobSubscriptionFetchErrorMarker(t *testing.T) {
	ha := globTestHA()
	ha.statesErr = errors.New("HA unavailable")
	p, store := setupTestProvider(t, ha)
	if err := store.Add("binary_sensor.*door*"); err != nil {
		t.Fatalf("add glob: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	// A failed bulk fetch must surface an explicit marker, not look like
	// "matched nothing".
	if !strings.Contains(got, `"reason":"fetch_error"`) || !strings.Contains(got, `"glob":"binary_sensor.*door*"`) {
		t.Errorf("expected glob fetch-error marker, got:\n%s", got)
	}
}

func TestExpandGlobSubscription_ExcludesAlreadyVisible(t *testing.T) {
	states := []homeassistant.State{
		{EntityID: "sensor.a", State: "1"},
		{EntityID: "sensor.b", State: "2"},
		{EntityID: "sensor.c", State: "3"},
	}
	exclude := map[string]struct{}{"sensor.b": {}}

	out := expandGlobSubscription(
		context.Background(),
		globTestHA(),
		slog.Default(),
		WatchedSubscription{EntityID: "sensor.*"},
		states,
		nil, // no fetch error
		time.Now(),
		nil, // no registries
		25,
		exclude,
	)
	if !strings.Contains(out, "sensor.a") || !strings.Contains(out, "sensor.c") {
		t.Errorf("expected sensor.a and sensor.c, got:\n%s", out)
	}
	if strings.Contains(out, "sensor.b") {
		t.Errorf("sensor.b should be excluded (already-visible), got:\n%s", out)
	}
}

func TestProvider_GlobAndConcreteMix(t *testing.T) {
	p, store := setupTestProvider(t, globTestHA())
	if err := store.Add("light.hall"); err != nil { // concrete
		t.Fatalf("add concrete: %v", err)
	}
	if err := store.Add("binary_sensor.*door*"); err != nil { // glob
		t.Fatalf("add glob: %v", err)
	}

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	for _, want := range []string{"light.hall", "binary_sensor.front_door", "binary_sensor.back_door"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %s (concrete + glob mix):\n%s", want, got)
		}
	}
}
