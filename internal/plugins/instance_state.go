package plugins

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// HostScopeAPI is the instance-state scope of the api host. Proxy nodes use
// NodeHostScope. The scope is derived by the host, never by the plugin.
const HostScopeAPI = "api"

// NodeHostScope returns the instance-state scope of one proxy node,
// "node:<stream_nodes.id>".
func NodeHostScope(nodeRowID int64) string {
	return "node:" + strconv.FormatInt(nodeRowID, 10)
}

// InstanceStateStore stores plugin_instance_state rows: one encrypted value
// per installation, host scope and key. Values are sealed with the server
// data cipher under row AAD so a blob cannot be moved to another installation,
// host or key, and nothing in the API returns them.
type InstanceStateStore struct {
	pool   *pgxpool.Pool
	cipher *secret.Cipher
}

// NewInstanceStateStore creates the store. The cipher is required: instance
// state holds overlay node keys and is never written in plaintext.
func NewInstanceStateStore(pool *pgxpool.Pool, cipher *secret.Cipher) *InstanceStateStore {
	return &InstanceStateStore{pool: pool, cipher: cipher}
}

// ForScope binds the store to one host scope, giving the pluginhost
// RuntimeHost server an InstanceStateStore that only names installations
// and keys.
func (s *InstanceStateStore) ForScope(scope string) *ScopedInstanceStateStore {
	return &ScopedInstanceStateStore{store: s, scope: scope}
}

func instanceStateAAD(installationID int, scope, key string) string {
	return secret.RowAAD(
		"plugin_instance_state",
		"state_value",
		strconv.Itoa(installationID)+":"+scope+":"+key,
	)
}

func validateInstanceStateScope(installationID int, scope string) error {
	if installationID <= 0 {
		return pluginhost.ErrInstanceStateUnavailable
	}
	if strings.TrimSpace(scope) == "" {
		return errors.New("instance state host scope is required")
	}
	return nil
}

// Read returns the value stored under key for the installation in scope and
// its version. Version 0 means no value is stored.
func (s *InstanceStateStore) Read(ctx context.Context, installationID int, scope, key string) ([]byte, int64, error) {
	if err := validateInstanceStateScope(installationID, scope); err != nil {
		return nil, 0, err
	}
	if err := pluginhost.ValidateInstanceStateKey(key); err != nil {
		return nil, 0, err
	}
	if s == nil || s.pool == nil {
		return nil, 0, errors.New("instance state store is not configured")
	}
	var stored []byte
	var version int64
	err := s.pool.QueryRow(ctx, `
		SELECT state_value, version
		FROM plugin_instance_state
		WHERE plugin_installation_id = $1 AND host_scope = $2 AND state_key = $3
	`, installationID, scope, key).Scan(&stored, &version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("reading plugin instance state: %w", err)
	}
	value, err := s.open(installationID, scope, key, stored)
	if err != nil {
		return nil, 0, err
	}
	return value, version, nil
}

// Write stores value under key for the installation in scope and returns the
// version it wrote. A nil expectedVersion replaces any previous value. A
// non-nil one writes only while the key is at that version, 0 meaning the
// key must not exist, and otherwise fails with
// pluginhost.ErrInstanceStateVersionConflict. A new key fails with
// pluginhost.ErrInstanceStateTooManyKeys when the scope already holds
// InstanceStateMaxKeys other keys.
func (s *InstanceStateStore) Write(ctx context.Context, installationID int, scope, key string, value []byte, expectedVersion *int64) (int64, error) {
	if err := validateInstanceStateScope(installationID, scope); err != nil {
		return 0, err
	}
	if err := pluginhost.ValidateInstanceStateKey(key); err != nil {
		return 0, err
	}
	if len(value) > pluginhost.InstanceStateMaxValueBytes {
		return 0, pluginhost.ErrInstanceStateValueTooLarge
	}
	if s == nil || s.pool == nil {
		return 0, errors.New("instance state store is not configured")
	}
	sealed, err := s.seal(installationID, scope, key, value)
	if err != nil {
		return 0, err
	}
	// Writers to one scope are serialized by a transaction-scoped advisory
	// lock so two concurrent new keys cannot both observe the same count and
	// overshoot the key budget. Each statement below checks the version in
	// its own WHERE, so the condition and the write are one step.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("writing plugin instance state: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('plugin_instance_state'), hashtext($1::text || ':' || $2))`, strconv.Itoa(installationID), scope); err != nil {
		return 0, fmt.Errorf("writing plugin instance state: %w", err)
	}
	var version int64
	if expectedVersion != nil && *expectedVersion != 0 {
		version, err = replaceInstanceState(ctx, tx, installationID, scope, key, sealed, *expectedVersion)
	} else {
		version, err = upsertInstanceState(ctx, tx, installationID, scope, key, sealed, expectedVersion == nil)
	}
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("writing plugin instance state: %w", err)
	}
	return version, nil
}

// replaceInstanceState overwrites a key that is still at expectedVersion. It
// never adds a key, so the key budget does not apply. A negative version
// matches no row.
func replaceInstanceState(ctx context.Context, tx pgx.Tx, installationID int, scope, key string, sealed []byte, expectedVersion int64) (int64, error) {
	var version int64
	err := tx.QueryRow(ctx, `
		UPDATE plugin_instance_state
		SET state_value = $4, version = version + 1, updated_at = NOW()
		WHERE plugin_installation_id = $1 AND host_scope = $2 AND state_key = $3 AND version = $5
		RETURNING version
	`, installationID, scope, key, sealed, expectedVersion).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, pluginhost.ErrInstanceStateVersionConflict
	}
	if err != nil {
		return 0, fmt.Errorf("writing plugin instance state: %w", err)
	}
	return version, nil
}

// upsertInstanceState inserts a key while the scope has room in its key
// budget. When the key already exists it overwrites it if overwrite is set
// and otherwise leaves it alone, which is a version conflict for a
// create-only write.
func upsertInstanceState(ctx context.Context, tx pgx.Tx, installationID int, scope, key string, sealed []byte, overwrite bool) (int64, error) {
	var version int64
	err := tx.QueryRow(ctx, `
		INSERT INTO plugin_instance_state (plugin_installation_id, host_scope, state_key, state_value)
		SELECT $1, $2, $3, $4
		WHERE (
			SELECT count(*)
			FROM plugin_instance_state
			WHERE plugin_installation_id = $1 AND host_scope = $2 AND state_key <> $3
		) < $5
		ON CONFLICT (plugin_installation_id, host_scope, state_key) DO UPDATE SET
			state_value = EXCLUDED.state_value,
			version = plugin_instance_state.version + 1,
			updated_at = NOW()
		WHERE $6::boolean
		RETURNING version
	`, installationID, scope, key, sealed, pluginhost.InstanceStateMaxKeys, overwrite).Scan(&version)
	if err == nil {
		return version, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("writing plugin instance state: %w", err)
	}
	// No row: the budget refused a new key, or a create-only write found the
	// key present. The advisory lock keeps the key as the statement saw it.
	if overwrite {
		return 0, pluginhost.ErrInstanceStateTooManyKeys
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM plugin_instance_state
			WHERE plugin_installation_id = $1 AND host_scope = $2 AND state_key = $3
		)
	`, installationID, scope, key).Scan(&exists); err != nil {
		return 0, fmt.Errorf("writing plugin instance state: %w", err)
	}
	if exists {
		return 0, pluginhost.ErrInstanceStateVersionConflict
	}
	return 0, pluginhost.ErrInstanceStateTooManyKeys
}

// Keys lists the keys stored for the installation in scope, in key order.
func (s *InstanceStateStore) Keys(ctx context.Context, installationID int, scope string) ([]string, error) {
	if err := validateInstanceStateScope(installationID, scope); err != nil {
		return nil, err
	}
	if s == nil || s.pool == nil {
		return nil, errors.New("instance state store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT state_key
		FROM plugin_instance_state
		WHERE plugin_installation_id = $1 AND host_scope = $2
		ORDER BY state_key ASC
	`, installationID, scope)
	if err != nil {
		return nil, fmt.Errorf("listing plugin instance state keys: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scanning plugin instance state key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin instance state keys: %w", err)
	}
	return keys, nil
}

// Empty values need an authenticated marker because Cipher.Encrypt leaves
// empty plaintext unwrapped. A separate AAD domain prevents adding or removing
// the marker from turning an ordinary ciphertext into an empty value.
const instanceStateEmptyMarker = "empty:v1:"

func (s *InstanceStateStore) seal(installationID int, scope, key string, value []byte) ([]byte, error) {
	if s.cipher == nil {
		return nil, errors.New("plugin instance state requires the server data cipher")
	}
	if len(value) == 0 {
		ciphertext, err := s.cipher.Encrypt("empty", instanceStateEmptyMarker+instanceStateAAD(installationID, scope, key))
		if err != nil {
			return nil, fmt.Errorf("encrypting empty plugin instance state: %w", err)
		}
		return []byte(instanceStateEmptyMarker + ciphertext), nil
	}
	ciphertext, err := s.cipher.Encrypt(string(value), instanceStateAAD(installationID, scope, key))
	if err != nil {
		return nil, fmt.Errorf("encrypting plugin instance state: %w", err)
	}
	return []byte(ciphertext), nil
}

func (s *InstanceStateStore) open(installationID int, scope, key string, stored []byte) ([]byte, error) {
	if s.cipher == nil {
		return nil, errors.New("plugin instance state requires the server data cipher")
	}
	if ciphertext, empty := strings.CutPrefix(string(stored), instanceStateEmptyMarker); empty {
		plaintext, err := s.cipher.Decrypt(ciphertext, instanceStateEmptyMarker+instanceStateAAD(installationID, scope, key))
		if err != nil {
			return nil, fmt.Errorf("decrypting empty plugin instance state: %w", err)
		}
		if plaintext != "empty" {
			return nil, errors.New("invalid empty plugin instance state marker")
		}
		return []byte{}, nil
	}
	if !secret.IsEncrypted(string(stored)) {
		return nil, errors.New("plugin instance state row is not an encrypted envelope")
	}
	plaintext, err := s.cipher.Decrypt(string(stored), instanceStateAAD(installationID, scope, key))
	if err != nil {
		return nil, fmt.Errorf("decrypting plugin instance state: %w", err)
	}
	return []byte(plaintext), nil
}

// ScopedInstanceStateStore is an InstanceStateStore bound to one fixed host
// scope for a plugin process. It satisfies pluginhost.InstanceStateStore.
type ScopedInstanceStateStore struct {
	store *InstanceStateStore
	scope string
}

// Scope returns the bound host scope.
func (s *ScopedInstanceStateStore) Scope() string { return s.scope }

// ReadInstanceState implements pluginhost.InstanceStateStore.
func (s *ScopedInstanceStateStore) ReadInstanceState(ctx context.Context, installationID int, key string) ([]byte, int64, error) {
	return s.store.Read(ctx, installationID, s.scope, key)
}

// WriteInstanceState implements pluginhost.InstanceStateStore.
func (s *ScopedInstanceStateStore) WriteInstanceState(ctx context.Context, installationID int, key string, value []byte, expectedVersion *int64) (int64, error) {
	return s.store.Write(ctx, installationID, s.scope, key, value, expectedVersion)
}

var _ pluginhost.InstanceStateStore = (*ScopedInstanceStateStore)(nil)
