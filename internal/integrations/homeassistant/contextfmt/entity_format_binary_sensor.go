package contextfmt

// binarySensorStateLabels maps a binary_sensor device_class to its
// semantic [off, on] labels. Tracks the canonical Home Assistant
// device_class catalog. Translation collapses the on/off encoding into
// the actual fact about the world, so the model does not need to know
// that device_class:door + state:on means "door is open".
var binarySensorStateLabels = map[string][2]string{
	"battery":          {"normal", "low"},
	"battery_charging": {"not_charging", "charging"},
	"carbon_monoxide":  {"clear", "detected"},
	"cold":             {"normal", "cold"},
	"connectivity":     {"disconnected", "connected"},
	"door":             {"closed", "open"},
	"garage_door":      {"closed", "open"},
	"gas":              {"clear", "detected"},
	"heat":             {"normal", "hot"},
	"light":            {"no_light", "light_detected"},
	"lock":             {"locked", "unlocked"},
	"moisture":         {"dry", "wet"},
	"motion":           {"clear", "detected"},
	"moving":           {"stopped", "moving"},
	"occupancy":        {"clear", "occupied"},
	"opening":          {"closed", "open"},
	"plug":             {"unplugged", "plugged_in"},
	"power":            {"no_power", "powered"},
	"presence":         {"away", "home"},
	"problem":          {"ok", "problem"},
	"running":          {"not_running", "running"},
	"safety":           {"safe", "unsafe"},
	"smoke":            {"clear", "detected"},
	"sound":            {"clear", "detected"},
	"tamper":           {"clear", "tampering"},
	"update":           {"up_to_date", "update_available"},
	"vibration":        {"clear", "detected"},
	"window":           {"closed", "open"},
}

// semanticState returns a model-friendly state label for the given
// (domain, device_class, state) tuple. For binary_sensors with a known
// device_class, on/off is translated to its semantic pair (e.g. door
// on/off → open/closed). Numeric default-domain states are rounded by
// device_class. All other inputs pass through unchanged so unavailable,
// unknown, and unmapped values are preserved.
func semanticState(domain, deviceClass, state string) string {
	if domain == "binary_sensor" && deviceClass != "" {
		if labels, ok := binarySensorStateLabels[deviceClass]; ok {
			switch state {
			case "off":
				return labels[0]
			case "on":
				return labels[1]
			}
		}
	}
	return roundState(state, deviceClass)
}
