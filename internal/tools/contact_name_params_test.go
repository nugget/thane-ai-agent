package tools

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// TestContactNameRuleTeachesResolution pins what the shared name rule
// tells a model about a contact name argument: what it matches, that
// free text never resolves it, and what an ambiguous name hands back.
func TestContactNameRuleTeachesResolution(t *testing.T) {
	for _, want := range []string{
		"formatted name or nickname", "operator's own contact wins, then one above known",
		"given name or the first word of its formatted name", "exactly one contact must fit",
		"Notes, AI summaries and organizations are never used to resolve a name",
		"none is chosen", "full formatted name, trust zone and contact_id",
	} {
		if !strings.Contains(contactNameRule, want) {
			t.Errorf("contactNameRule lacks %q: %s", want, contactNameRule)
		}
	}
}

// TestContactNameParametersTeachResolution pins that every tool taking a
// contact name outside the contact family says how the name resolves,
// and names the retry the tool can actually make: a contact_id where it
// takes one, the full formatted name where it takes only a name.
func TestContactNameParametersTeachResolution(t *testing.T) {
	haReg, _ := newTestNotifyRegistry()
	routerReg, _, _ := newTestNotifyRegistryWithRouter(t)
	escalationReg := newTestEscalationRegistry(t)
	counterpartyReg := NewEmptyRegistry()
	counterpartyReg.EnableCounterpartyTools(newWhereaboutsFixture(t, "home", "", nil).deps)
	placesDeps, _ := newPlacesFixture(t, "")
	counterpartyReg.EnableCounterpartyPlacesTools(placesDeps)

	tests := []struct {
		reg       *Registry
		tool      string
		parameter string
		want      string
	}{
		{haReg, "ha_notify", "recipient", contactNameRetryByName},
		{routerReg, "send_notification", "recipient", contactNameRetryByName},
		{routerReg, "request_human_decision", "recipient", contactNameRetryByName},
		{escalationReg, "request_human_escalation", "recipient", contactNameRetryByName},
		{counterpartyReg, "contact_whereabouts", "name", contactNameRetryByID},
		{counterpartyReg, "contact_recent_places", "name", contactNameRetryByID},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			tool := tt.reg.Get(tt.tool)
			if tool == nil {
				t.Fatalf("%s is not registered", tt.tool)
			}
			properties, _ := tool.Parameters["properties"].(map[string]any)
			schema, _ := properties[tt.parameter].(map[string]any)
			description, _ := schema["description"].(string)
			if !strings.HasSuffix(description, tt.want) {
				t.Errorf("%s %s description = %q, want it to end with the name rule and its retry %q", tt.tool, tt.parameter, description, tt.want)
			}
		})
	}
}

// newTestEscalationRegistry registers request_human_escalation over an
// in-memory record store.
func newTestEscalationRegistry(t *testing.T) *Registry {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	records, err := notifications.NewRecordStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}
	resolver := &mockNotifyContacts{contact: &contacts.Contact{ID: uuid.New(), FormattedName: "Alice"}}
	reg := NewEmptyRegistry()
	reg.SetEscalationTools(EscalationDeps{
		Router:     notifications.NewNotificationRouter(resolver, records, slog.Default()),
		Records:    records,
		Dispatcher: notifications.NewCallbackDispatcher(records, nil, nil, "test-thane", slog.Default()),
		Waiter:     notifications.NewResponseWaiter(),
	})
	return reg
}
