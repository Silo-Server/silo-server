// Package apiv2 embeds the committed API v2 contract artifacts so the server
// binary and the CI gate read the exact bytes that were checked in.
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
