// Package apiv2 embeds the committed API v2 contract artifacts so the
// internal/contractledger gate, and any future consumer, reads the exact bytes
// that were checked in. Nothing in cmd/silo imports this package today; the
// server binary does not carry or serve these artifacts.
//
// This package deliberately contains nothing but the embed directive; loading
// and validation live in internal/contractledger (migration ledger) and
// internal/routeinventory (route inventory).
package apiv2

import "embed"

// FS holds route-inventory.json, migration.json, and migration.schema.json.
//
//go:embed route-inventory.json migration.json migration.schema.json
var FS embed.FS
