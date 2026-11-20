package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
)

// removedCredentialPaths finds saved credential paths the next manifest no
// longer protects. Keep them protected even if the next release drops an
// entry altogether: a later release could declare it public again.
func removedCredentialPaths(before *pluginv1.PluginManifest, key string, protected [][]string) [][]string {
	paths := GlobalConfigSecretPaths(before, key)
	return slices.DeleteFunc(paths, func(path []string) bool {
		return slices.ContainsFunc(protected, func(next []string) bool {
			return len(next) <= len(path) && slices.Equal(next, path[:len(next)])
		})
	})
}

func protectRetainedCredentials(key string, removed, protected [][]string, before, after map[string]any) error {
	var exposed credentialValues
	for _, path := range removed {
		saved, _ := configPathValue(before, path)
		if !populatedCredential(saved) {
			continue
		}
		if exposed == nil {
			exposed = make(credentialValues)
			collectCredentialValues(exposed, after, protected)
		}
		values := make(credentialValues)
		collectCredentialValues(values, saved, nil)
		retained := false
		switch saved.(type) {
		case map[string]any, []any:
			// A schema with dynamic properties or arrays can mark an opaque
			// container sensitive. Keep requiring it to be cleared as a unit.
			retained = populatedCredential(after[path[0]])
		}
		if retained || values.overlaps(exposed) {
			return fmt.Errorf("config entry %q has a saved credential at %q that this release no longer protects",
				key, strings.Join(path, "."))
		}
	}
	return nil
}

// String and boolean keys match directly. Numbers in the same floating-point
// bucket still need exact comparison: large, distinct JSON integers can round
// to the same double.
type credentialValues map[any][]json.Number

func (v credentialValues) overlaps(other credentialValues) bool {
	for key, numbers := range v {
		matches, ok := other[key]
		if !ok {
			continue
		}
		if numbers == nil {
			return true
		}
		for _, number := range numbers {
			if slices.ContainsFunc(matches, func(match json.Number) bool { return sameConfigValue(number, match) }) {
				return true
			}
		}
	}
	return false
}

// Index scalar values wherever they occur in the config, skipping paths that
// the replacement still protects. Include undeclared fields because a later
// release could make them public without another change to the config.
func collectCredentialValues(values credentialValues, value any, protected [][]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			nested, whole := childSecretPaths(protected, key)
			if !whole {
				collectCredentialValues(values, child, nested)
			}
		}
	case []any:
		for _, child := range v {
			collectCredentialValues(values, child, nil)
		}
	case json.Number:
		bucket, _ := v.Float64()
		values[bucket] = append(values[bucket], v)
	default:
		if populatedCredential(v) {
			values[v] = nil
		}
	}
}

func unsafeReplacementConfigMessage(err error) string {
	return "Silo rejected this release because " + err.Error() + ". Clear that value or choose a compatible release."
}

func populatedCredential(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case string:
		return value != ""
	case map[string]any:
		return len(value) != 0
	case []any:
		return len(value) != 0
	default:
		return true
	}
}

// guardReplacementCredentials runs under the parent installation lock while
// its current manifest and saved settings still describe the same release.
func (s *InstallationStore) guardReplacementCredentials(ctx context.Context, tx pgx.Tx, installation *Installation, candidate *pluginv1.PluginManifest) error {
	var oldManifestJSON []byte
	err := tx.QueryRow(ctx, `SELECT (SELECT manifest_json FROM plugin_archives WHERE plugin_installation_id = $1)
		WHERE EXISTS (SELECT 1 FROM plugin_runtime_configs WHERE plugin_installation_id = $1)`,
		installation.ID).Scan(&oldManifestJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	var previous *pluginv1.PluginManifest
	if len(oldManifestJSON) != 0 {
		previous, err = LoadManifestBytes(oldManifestJSON)
		if err != nil || previous.GetPluginId() != installation.PluginID || previous.GetVersion() != installation.Version {
			return packageError(err, "Silo could not verify how the saved settings for this plugin must be redacted.")
		}
	} else if installation.Signature.State != SignatureStateVerified {
		// Legacy installations can have only the manifest beside the installed binary.
		previous = matchingInstalledManifest(InstalledManifestPath(installation.InstallPath), installation)
	}
	if previous == nil {
		return s.guardReplacementWithoutManifest(ctx, tx, installation.ID)
	}
	for _, schema := range previous.GetGlobalConfigSchema() {
		key := schema.GetKey()
		protected := GlobalConfigSecretPaths(candidate, key)
		paths := removedCredentialPaths(previous, key, protected)
		if len(paths) == 0 {
			continue
		}
		current, err := readGlobalConfigInTx(ctx, tx, s.cipher, installation.ID, key)
		if err != nil {
			return err
		}
		if err := protectRetainedCredentials(key, paths, protected, current, current); err != nil {
			return &PackageError{Message: unsafeReplacementConfigMessage(err)}
		}
	}
	return nil
}

// Without the old manifest, every populated saved setting might be a credential.
func (s *InstallationStore) guardReplacementWithoutManifest(ctx context.Context, tx pgx.Tx, installationID int) error {
	rows, err := tx.Query(ctx, `SELECT config_key, config_value FROM plugin_runtime_configs WHERE plugin_installation_id = $1`, installationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return err
		}
		config, err := decodeRuntimeConfigValue(s.cipher, installationID, key, raw)
		if err != nil {
			return err
		}
		for value := range maps.Values(config) {
			if populatedCredential(value) {
				return packageError(nil, "Silo could not verify how the saved settings for this plugin must be redacted.")
			}
		}
	}
	return rows.Err()
}
