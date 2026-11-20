// Package provisioningv1 carries the operator-facing provisioning schema.
package provisioningv1

import _ "embed"

// Schema describes the document consumed through SILO_PLUGINS and SILO_PLUGINS_FILE.
//
//go:embed schema.json
var Schema []byte
