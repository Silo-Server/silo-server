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
	pool   *pgxpool.Pool
	cipher *secret.Cipher
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
	if value == nil {
		value = map[string]any{}
	}
	valueJSON, err := encodeRuntimeConfigValue(s.cipher, installationID, key, value)
	if err != nil {
		return fmt.Errorf("marshaling plugin runtime config: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
		VALUES ($1, $2, $3)
		ON CONFLICT (plugin_installation_id, config_key) DO UPDATE SET
			config_value = EXCLUDED.config_value,
			updated_at = NOW()
	`, installationID, key, valueJSON)
	if err != nil {
		return fmt.Errorf("upserting plugin runtime config: %w", err)
	}
	return nil
}

// CompareAndSwapGlobalConfig persists value only when the row still matches
// the version the caller merged. A nil expectedUpdatedAt creates the row only
// when it does not already exist.
func (s *RuntimeConfigStore) CompareAndSwapGlobalConfig(
	ctx context.Context,
	installationID int,
	key string,
	value map[string]any,
	expectedUpdatedAt *time.Time,
) (bool, error) {
	if value == nil {
		value = map[string]any{}
	}
	valueJSON, err := encodeRuntimeConfigValue(s.cipher, installationID, key, value)
	if err != nil {
		return false, fmt.Errorf("marshaling plugin runtime config: %w", err)
	}

	var tag pgconn.CommandTag
	if expectedUpdatedAt == nil {
		tag, err = s.pool.Exec(ctx, `
			INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
			VALUES ($1, $2, $3)
			ON CONFLICT (plugin_installation_id, config_key) DO NOTHING
		`, installationID, key, valueJSON)
	} else {
		tag, err = s.pool.Exec(ctx, `
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
	return tag.RowsAffected() > 0, nil
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

func (s *RuntimeConfigStore) UpsertAuthBinding(ctx context.Context, binding AuthBinding) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO plugin_auth_bindings (
			plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (plugin_installation_id, capability_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			display_order = EXCLUDED.display_order,
			auto_provision = EXCLUDED.auto_provision,
			default_login = EXCLUDED.default_login,
			updated_at = NOW()
	`,
		binding.InstallationID,
		binding.CapabilityID,
		binding.Enabled,
		binding.DisplayOrder,
		binding.AutoProvision,
		binding.DefaultLogin,
	)
	if err != nil {
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
		SELECT plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login, created_at, updated_at
		FROM plugin_auth_bindings
		WHERE plugin_installation_id = $1 AND capability_id = $2
	`, installationID, capabilityID).Scan(
		&binding.InstallationID,
		&binding.CapabilityID,
		&binding.Enabled,
		&binding.DisplayOrder,
		&binding.AutoProvision,
		&binding.DefaultLogin,
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
		SELECT plugin_installation_id, capability_id, enabled, display_order, auto_provision, default_login, created_at, updated_at
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
