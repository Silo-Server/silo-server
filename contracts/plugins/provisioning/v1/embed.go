// Package provisioningv1 carries the operator-facing provisioning schema.
package provisioningv1

import _ "embed"

// Schema describes the deployment document consumed through SILO_CONFIG or
// SILO_CONFIG_FILE, and through SILO_PLUGINS or SILO_PLUGINS_FILE, the names it
// had when it declared plugins alone.
//
//go:embed schema.json
var Schema []byte
