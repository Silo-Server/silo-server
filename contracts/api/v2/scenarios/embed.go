// Package scenarios embeds the tier-1 scenario catalogs and their JSON Schema
// so the coverage gate and the executor read the exact bytes that were
// checked in.
//
// Loading, validation, and execution live in internal/scenariocatalog.
package scenarios

import "embed"

// FS holds scenario-catalog.schema.json and every <listener>/<group>.json
// catalog. Referenced request bodies live under fixtures/.
//
//go:embed scenario-catalog.schema.json */*.json
var FS embed.FS

// SchemaPath is the schema file name inside FS.
const SchemaPath = "scenario-catalog.schema.json"
