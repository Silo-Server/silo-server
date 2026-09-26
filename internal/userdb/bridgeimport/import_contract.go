package bridgeimport

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// MappingVersion identifies the receipt transformation rules.
const MappingVersion = 1

const (
	sourceReadOnlyQuery       = "mode=ro&immutable=1&_query_only=1"
	sourceFileScheme          = "file"
	providerSwitchBlocked     = "blocked"
	sourceCollectionRevisions = "personal_collection_revisions"
	accountImported           = "account_imported"
	sourceSectionOverrides    = "profile_section_overrides"
	sourceOrderRevision       = "personal_collection_order_revision"
	sourceSingleton           = "singleton"
)

// ProgressTransition must establish the account's durable sync generation in
// this transaction. It must not commit, rotate it outside the transaction, or
// activate a provider. There is deliberately no default implementation: a live
// source MAX(synced_seq) cannot bound cursors retained after rows were deleted.
// An identical receipt replay does not call this transition again.
type ProgressTransition func(context.Context, pgx.Tx, int) (string, error)

// BackupIdentity binds a verified standalone backup to an existing installation
// and account. A numeric filename alone is not provenance. The caller must
// establish backup consistency and fence all application writers before import.
type BackupIdentity struct {
	AccountID      int
	InstallationID string
	SourceSHA256   string
}

// TableVerification contains no source values, paths or credentials. Digests
// compare the imported semantic projection, excluding remapped surrogate IDs
// and deliberately rebased sequence/revision witnesses.
type TableVerification struct {
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256"`
}

// ImportResult is account evidence, never global readiness. Replaying an
// identical receipt returns its original generation and import timestamp.
type ImportResult struct {
	Status             string                       `json:"status"`
	ProviderSwitch     string                       `json:"provider_switch"`
	Replayed           bool                         `json:"replayed"`
	ProgressGeneration string                       `json:"progress_generation"`
	ImportedAt         time.Time                    `json:"imported_at"`
	Tables             map[string]TableVerification `json:"tables"`
}
