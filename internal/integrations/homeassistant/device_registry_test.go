package homeassistant

import (
	"encoding/json"
	"testing"
)

func TestFlexString_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"string", `"1.2.3"`, "1.2.3"},
		{"empty string", `""`, ""},
		{"integer", `2`, "2"},
		{"float", `2.3`, "2.3"},
		{"null", `null`, ""},
		{"bool", `true`, "true"},
		{"whitespace padded number", ` 7 `, "7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s flexString
			if err := json.Unmarshal([]byte(tc.in), &s); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tc.in, err)
			}
			if string(s) != tc.want {
				t.Errorf("Unmarshal(%s) = %q, want %q", tc.in, string(s), tc.want)
			}
		})
	}
}

// TestDeviceRegistryEntry_NumericVersionDoesNotFailDecode is the regression
// for the production failure: a single device reporting a numeric sw_version
// (or hw_version) must not fail the entire device_registry/list decode and
// take down every device-level lookup. The numeric value is coerced to its
// string form, and the rest of the list decodes normally.
func TestDeviceRegistryEntry_NumericVersionDoesNotFailDecode(t *testing.T) {
	raw := `[
		{"id":"a","name":"Numeric FW","sw_version":2,"hw_version":1.5},
		{"id":"b","name":"Normal","sw_version":"3.4.5","hw_version":"rev-c"},
		{"id":"c","name":"Null FW","sw_version":null}
	]`

	var devices []DeviceRegistryEntry
	if err := json.Unmarshal([]byte(raw), &devices); err != nil {
		t.Fatalf("decode device list: %v", err)
	}
	if len(devices) != 3 {
		t.Fatalf("decoded %d devices, want 3", len(devices))
	}

	if got := string(devices[0].SWVersion); got != "2" {
		t.Errorf("device[0].SWVersion = %q, want \"2\"", got)
	}
	if got := string(devices[0].HWVersion); got != "1.5" {
		t.Errorf("device[0].HWVersion = %q, want \"1.5\"", got)
	}
	if got := string(devices[1].SWVersion); got != "3.4.5" {
		t.Errorf("device[1].SWVersion = %q, want \"3.4.5\"", got)
	}
	if got := string(devices[1].HWVersion); got != "rev-c" {
		t.Errorf("device[1].HWVersion = %q, want \"rev-c\"", got)
	}
	if got := string(devices[2].SWVersion); got != "" {
		t.Errorf("device[2].SWVersion = %q, want empty", got)
	}
}

// TestDeviceRegistryEntry_AllIntegrationFieldsTolerateNumbers is the broad
// guarantee behind this hardening: HA does not strictly validate integration
// input, so every integration-supplied device field can arrive as a number —
// and none of them may fail the device_registry/list decode. Each numeric
// value is coerced to its string form, including elements nested inside the
// identifiers/connections tuples.
func TestDeviceRegistryEntry_AllIntegrationFieldsTolerateNumbers(t *testing.T) {
	raw := `[{
		"id":"a",
		"manufacturer":3,
		"model":650,
		"model_id":42,
		"name":12345,
		"name_by_user":99,
		"serial_number":1234567890,
		"sw_version":2,
		"hw_version":1.5,
		"identifiers":[["mqtt",42],["hue","light-1"]],
		"connections":[["mac",112233]]
	}]`

	var devices []DeviceRegistryEntry
	if err := json.Unmarshal([]byte(raw), &devices); err != nil {
		t.Fatalf("decode device list with all-numeric fields: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("decoded %d devices, want 1", len(devices))
	}
	d := devices[0]

	got := map[string]string{
		"manufacturer":  string(d.Manufacturer),
		"model":         string(d.Model),
		"model_id":      string(d.ModelID),
		"name":          string(d.Name),
		"name_by_user":  string(d.NameByUser),
		"serial_number": string(d.SerialNumber),
		"sw_version":    string(d.SWVersion),
		"hw_version":    string(d.HWVersion),
	}
	want := map[string]string{
		"manufacturer":  "3",
		"model":         "650",
		"model_id":      "42",
		"name":          "12345",
		"name_by_user":  "99",
		"serial_number": "1234567890",
		"sw_version":    "2",
		"hw_version":    "1.5",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}

	if len(d.Identifiers) != 2 || string(d.Identifiers[0][1]) != "42" {
		t.Errorf("numeric identifier element not coerced: %+v", d.Identifiers)
	}
	if len(d.Connections) != 1 || string(d.Connections[0][1]) != "112233" {
		t.Errorf("numeric connection element not coerced: %+v", d.Connections)
	}
}

// TestEntityRegistryEntry_NumericFieldsTolerated covers the entity-registry
// counterpart: a numeric unique_id (common — integrations use raw device IDs)
// or original_name must not fail the entity_registry/list decode. unique_id
// has no readers, but the decode still crashed on it before this fix.
func TestEntityRegistryEntry_NumericFieldsTolerated(t *testing.T) {
	raw := `[
		{"entity_id":"sensor.a","unique_id":1234567890,"original_name":42},
		{"entity_id":"sensor.b","unique_id":"abc-123","original_name":"Temp"}
	]`
	var entries []EntityRegistryEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("decode entity list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("decoded %d entities, want 2", len(entries))
	}
	if got := string(entries[0].UniqueID); got != "1234567890" {
		t.Errorf("entries[0].UniqueID = %q, want \"1234567890\"", got)
	}
	if got := string(entries[0].OriginalName); got != "42" {
		t.Errorf("entries[0].OriginalName = %q, want \"42\"", got)
	}
	if got := string(entries[1].UniqueID); got != "abc-123" {
		t.Errorf("entries[1].UniqueID = %q, want \"abc-123\"", got)
	}
}
