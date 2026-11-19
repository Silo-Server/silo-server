package plugins

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
)

// removedCredentialPaths finds saved credential paths the next manifest no
// longer protects. Keep them protected even if the next release drops an
// entry altogether: a later release could declare it public again.
func removedCredentialPaths(before, after *pluginv1.PluginManifest, key string) [][]string {
	protected := GlobalConfigSecretPaths(after, key)
	paths := GlobalConfigSecretPaths(before, key)
	return slices.DeleteFunc(paths, func(path []string) bool {
		return slices.ContainsFunc(protected, func(next []string) bool {
			return len(next) <= len(path) && slices.Equal(next, path[:len(next)])
		})
	})
}

func protectRetainedCredentials(key string, paths [][]string, before, after map[string]any) error {
	for _, path := range paths {
		saved := configAtPath(before, path)
		prospective := after[path[0]]
		if !populatedCredential(saved) || !populatedCredential(prospective) {
			continue
		}
		// Arrays and dynamic schema properties can mark a whole container
		// sensitive. Only addressable scalar paths let us prove replacement
		// removed the prior value while keeping the surrounding field.
		switch saved.(type) {
		case map[string]any, []any:
		default:
			if !containsConfigValue(prospective, saved) {
				continue
			}
		}
		return fmt.Errorf("config entry %q has a saved credential at %q that this release no longer protects",
			key, strings.Join(path, "."))
	}
	return nil
}

func unsafeReplacementConfigMessage(err error) string {
	return "Silo rejected this release because " + err.Error() + ". Clear that value or choose a compatible release."
}

func configAtPath(config map[string]any, path []string) any {
	var value any = config
	for _, segment := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[segment]
	}
	return value
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

func containsConfigValue(value, target any) bool {
	switch value := value.(type) {
	case map[string]any:
		for _, child := range value {
			if containsConfigValue(child, target) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(value, func(child any) bool { return containsConfigValue(child, target) })
	default:
		return sameConfigValue(value, target)
	}
	return false
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
	if len(oldManifestJSON) == 0 && installation.Signature.State != SignatureStateVerified {
		// Legacy packages may only have a local manifest. A verified
		// installation carries its previous manifest in the stored archive.
		return nil
	}

	previous, err := LoadManifestBytes(oldManifestJSON)
	if err != nil || previous.GetPluginId() != installation.PluginID || previous.GetVersion() != installation.Version {
		return packageError(err, "Silo could not verify how the saved settings for this plugin must be redacted.")
	}
	for _, schema := range previous.GetGlobalConfigSchema() {
		key := schema.GetKey()
		paths := removedCredentialPaths(previous, candidate, key)
		if len(paths) == 0 {
			continue
		}
		current, err := readGlobalConfigInTx(ctx, tx, s.cipher, installation.ID, key)
		if err != nil {
			return err
		}
		if err := protectRetainedCredentials(key, paths, current, current); err != nil {
			return &PackageError{Message: unsafeReplacementConfigMessage(err)}
		}
	}
	return nil
}
