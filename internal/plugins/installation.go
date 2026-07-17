package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInstallationNotFound = errors.New("plugin installation not found")
var ErrArchiveNotFound = errors.New("plugin archive not found")
var ErrInstallationDisabled = errors.New("plugin installation is disabled")

// ErrBuiltinInstallationImmutable is returned when a caller tries to mutate
// the reserved builtin-host installation row (delete, update, config, ...).
var ErrBuiltinInstallationImmutable = errors.New("builtin installation cannot be modified")

// Installation kinds. A 'builtin' installation is the reserved row that
// anchors built-in host provider capabilities (silo.builtin); it has no
// archive, manifest, or binary and must never be launched, updated, or
// deleted. Generic reads must NOT filter builtins — the metadata chain's
// enabled-check depends on reading them.
const (
	KindPlugin  = "plugin"
	KindBuiltin = "builtin"
)

type Installation struct {
	ID               int
	RepositoryID     *int
	PluginID         string
	Version          string
	InstallPath      string
	Enabled          bool
	Kind             string  `json:"kind"`
	UpdatePolicy     string  `json:"update_policy"`
	AvailableVersion *string `json:"available_version,omitempty"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// IsBuiltin reports whether this is the reserved builtin-host installation.
func (i *Installation) IsBuiltin() bool {
	return i != nil && i.Kind == KindBuiltin
}

type Capability struct {
	InstallationID int
	Type           string
	ID             string
	Metadata       map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type InstallationArchive struct {
	InstallationID int
	ManifestJSON   []byte
	Checksum       string
	Bytes          []byte
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateInstallationInput struct {
	RepositoryID int
	PluginID     string
	Version      string
	InstallPath  string
	Enabled      bool
	UpdatePolicy string
	Capabilities []Capability
}

type UpdateInstallationInput struct {
	Version          *string
	InstallPath      *string
	Enabled          *bool
	UpdatePolicy     *string
	AvailableVersion *string
	Capabilities     []Capability
}

type InstallationStore struct {
	pool *pgxpool.Pool
}

func NewInstallationStore(pool *pgxpool.Pool) *InstallationStore {
	return &InstallationStore{pool: pool}
}

const installationColumns = `id, repository_id, plugin_id, version, install_path, enabled, kind, update_policy, available_version, created_at, updated_at`
const capabilityColumns = `plugin_installation_id, capability_type, capability_id, metadata, created_at, updated_at`
const archiveColumns = `plugin_installation_id, manifest_json, checksum, archive_bytes, created_at, updated_at`

func scanInstallation(row pgx.Row) (*Installation, error) {
	var installation Installation
	var repositoryID *int
	if err := row.Scan(
		&installation.ID,
		&repositoryID,
		&installation.PluginID,
		&installation.Version,
		&installation.InstallPath,
		&installation.Enabled,
		&installation.Kind,
		&installation.UpdatePolicy,
		&installation.AvailableVersion,
		&installation.CreatedAt,
		&installation.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInstallationNotFound
		}
		return nil, fmt.Errorf("scanning plugin installation: %w", err)
	}
	installation.RepositoryID = repositoryID
	return &installation, nil
}

func scanCapabilities(rows pgx.Rows) ([]*Capability, error) {
	var capabilities []*Capability
	for rows.Next() {
		var capability Capability
		var metadataJSON []byte
		if err := rows.Scan(
			&capability.InstallationID,
			&capability.Type,
			&capability.ID,
			&metadataJSON,
			&capability.CreatedAt,
			&capability.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning plugin capability: %w", err)
		}
		capability.Metadata = map[string]any{}
		if len(metadataJSON) > 0 {
			if err := json.Unmarshal(metadataJSON, &capability.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshaling plugin capability metadata: %w", err)
			}
		}
		capabilities = append(capabilities, &capability)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin capabilities: %w", err)
	}
	return capabilities, nil
}

func scanArchive(row pgx.Row) (*InstallationArchive, error) {
	var archive InstallationArchive
	if err := row.Scan(
		&archive.InstallationID,
		&archive.ManifestJSON,
		&archive.Checksum,
		&archive.Bytes,
		&archive.CreatedAt,
		&archive.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrArchiveNotFound
		}
		return nil, fmt.Errorf("scanning plugin archive: %w", err)
	}
	return &archive, nil
}

func (s *InstallationStore) Create(ctx context.Context, input CreateInstallationInput) (*Installation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create installation transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	updatePolicy := input.UpdatePolicy
	if updatePolicy == "" {
		updatePolicy = "auto"
	}
	query := `INSERT INTO plugin_installations (repository_id, plugin_id, version, install_path, enabled, update_policy)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING ` + installationColumns
	installation, err := scanInstallation(tx.QueryRow(
		ctx,
		query,
		nilIfZero(input.RepositoryID),
		input.PluginID,
		input.Version,
		input.InstallPath,
		input.Enabled,
		updatePolicy,
	))
	if err != nil {
		return nil, fmt.Errorf("creating plugin installation: %w", err)
	}

	if err := s.replaceCapabilities(ctx, tx, installation.ID, input.Capabilities); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create installation transaction: %w", err)
	}
	return installation, nil
}

func (s *InstallationStore) GetByID(ctx context.Context, id int) (*Installation, error) {
	query := `SELECT ` + installationColumns + ` FROM plugin_installations WHERE id = $1`
	return scanInstallation(s.pool.QueryRow(ctx, query, id))
}

func (s *InstallationStore) List(ctx context.Context) ([]*Installation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+installationColumns+` FROM plugin_installations ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing plugin installations: %w", err)
	}
	defer rows.Close()

	var installations []*Installation
	for rows.Next() {
		installation, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin installations: %w", err)
	}
	return installations, nil
}

func (s *InstallationStore) ListEnabled(ctx context.Context) ([]*Installation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+installationColumns+` FROM plugin_installations WHERE enabled = true ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing enabled plugin installations: %w", err)
	}
	defer rows.Close()

	var installations []*Installation
	for rows.Next() {
		installation, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating enabled plugin installations: %w", err)
	}
	return installations, nil
}

func (s *InstallationStore) ListByPluginID(ctx context.Context, pluginID string) ([]*Installation, error) {
	rows, err := s.pool.Query(
		ctx,
		`SELECT `+installationColumns+` FROM plugin_installations WHERE plugin_id = $1 ORDER BY id ASC`,
		pluginID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing plugin installations for plugin %q: %w", pluginID, err)
	}
	defer rows.Close()

	var installations []*Installation
	for rows.Next() {
		installation, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin installations for plugin %q: %w", pluginID, err)
	}
	return installations, nil
}

func (s *InstallationStore) Update(ctx context.Context, id int, input UpdateInstallationInput) error {
	// Guard at the store, not only the HTTP handler: the reserved builtin row
	// carries no archive/binary and must never have its version, path, enabled
	// flag, or capabilities rewritten, or its chain participation breaks.
	installation, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if installation.IsBuiltin() {
		return ErrBuiltinInstallationImmutable
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update installation transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var setClauses []string
	var args []any
	argIndex := 1

	if input.Version != nil {
		setClauses = append(setClauses, fmt.Sprintf("version = $%d", argIndex))
		args = append(args, *input.Version)
		argIndex++
	}
	if input.InstallPath != nil {
		setClauses = append(setClauses, fmt.Sprintf("install_path = $%d", argIndex))
		args = append(args, *input.InstallPath)
		argIndex++
	}
	if input.Enabled != nil {
		setClauses = append(setClauses, fmt.Sprintf("enabled = $%d", argIndex))
		args = append(args, *input.Enabled)
		argIndex++
	}
	if input.UpdatePolicy != nil {
		setClauses = append(setClauses, fmt.Sprintf("update_policy = $%d", argIndex))
		args = append(args, *input.UpdatePolicy)
		argIndex++
	}
	if input.AvailableVersion != nil {
		setClauses = append(setClauses, fmt.Sprintf("available_version = $%d", argIndex))
		args = append(args, *input.AvailableVersion)
		argIndex++
	}

	if len(setClauses) > 0 {
		setClauses = append(setClauses, "updated_at = NOW()")
		args = append(args, id)
		query := fmt.Sprintf(
			"UPDATE plugin_installations SET %s WHERE id = $%d",
			strings.Join(setClauses, ", "),
			argIndex,
		)
		tag, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("updating plugin installation: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrInstallationNotFound
		}
	}

	if input.Capabilities != nil {
		if err := s.replaceCapabilities(ctx, tx, id, input.Capabilities); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update installation transaction: %w", err)
	}
	return nil
}

func (s *InstallationStore) ListCapabilities(ctx context.Context, installationID int) ([]*Capability, error) {
	query := `SELECT ` + capabilityColumns + ` FROM plugin_capabilities
		WHERE plugin_installation_id = $1
		ORDER BY capability_type ASC, capability_id ASC`
	rows, err := s.pool.Query(ctx, query, installationID)
	if err != nil {
		return nil, fmt.Errorf("listing plugin capabilities: %w", err)
	}
	defer rows.Close()
	return scanCapabilities(rows)
}

func (s *InstallationStore) SaveArchive(
	ctx context.Context,
	installationID int,
	manifestJSON []byte,
	checksum string,
	archiveBytes []byte,
) error {
	if len(manifestJSON) == 0 {
		return fmt.Errorf("saving plugin archive: manifest JSON is required")
	}
	if checksum == "" {
		return fmt.Errorf("saving plugin archive: checksum is required")
	}
	if len(archiveBytes) == 0 {
		return fmt.Errorf("saving plugin archive: archive bytes are required")
	}
	query := `INSERT INTO plugin_archives (plugin_installation_id, manifest_json, checksum, archive_bytes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (plugin_installation_id) DO UPDATE SET
			manifest_json = EXCLUDED.manifest_json,
			checksum = EXCLUDED.checksum,
			archive_bytes = EXCLUDED.archive_bytes,
			updated_at = NOW()`
	tag, err := s.pool.Exec(ctx, query, installationID, manifestJSON, checksum, archiveBytes)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrInstallationNotFound
		}
		return fmt.Errorf("saving plugin archive: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInstallationNotFound
	}
	return nil
}

func (s *InstallationStore) GetArchive(ctx context.Context, installationID int) (*InstallationArchive, error) {
	query := `SELECT ` + archiveColumns + ` FROM plugin_archives WHERE plugin_installation_id = $1`
	return scanArchive(s.pool.QueryRow(ctx, query, installationID))
}

func (s *InstallationStore) Delete(ctx context.Context, id int) error {
	installation, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	// Guard at the store, not only the HTTP handler: deleting the reserved
	// builtin row would cascade through every builtin chain row and RemoveAll
	// the (sentinel) install dir.
	if installation.IsBuiltin() {
		return ErrBuiltinInstallationImmutable
	}

	tag, err := s.pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting plugin installation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInstallationNotFound
	}

	installDir := filepath.Dir(installation.InstallPath)
	if err := os.RemoveAll(installDir); err != nil {
		return fmt.Errorf("removing plugin installation files: %w", err)
	}

	return nil
}

func (s *InstallationStore) replaceCapabilities(
	ctx context.Context,
	tx pgx.Tx,
	installationID int,
	capabilities []Capability,
) error {
	if _, err := tx.Exec(ctx, "DELETE FROM plugin_capabilities WHERE plugin_installation_id = $1", installationID); err != nil {
		return fmt.Errorf("deleting plugin capabilities: %w", err)
	}

	for _, capability := range capabilities {
		metadata := capability.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadataJSON, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshaling plugin capability metadata: %w", err)
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO plugin_capabilities
				(plugin_installation_id, capability_type, capability_id, metadata)
			VALUES ($1, $2, $3, $4)`,
			installationID,
			capability.Type,
			capability.ID,
			metadataJSON,
		)
		if err != nil {
			return fmt.Errorf("inserting plugin capability: %w", err)
		}
	}
	return nil
}

func nilIfZero(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}
