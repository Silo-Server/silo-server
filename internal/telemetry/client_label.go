package telemetry

import "strings"

// ClientLabel maps only recognized first-party product names into the fixed
// client families used as the `client` metric label: web, apple, android,
// other, or none for a nameless client. Arbitrary self-reported names cannot
// create metric series or store private text in Prometheus. Logs retain the
// existing clamped client identity for diagnosis.
func ClientLabel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return "none"
	case "silo web":
		return "web"
	case "silo apple", "silo apple tv", "silo ios", "silo tvos", "silo macos", "silo ipados":
		return "apple"
	case "silo android", "silo android tv":
		return "android"
	default:
		return "other"
	}
}
