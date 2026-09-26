package telemetry

import "strings"

// ClientLabelName is the metric label that carries a ClientLabel family.
const ClientLabelName = "client"

// The client families, the only values ClientLabel returns.
const (
	clientNone    = "none"
	clientWeb     = "web"
	clientApple   = "apple"
	clientAndroid = "android"
	clientOther   = "other"
)

// ClientLabel maps only recognized first-party product names into the fixed
// client families used as the `client` metric label: web, apple, android,
// other, or none for a nameless client. Arbitrary self-reported names cannot
// create metric series or store private text in Prometheus. Logs retain the
// existing clamped client identity for diagnosis.
func ClientLabel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "":
		return clientNone
	case "silo web":
		return clientWeb
	case "silo apple", "silo apple tv", "silo ios", "silo tvos", "silo macos", "silo ipados":
		return clientApple
	case "silo android", "silo android tv":
		return clientAndroid
	default:
		return clientOther
	}
}
