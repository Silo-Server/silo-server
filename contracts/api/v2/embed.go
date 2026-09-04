// Package apiv2 embeds the committed API v2 contract artifacts so every
// consumer reads the exact bytes that were checked in: the
// internal/contractledger gate, internal/routeinventory, internal/scenariocatalog,
// and the server binary, which serves and digests the OpenAPI artifact through
// internal/apiv2.
//
// This package deliberately contains nothing but the embed directives; loading
// and validation live in the packages above.
package apiv2

import (
	"embed"
	_ "embed"
)

// FS holds route-inventory.json, migration.json, migration.schema.json,
// offline-routes.txt, and the breaking-change approvals with their schema.
// The post-lock marker LOCKED is deliberately not embedded: it is read from
// the working tree by the diff command, never by the binary.
//
//go:embed route-inventory.json migration.json migration.schema.json offline-routes.txt breaking-approvals.json breaking-approvals.schema.json
var FS embed.FS

// OfflineRoutesPath is the file inside FS that pins which API listener rows
// the scenario executor's offline router registers. TestOfflineRouteSet in
// internal/api generates it; internal/scenariocatalog decodes it.
const OfflineRoutesPath = "offline-routes.txt"

// OpenAPI is the committed contracts/api/v2/openapi.json, byte for byte. The
// server serves these bytes at /api/v2/openapi.json and reports their SHA-256
// as the contract digest in /api/v2/system/info; it never regenerates the
// document from runtime wiring. cmd/apiv2-openapi generates the file from the
// Go registries (make apiv2-openapi); make verify-apiv2-openapi and
// TestCommittedArtifactMatchesRouter in internal/apiv2 fail when it is stale.
//
//go:embed openapi.json
var OpenAPI []byte
