package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
)

// RestoreInstallationInput is one plugin from a backup's restore set.
type RestoreInstallationInput struct {
	PluginID string
	Version  string
	// SourceInstallationID is the installation id the configuration rows
	// were encrypted under on the source server.
	SourceInstallationID int
	// Configs are plugin_runtime_configs rows as stored in the backup.
	Configs []RestoreConfig
	// ConfigUnreadable is set when the backup's key differs from this
	// server's: Configs cannot be decrypted and are not offered.
	ConfigUnreadable bool
}

// RestoreConfig is one stored configuration row.
type RestoreConfig struct {
	Key    string
	Stored json.RawMessage
}

// RestoreConfigProblem is a configuration value a restore did not write.
type RestoreConfigProblem struct {
	Key    string
	Reason string
}

// RestoreInstallationResult says what RestoreInstallation did.
type RestoreInstallationResult struct {
	InstallationID int
	// SettingsOnly is true when another version is installed here: only
	// configuration was restored.
	SettingsOnly  bool
	InstalledHere string
	Enabled       bool
	Written       []string
	Rejected      []RestoreConfigProblem
}

// Changed reports whether the restore wrote anything.
func (r RestoreInstallationResult) Changed() bool { return len(r.Written) > 0 }

// ErrRestoreUnavailable reports a service built without the concrete stores
// a restore writes through.
var ErrRestoreUnavailable = errors.New("plugin restore is not available on this server")

// ErrPluginNotInstalled refuses restoring a plugin this server does not
// have: a restore never installs one. Its binary would come from the backup,
// which only the backup's own checksums vouch for, and anyone who can write
// the backup destination could swap it. An admin installs it from a plugin
// catalog, and restoring it again then applies its settings.
var ErrPluginNotInstalled = errors.New("the plugin is not installed on this server; install it from a plugin catalog, then restore it again")

// PrepareRestore reads the installed manifest of plugin pluginID on the
// pool, before a restore opens its transaction, so RestoreInstallation finds
// it in the archive cache rather than reading the stored archive inside the
// transaction. A plugin not installed here needs nothing.
func (s *Service) PrepareRestore(ctx context.Context, pluginID string) error {
	store, ok := s.installations.(*InstallationStore)
	if !ok {
		return ErrRestoreUnavailable
	}
	existing, err := store.ListByPluginID(ctx, pluginID)
	if err != nil {
		return err
	}
	for _, inst := range existing {
		if !inst.IsBuiltin() {
			if _, err := s.readInstalledManifest(ctx, inst); err != nil {
				return err
			}
		}
	}
	return nil
}

// RestoreInstallation restores one installed plugin's configuration inside
// the caller's transaction. It never installs a plugin: one missing here is
// ErrPluginNotInstalled. The installation keeps its enabled state. At the
// same version its configuration is decrypted with this server's key under
// the source installation id, checked by the same validation an admin save
// uses, and written re-encrypted under this installation's id; at another
// version only values the installed version accepts are written.
//
// The returned func runs once the transaction commits: it stops reconfigured
// runtimes and publishes the lifecycle change (EventPluginsChanged).
func (s *Service) RestoreInstallation(ctx context.Context, tx pgx.Tx, in RestoreInstallationInput) (RestoreInstallationResult, func(context.Context), error) {
	var result RestoreInstallationResult
	store, ok := s.installations.(*InstallationStore)
	configs, okConfigs := s.configs.(*RuntimeConfigStore)
	if !ok || !okConfigs || tx == nil {
		return result, nil, ErrRestoreUnavailable
	}
	txStore := store.WithTx(tx)
	existing, err := txStore.ListByPluginID(ctx, in.PluginID)
	if err != nil {
		return result, nil, err
	}
	var installs []*Installation
	for _, inst := range existing {
		if !inst.IsBuiltin() {
			installs = append(installs, inst)
		}
	}
	if len(installs) > 1 {
		return result, nil, fmt.Errorf("multiple installations of plugin %q exist", in.PluginID)
	}

	if len(installs) == 0 {
		return result, nil, ErrPluginNotInstalled
	}
	inst := installs[0]
	// The installation and its manifest are read through the transaction:
	// it may hold the pool's last connection.
	manifest, err := s.readInstalledManifestFrom(ctx, inst, txStore)
	if err != nil {
		return result, nil, err
	}
	result.InstallationID, result.Enabled, result.InstalledHere = inst.ID, inst.Enabled, inst.Version
	result.SettingsOnly = inst.Version != in.Version

	current, err := storedConfigs(ctx, tx, configs, result.InstallationID)
	if err != nil {
		return result, nil, err
	}
	for _, c := range in.Configs {
		if in.ConfigUnreadable {
			result.Rejected = append(result.Rejected, RestoreConfigProblem{Key: c.Key, Reason: "it was encrypted with another SECRET_KEY"})
			continue
		}
		value, err := decodeRuntimeConfigValue(configs.cipher, in.SourceInstallationID, c.Key, c.Stored)
		if err != nil {
			result.Rejected = append(result.Rejected, RestoreConfigProblem{Key: c.Key, Reason: "it cannot be decrypted with this server's key"})
			continue
		}
		if err := ValidateGlobalConfigValue(manifest, c.Key, globalConfigValidationProjection(manifest, c.Key, value, value)); err != nil {
			result.Rejected = append(result.Rejected, RestoreConfigProblem{Key: c.Key, Reason: err.Error()})
			continue
		}
		if have, ok := current[c.Key]; ok && SameConfig(have, value) {
			continue
		}
		if err := putConfigInTx(ctx, tx, configs, result.InstallationID, c.Key, value); err != nil {
			return result, nil, err
		}
		result.Written = append(result.Written, c.Key)
	}

	id, changed := result.InstallationID, result.Changed()
	after := func(ctx context.Context) {
		if !changed {
			return
		}
		if s.host != nil {
			// A stop that fails leaves a stale process; the lifecycle
			// change below and the advanced runtime generation replace it.
			_ = s.host.Stop(id)
		}
		s.resident.Reset(id)
		s.OnLifecycleChange(ctx)
	}
	return result, after, nil
}

// storedConfigs reads an installation's configuration in tx, decrypted.
func storedConfigs(ctx context.Context, tx pgx.Tx, store *RuntimeConfigStore, installationID int) (map[string]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT config_key, config_value FROM plugin_runtime_configs WHERE plugin_installation_id = $1`, installationID)
	if err != nil {
		return nil, fmt.Errorf("reading plugin configuration: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]any{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		value, err := decodeRuntimeConfigValue(store.cipher, installationID, key, raw)
		if err != nil {
			continue // unreadable here: a restore overwrites it
		}
		out[key] = value
	}
	return out, rows.Err()
}

// putConfigInTx writes one configuration row encrypted under the target
// installation id and advances its runtime generation, as an admin save
// does, in the caller's transaction.
func putConfigInTx(ctx context.Context, tx pgx.Tx, store *RuntimeConfigStore, installationID int, key string, value map[string]any) error {
	valueJSON, err := encodeRuntimeConfigValue(store.cipher, installationID, key, value)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM plugin_installations WHERE id = $1 FOR NO KEY UPDATE`, installationID); err != nil {
		return fmt.Errorf("lock plugin installation for config update: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
		VALUES ($1, $2, $3)
		ON CONFLICT (plugin_installation_id, config_key) DO UPDATE SET config_value = EXCLUDED.config_value, updated_at = NOW()
	`, installationID, key, valueJSON); err != nil {
		return fmt.Errorf("writing plugin runtime config: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET runtime_generation = runtime_generation + 1, updated_at = NOW() WHERE id = $1`, installationID); err != nil {
		return fmt.Errorf("advance plugin runtime generation: %w", err)
	}
	return nil
}

// SameConfig reports whether two decoded configuration values are equal. It
// compares their JSON form, so number representations do not count as a
// difference.
func SameConfig(a, b map[string]any) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return reflect.DeepEqual(a, b)
	}
	var x, y any
	if json.Unmarshal(left, &x) != nil || json.Unmarshal(right, &y) != nil {
		return string(left) == string(right)
	}
	return reflect.DeepEqual(x, y)
}

// DecodeStoredConfig decrypts a stored plugin_runtime_configs value with
// this service's key under installationID, for a restore preview.
func (s *Service) DecodeStoredConfig(installationID int, key string, stored []byte) (map[string]any, error) {
	configs, ok := s.configs.(*RuntimeConfigStore)
	if !ok {
		return nil, ErrRestoreUnavailable
	}
	return decodeRuntimeConfigValue(configs.cipher, installationID, key, stored)
}
