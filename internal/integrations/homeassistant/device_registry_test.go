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
