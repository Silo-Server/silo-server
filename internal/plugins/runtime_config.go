package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

var (
	ErrAuthBindingNotFound = errors.New("plugin auth binding not found")
	ErrTaskBindingNotFound = errors.New("plugin task binding not found")
)

type RuntimeConfig struct {
	InstallationID int
	Key            string
	Value          map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type AuthBinding struct {
	InstallationID int
	CapabilityID   string
	Enabled        bool
	DisplayOrder   int
	AutoProvision  bool
	DefaultLogin   bool
	CallbackMode   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type TaskBinding struct {
	InstallationID int
	CapabilityID   string
	Enabled        bool
	Trigger        map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type RuntimeConfigStore struct {
	provisionWriter *ProvisionWriter
	pool            *pgxpool.Pool
	cipher          *secret.Cipher
}

// plugin_runtime_configs.config_value is intentionally an opaque, whole-row
// encrypted envelope. Plugins may write undeclared keys and manifest secret
// annotations can drift or be unavailable during startup backfill, so field
// classification is an Admin redaction concern, not the at-rest boundary.
// Runtime code must use RuntimeConfigStore rather than querying JSON members.
const encryptedRuntimeConfigField = "__silo_encrypted_runtime_config_v1"

// NewRuntimeConfigStore creates the plugin config store. Production callers
// pass the server data cipher; the variadic form keeps DB-only tests concise.
func NewRuntimeConfigStore(pool *pgxpool.Pool, ciphers ...*secret.Cipher) *RuntimeConfigStore {
	var cipher *secret.Cipher
	if len(ciphers) > 0 {
		cipher = ciphers[0]
	}
	return &RuntimeConfigStore{pool: pool, cipher: cipher}
}

func (s *RuntimeConfigStore) PutGlobalConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockPluginInstallation(ctx, tx, installationID); err != nil {
			return err
		}
		ownership, err := LoadProvisionOwnership(ctx, tx, installationID)
		if err != nil {
			return err
		}
		current, err := readGlobalConfigInTx(ctx, tx, s.cipher, installationID, key)
		if err != nil {
			return err
		}
		if value == nil {
			value = map[string]any{}
		}
		merged := preserveOwnedConfig(ownership.ConfigFields[key], current, value)
		if current != nil && SameConfig(current, merged) {
			return nil
		}
		if current == nil && len(merged) == 0 && len(ownership.ConfigFields[key]) > 0 {
			return nil
		}
		return upsertGlobalConfig(ctx, tx, s.cipher, installationID, key, merged)
	})
}

func readGlobalConfigInTx(ctx context.Context, tx pgx.Tx, cipher *secret.Cipher, installationID int, key string) (map[string]any, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT config_value FROM plugin_runtime_configs
		WHERE plugin_installation_id = $1 AND config_key = $2`, installationID, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading plugin runtime config: %w", err)
	}
	return decodeRuntimeConfigValue(cipher, installationID, key, raw)
}

func upsertGlobalConfig(ctx context.Context, tx pgx.Tx, cipher *secret.Cipher, installationID int, key string, value map[string]any) error {
	valueJSON, err := encodeRuntimeConfigValue(cipher, installationID, key, value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
		VALUES ($1, $2, $3)
		ON CONFLICT (plugin_installation_id, config_key) DO UPDATE SET
			config_value = EXCLUDED.config_value, updated_at = NOW()
	`, installationID, key, valueJSON)
	if err != nil {
		return fmt.Errorf("upserting plugin runtime config: %w", err)
	}
	return nil
}

// CompareAndSwapGlobalConfig persists value only when the row still matches
// the version the caller merged. A nil expectedUpdatedAt creates the row only
// when it does not already exist. An admin save also advances the durable
// runtime generation in the same transaction, so proxy polls recover a
// missed lifecycle event. Plugin-originated writes use PutGlobalConfig.
func (s *RuntimeConfigStore) CompareAndSwapGlobalConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
	expectedUpdatedAt *time.Time,
) (bool, error) {
	return s.compareAndSwapGlobalConfig(ctx, installationID, key, value, expectedUpdatedAt, nil, nil, nil)
}

func (s *RuntimeConfigStore) compareAndSwapGlobalConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
	expectedUpdatedAt *time.Time,
	expectedPackage *Installation,
	clears map[string]struct{},
	validate func() error,
) (bool, error) {

	if value == nil {
		value = map[string]any{}
	}
	valueJSON, err := encodeRuntimeConfigValue(s.cipher, installationID, key, value)
	if err != nil {
		return false, fmt.Errorf("marshaling plugin runtime config: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin plugin config update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the parent installation before touching plugin_runtime_configs.
	// InstallationStore.Update takes this lock first; acquiring it here keeps
	// the FK key-share lock and generation update in one order and prevents a
	// concurrent lifecycle update from deadlocking with a config save.
	if err := lockPluginInstallation(ctx, tx, installationID); err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			return false, nil
		}
		return false, err
	}
	if expectedPackage != nil {
		var version, path string
		if err := tx.QueryRow(ctx, `SELECT version, install_path FROM plugin_installations WHERE id = $1`, installationID).Scan(&version, &path); err != nil {
			return false, err
		}
		if version != expectedPackage.Version || path != expectedPackage.InstallPath {
			return false, ErrInstallationChanged
		}
	}

	matches, err := s.checkOwnedConfigCAS(ctx, tx, installationID, key, value, expectedUpdatedAt, clears)
	if err != nil || !matches {
		return false, err
	}

	if validate != nil {
		if err := validate(); err != nil {
			return false, err
		}
	}

	var tag pgconn.CommandTag
	if expectedUpdatedAt == nil {
		tag, err = tx.Exec(ctx, `
			INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
			VALUES ($1, $2, $3)
			ON CONFLICT (plugin_installation_id, config_key) DO NOTHING
		`, installationID, key, valueJSON)
	} else {
		tag, err = tx.Exec(ctx, `
			UPDATE plugin_runtime_configs
			SET config_value = $3, updated_at = NOW()
			WHERE plugin_installation_id = $1
				AND config_key = $2
				AND updated_at = $4
		`, installationID, key, valueJSON, *expectedUpdatedAt)
	}
	if err != nil {
		return false, fmt.Errorf("compare-and-swap plugin runtime config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE plugin_installations
		SET runtime_generation = runtime_generation + 1, updated_at = NOW()
		WHERE id = $1`, installationID); err != nil {
		return false, fmt.Errorf("advance plugin runtime generation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit plugin config update: %w", err)
	}
	return true, nil
}

func (s *RuntimeConfigStore) checkOwnedConfigCAS(
	ctx context.Context, tx pgx.Tx, installationID int, key string, value map[string]any,
	expectedUpdatedAt *time.Time, clears map[string]struct{},
) (bool, error) {
	ownership, err := LoadProvisionOwnership(ctx, tx, installationID)
	if err != nil {
		return false, err
	}
	fields := ownership.ConfigFields[key]
	if len(fields) == 0 {
		return true, nil
	}
	var updatedAt time.Time
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT config_value, updated_at FROM plugin_runtime_configs
		WHERE plugin_installation_id = $1 AND config_key = $2`, installationID, key).Scan(&raw, &updatedAt)
	found := err == nil
	if !found && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("reading config for compare-and-swap: %w", err)
	}
	switch {
	case expectedUpdatedAt == nil && found, expectedUpdatedAt != nil && !found:
		return false, nil
	case found && !updatedAt.Equal(*expectedUpdatedAt):
		return false, nil
	}
	for _, field := range fields {
		if _, clearing := clears[field]; clearing {
			return false, &ManagedByEnvironmentError{Field: "config." + key + "." + field}
		}
	}
	current, err := decodeRuntimeConfigValue(s.cipher, installationID, key, raw)
	if err != nil {
		return false, err
	}
	if err := requireUnchangedOwnedConfig(key, fields, current, value); err != nil {
		return false, err
	}
	return true, nil
}

func (s *RuntimeConfigStore) ListGlobalConfigs(ctx context.Context, installationID int) ([]*RuntimeConfig, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT plugin_installation_id, config_key, config_value, created_at, updated_at
		FROM plugin_runtime_configs
		WHERE plugin_installation_id = $1
		ORDER BY config_key ASC
	`, installationID)
	if err != nil {
		return nil, fmt.Errorf("listing plugin runtime configs: %w", err)
	}
	defer rows.Close()

	var configs []*RuntimeConfig
	for rows.Next() {
		var config RuntimeConfig
		var valueJSON []byte
		if err := rows.Scan(
			&config.InstallationID,
			&config.Key,
			&valueJSON,
			&config.CreatedAt,
			&config.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning plugin runtime config: %w", err)
		}
		config.Value, err = decodeRuntimeConfigValue(s.cipher, config.InstallationID, config.Key, valueJSON)
		if err != nil {
			return nil, fmt.Errorf("decoding plugin runtime config %d/%s: %w", config.InstallationID, config.Key, err)
		}
		configs = append(configs, &config)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin runtime configs: %w", err)
	}
	return configs, nil
}

func runtimeConfigAAD(installationID int, key string) string {
	return secret.RowAAD(
		"plugin_runtime_configs",
		"config_value",
		strconv.Itoa(installationID)+":"+key,
	)
}

func encodeRuntimeConfigValue(
	cipher *secret.Cipher,
	installationID int,
	key string,
	value map[string]any,
) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	plaintext, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshaling plugin runtime config: %w", err)
	}
	return encodeRuntimeConfigJSON(cipher, installationID, key, plaintext)
}

func encodeRuntimeConfigJSON(
	cipher *secret.Cipher,
	installationID int,
	key string,
	plaintext []byte,
) ([]byte, error) {
	if !json.Valid(plaintext) {
		return nil, errors.New("plugin runtime config is not valid JSON")
	}
	if cipher == nil {
		return append([]byte(nil), plaintext...), nil
	}
	ciphertext, err := cipher.Encrypt(string(plaintext), runtimeConfigAAD(installationID, key))
	if err != nil {
		return nil, fmt.Errorf("encrypting plugin runtime config: %w", err)
	}
	wrapped, err := json.Marshal(map[string]string{encryptedRuntimeConfigField: ciphertext})
	if err != nil {
		return nil, fmt.Errorf("marshaling encrypted plugin runtime config: %w", err)
	}
	return wrapped, nil
}

func decodeRuntimeConfigValue(
	cipher *secret.Cipher,
	installationID int,
	key string,
	valueJSON []byte,
) (map[string]any, error) {
	if len(valueJSON) == 0 {
		return map[string]any{}, nil
	}
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(valueJSON, &wrapped); err != nil {
		return nil, fmt.Errorf("unmarshaling plugin runtime config: %w", err)
	}
	if rawCiphertext, ok := wrapped[encryptedRuntimeConfigField]; ok && len(wrapped) == 1 {
		if cipher == nil {
			return nil, errors.New("encrypted plugin runtime config requires the server data cipher")
		}
		var ciphertext string
		if err := json.Unmarshal(rawCiphertext, &ciphertext); err != nil || !secret.IsEncrypted(ciphertext) {
			return nil, errors.New("invalid encrypted plugin runtime config envelope")
		}
		plaintext, err := cipher.Decrypt(ciphertext, runtimeConfigAAD(installationID, key))
		if err != nil {
			return nil, fmt.Errorf("decrypting plugin runtime config: %w", err)
		}
		valueJSON = []byte(plaintext)
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(valueJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("unmarshaling plugin runtime config value: %w", err)
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, nil
}

// BackfillEncryptedConfigs wraps legacy plaintext JSON objects with the same
// row-bound encryption used by PutGlobalConfig. It is idempotent and may be
// rerun after a partial failure.
func (s *RuntimeConfigStore) BackfillEncryptedConfigs(ctx context.Context) (int, error) {
	if s == nil || s.pool == nil || s.cipher == nil {
		return 0, nil
	}
	return backfillEncryptedConfigs(ctx, s.pool, s.cipher)
}

func backfillEncryptedConfigs(
	ctx context.Context,
	db secret.Executor,
	cipher *secret.Cipher,
) (int, error) {
	rows, err := db.Query(ctx, `
		SELECT id, plugin_installation_id, config_key, config_value
		FROM plugin_runtime_configs
		ORDER BY id ASC
	`)
	if err != nil {
		return 0, fmt.Errorf("listing plugin runtime configs for encryption backfill: %w", err)
	}
	type rowValue struct {
		id             int64
		installationID int
		key            string
		valueJSON      []byte
	}
	var pending []rowValue
	for rows.Next() {
		var row rowValue
		if err := rows.Scan(&row.id, &row.installationID, &row.key, &row.valueJSON); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning plugin runtime config for encryption backfill: %w", err)
		}
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(row.valueJSON, &wrapped); err != nil {
			rows.Close()
			return 0, fmt.Errorf("decode plugin runtime config %d for encryption backfill: %w", row.id, err)
		}
		if raw, ok := wrapped[encryptedRuntimeConfigField]; ok && len(wrapped) == 1 {
			var ciphertext string
			if json.Unmarshal(raw, &ciphertext) == nil && secret.IsEncrypted(ciphertext) {
				continue
			}
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterating plugin runtime configs for encryption backfill: %w", err)
	}
	rows.Close()

	updated := 0
	for _, row := range pending {
		encoded, err := encodeRuntimeConfigJSON(
			cipher,
			row.installationID,
			row.key,
			row.valueJSON,
		)
		if err != nil {
			return updated, fmt.Errorf("encrypt plugin runtime config %d: %w", row.id, err)
		}
		tag, err := db.Exec(ctx,
			`UPDATE plugin_runtime_configs
			 SET config_value = $2, updated_at = updated_at
			 WHERE id = $1 AND config_value = $3::jsonb`,
			row.id,
			encoded,
			row.valueJSON,
		)
		if err != nil {
			return updated, fmt.Errorf("update plugin runtime config %d encryption backfill: %w", row.id, err)
		}
		if tag.RowsAffected() > 0 {
			updated++
		}
	}
	return updated, nil
}

// ErrAuthProviderAlreadyEnabled refuses enabling an auth binding while
// another one of the same kind, or of the same installation, is enabled: a
// server has at most one primary external sign-in provider (OIDC, LDAP) and
// at most one network identity provider, plus the built-in local accounts.
// Identities and account rechecks are keyed by installation, so the two must
// come from different installations.
var ErrAuthProviderAlreadyEnabled = errors.New("another external sign-in provider is already enabled")

var ErrAuthBindingIneligible = errors.New("enabled sign-in binding is incompatible with the installed plugin")

// authBindingsLock serializes auth binding writes so two concurrent enables
// cannot both pass the one-provider check.
const authBindingsLock = "silo:plugin_auth_bindings"

// AuthBindingIsNetworkSQL is the SQL test that the auth binding named by the
// installation and capability expressions declares the "network" auth mode.
// It reads the capability's stored manifest metadata, so a binding's kind
// never drifts from what the plugin declares.
func AuthBindingIsNetworkSQL(installation, capability string) string {
	return `COALESCE((SELECT c.metadata->'auth_modes' ? 'network' FROM plugin_capabilities c
		WHERE c.plugin_installation_id = ` + installation + ` AND c.capability_type = 'auth_provider.v1'
			AND c.capability_id = ` + capability + `), false)`
}

// UpsertAuthBinding writes one binding row. Enabling it is refused with
// ErrAuthProviderAlreadyEnabled while a different binding of the same kind
// (network identity or not), or of the same installation, is enabled.
func (s *RuntimeConfigStore) UpsertAuthBinding(ctx context.Context, binding AuthBinding) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAuthBindings(ctx, tx); err != nil {
			return err
		}
		if err := lockPluginInstallation(ctx, tx, binding.InstallationID); err != nil {
			return err
		}
		ownership, err := LoadProvisionOwnership(ctx, tx, binding.InstallationID)
		if err != nil {
			return err
		}
		if fields := ownership.BindingFields[binding.CapabilityID]; len(fields) > 0 {
			if err := requireUnchangedOwnedBinding(ctx, tx, binding, fields); err != nil {
				return err
			}
		}
		if binding.CallbackMode != "" {
			var modes []string
			err := tx.QueryRow(ctx, `SELECT COALESCE(metadata->'auth_modes', '[]'::jsonb)
				FROM plugin_capabilities WHERE plugin_installation_id = $1 AND capability_type = 'auth_provider.v1' AND capability_id = $2`,
				binding.InstallationID, binding.CapabilityID).Scan(&modes)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("reading auth capability modes: %w", err)
			}
			if err := ValidateAuthCallbackMode(binding.CallbackMode, modes); err != nil {
				return err
			}
		}

		if err := upsertAuthBindingInTx(ctx, tx, binding); err != nil {
			return err
		}
		if binding.Enabled {
			return validateInstallationAuthBindings(ctx, tx, binding.InstallationID)
		}
		return nil

	})
}

func lockAuthBindings(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, authBindingsLock); err != nil {
		return fmt.Errorf("locking plugin auth bindings: %w", err)
	}
	return nil
}

func validateEnabledAuthBinding(ctx context.Context, tx pgx.Tx, binding AuthBinding) error {
	var other bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM plugin_auth_bindings b JOIN plugin_installations i ON i.id = b.plugin_installation_id
		WHERE b.enabled AND i.enabled AND NOT (b.plugin_installation_id = $1 AND b.capability_id = $2)
			AND (b.plugin_installation_id = $1
				OR `+AuthBindingIsNetworkSQL("b.plugin_installation_id", "b.capability_id")+` =
					`+AuthBindingIsNetworkSQL("$1::bigint", "$2::text")+`))`,
		binding.InstallationID, binding.CapabilityID).Scan(&other); err != nil {
		return fmt.Errorf("checking enabled plugin auth bindings: %w", err)
	}
	if other {
		return ErrAuthProviderAlreadyEnabled
	}
	return nil
}

func validateInstallationAuthBindings(ctx context.Context, tx pgx.Tx, installationID int) error {
	rows, err := tx.Query(ctx, `
		SELECT b.capability_id, b.callback_mode, COALESCE(c.metadata->'auth_modes', '[]'::jsonb),
		       c.capability_id IS NOT NULL, i.enabled
		FROM plugin_auth_bindings b
		JOIN plugin_installations i ON i.id = b.plugin_installation_id
		LEFT JOIN plugin_capabilities c ON c.plugin_installation_id = i.id
			AND c.capability_type = 'auth_provider.v1' AND c.capability_id = b.capability_id
		WHERE b.plugin_installation_id = $1 AND b.enabled`, installationID)
	if err != nil {
		return err
	}
	type bindingEligibility struct {
		CapabilityID  string
		CallbackMode  string
		AuthModes     []string
		HasCapability bool
		Active        bool
	}
	bindings, err := pgx.CollectRows(rows, pgx.RowToStructByPos[bindingEligibility])
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if !binding.HasCapability ||
			(binding.CallbackMode == AuthCallbackStable && ValidateAuthCallbackMode(binding.CallbackMode, binding.AuthModes) != nil) {
			return fmt.Errorf("%w: %s", ErrAuthBindingIneligible, binding.CapabilityID)
		}
		if binding.Active {
			if err := validateEnabledAuthBinding(ctx, tx, AuthBinding{InstallationID: installationID, CapabilityID: binding.CapabilityID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireUnchangedOwnedBinding(ctx context.Context, tx pgx.Tx, input AuthBinding, fields []string) error {
	var current AuthBinding
	err := tx.QueryRow(ctx, `SELECT enabled, display_order, auto_provision, default_login, callback_mode
		FROM plugin_auth_bindings WHERE plugin_installation_id = $1 AND capability_id = $2`,
		input.InstallationID, input.CapabilityID).
		Scan(&current.Enabled, &current.DisplayOrder, &current.AutoProvision, &current.DefaultLogin, &current.CallbackMode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("reading plugin auth binding for ownership: %w", err)
	}
	for _, field := range fields {
		changed := false
		switch field {
		case "enabled":
			changed = current.Enabled != input.Enabled
		case "display_order":
			changed = current.DisplayOrder != input.DisplayOrder
		case "auto_provision":
			changed = current.AutoProvision != input.AutoProvision
		case "default_login":
			changed = current.DefaultLogin != input.DefaultLogin
		case "callback", "callback_mode":
			changed = input.CallbackMode != "" && current.CallbackMode != input.CallbackMode
		}
		if err != nil || changed {
			return &ManagedByEnvironmentError{Field: "auth_binding." + field}
		}
	}
	return nil
}

func upsertAuthBindingInTx(ctx context.Context, tx pgx.Tx, binding AuthBinding) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO plugin_auth_bindings (
			plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login, callback_mode
		) VALUES ($1, $2, $3, $4, $5, $6, COALESCE(NULLIF($7, ''), 'installation'))
		ON CONFLICT (plugin_installation_id, capability_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			display_order = EXCLUDED.display_order,
			auto_provision = EXCLUDED.auto_provision,
			default_login = EXCLUDED.default_login,
			callback_mode = COALESCE(NULLIF($7, ''), plugin_auth_bindings.callback_mode),
			updated_at = NOW()
	`, binding.InstallationID, binding.CapabilityID, binding.Enabled, binding.DisplayOrder,
		binding.AutoProvision, binding.DefaultLogin, binding.CallbackMode); err != nil {
		return fmt.Errorf("upserting plugin auth binding: %w", err)
	}
	return nil
}

func (s *RuntimeConfigStore) GetAuthBinding(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*AuthBinding, error) {
	var binding AuthBinding
	err := s.pool.QueryRow(ctx, `
		SELECT plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login, callback_mode, created_at, updated_at
		FROM plugin_auth_bindings
		WHERE plugin_installation_id = $1 AND capability_id = $2
	`, installationID, capabilityID).Scan(
		&binding.InstallationID,
		&binding.CapabilityID,
		&binding.Enabled,
		&binding.DisplayOrder,
		&binding.AutoProvision,
		&binding.DefaultLogin,
		&binding.CallbackMode,
		&binding.CreatedAt,
		&binding.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAuthBindingNotFound
		}
		return nil, fmt.Errorf("getting plugin auth binding: %w", err)
	}
	return &binding, nil
}

func (s *RuntimeConfigStore) ListAuthBindings(ctx context.Context) ([]*AuthBinding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login, callback_mode, created_at, updated_at
		FROM plugin_auth_bindings
		ORDER BY display_order ASC, plugin_installation_id ASC, capability_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing plugin auth bindings: %w", err)
	}
	defer rows.Close()

	var bindings []*AuthBinding
	for rows.Next() {
		var binding AuthBinding
		if err := rows.Scan(
			&binding.InstallationID,
			&binding.CapabilityID,
			&binding.Enabled,
			&binding.DisplayOrder,
			&binding.AutoProvision,
			&binding.DefaultLogin,
			&binding.CallbackMode,
			&binding.CreatedAt,
			&binding.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning plugin auth binding: %w", err)
		}
		bindings = append(bindings, &binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin auth bindings: %w", err)
	}
	return bindings, nil
}

func (s *RuntimeConfigStore) UpsertTaskBinding(ctx context.Context, binding TaskBinding) error {
	trigger := binding.Trigger
	if trigger == nil {
		trigger = map[string]any{}
	}
	triggerJSON, err := json.Marshal(trigger)
	if err != nil {
		return fmt.Errorf("marshaling plugin task binding trigger: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO plugin_task_bindings (plugin_installation_id, capability_id, enabled, trigger)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (plugin_installation_id, capability_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			trigger = EXCLUDED.trigger,
			updated_at = NOW()
	`,
		binding.InstallationID,
		binding.CapabilityID,
		binding.Enabled,
		triggerJSON,
	)
	if err != nil {
		return fmt.Errorf("upserting plugin task binding: %w", err)
	}
	return nil
}

func (s *RuntimeConfigStore) GetTaskBinding(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (*TaskBinding, error) {
	var binding TaskBinding
	var triggerJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT plugin_installation_id, capability_id, enabled, trigger, created_at, updated_at
		FROM plugin_task_bindings
		WHERE plugin_installation_id = $1 AND capability_id = $2
	`, installationID, capabilityID).Scan(
		&binding.InstallationID,
		&binding.CapabilityID,
		&binding.Enabled,
		&triggerJSON,
		&binding.CreatedAt,
		&binding.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTaskBindingNotFound
		}
		return nil, fmt.Errorf("getting plugin task binding: %w", err)
	}
	binding.Trigger = map[string]any{}
	if len(triggerJSON) > 0 {
		if err := json.Unmarshal(triggerJSON, &binding.Trigger); err != nil {
			return nil, fmt.Errorf("unmarshaling plugin task binding trigger: %w", err)
		}
	}
	return &binding, nil
}

func (s *RuntimeConfigStore) ListTaskBindings(ctx context.Context) ([]*TaskBinding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT plugin_installation_id, capability_id, enabled, trigger, created_at, updated_at
		FROM plugin_task_bindings
		ORDER BY plugin_installation_id ASC, capability_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing plugin task bindings: %w", err)
	}
	defer rows.Close()

	var bindings []*TaskBinding
	for rows.Next() {
		var binding TaskBinding
		var triggerJSON []byte
		if err := rows.Scan(
			&binding.InstallationID,
			&binding.CapabilityID,
			&binding.Enabled,
			&triggerJSON,
			&binding.CreatedAt,
			&binding.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning plugin task binding: %w", err)
		}
		binding.Trigger = map[string]any{}
		if len(triggerJSON) > 0 {
			if err := json.Unmarshal(triggerJSON, &binding.Trigger); err != nil {
				return nil, fmt.Errorf("unmarshaling plugin task binding trigger: %w", err)
			}
		}
		bindings = append(bindings, &binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugin task bindings: %w", err)
	}
	return bindings, nil
}
