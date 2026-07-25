// Package settingsv1 embeds the canonical cross-platform user settings
// contract so the server binary carries the exact bytes it was built from.
//
// This package deliberately contains nothing but the embed directive. The
// contract files are the artifact clients vendor and generate bindings from, so
// they live at this stable path rather than inside an internal package; the
// embed has to sit beside them because go:embed cannot reach outside its own
// directory.
//
// Loading, validation, and lookup live in internal/settingscontract.
package settingsv1

import "embed"

// FS holds manifest.json, manifest.schema.json, and schemas/.
//
//go:embed manifest.json manifest.schema.json schemas
var FS embed.FS
