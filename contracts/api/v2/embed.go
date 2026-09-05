// Package apiv2 embeds the committed API v2 contract artifacts so the
// internal/contractledger gate, and any future consumer, reads the exact bytes
// that were checked in. Nothing in cmd/silo imports this package today; the
// server binary does not carry or serve these artifacts.
//
// This package deliberately contains nothing but the embed directive; loading
// and validation live in internal/contractledger (migration ledger),
// internal/routeinventory (route inventory), and internal/scenariocatalog
// (offline route set).
package apiv2

import "embed"

// FS holds route-inventory.json, migration.json, migration.schema.json, and
// offline-routes.txt.
//
//go:embed route-inventory.json migration.json migration.schema.json offline-routes.txt
var FS embed.FS

// OfflineRoutesPath is the file inside FS that pins which API listener rows
// the scenario executor's offline router registers. TestOfflineRouteSet in
// internal/api generates it; internal/scenariocatalog decodes it.
const OfflineRoutesPath = "offline-routes.txt"
