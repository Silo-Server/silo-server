package handlers

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
)

// CheckEnvironmentSettings runs the checks an admin save of env would run
// against stored, the settings read from the database, and returns env with
// each value in the form an admin save stores. Every error names the
// variable it is about, because it stops startup.
//
// Covered: per-key normalization, the artwork root check, the recorded
// storage location guard (storage moves only through a managed transition),
// and the cross-field validation over the settings the process will run on.
func CheckEnvironmentSettings(stored map[string]string, env config.EnvSettings, redisBootstrapAvailable bool) (config.EnvSettings, error) {
	return CheckDeploymentSettings(stored, env, nil, "", redisBootstrapAvailable)
}

// CheckDeploymentSettings is CheckEnvironmentSettings with the machine-managed
// settings the deployment document writes, such as the AI provider registry,
// checked together with the environment's values: a recommendations
// variable can name a provider the document declares. declaredBy names the
// document's variable in errors.
func CheckDeploymentSettings(stored map[string]string, env config.EnvSettings, declared map[string]string, declaredBy string, redisBootstrapAvailable bool) (config.EnvSettings, error) {
	checked := make(config.EnvSettings, 0, len(env))
	normalized := make(map[string]string, len(env))
	for _, setting := range env {
		// None of the table's keys reach the branches of normalizeBatchSetting
		// that read handler state, so a zero handler normalizes them.
		value, _, err := (&AdminHandler{}).normalizeBatchSetting(context.Background(), setting.Key, setting.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", setting.Var, err)
		}
		if setting.Key == artworkLocalPathKey {
			if err := blobstore.CheckLocalRoot(value); err != nil {
				return nil, fmt.Errorf("invalid %s: %s must be a folder Silo can open, not a file, a symbolic link, or a path inside a file", setting.Var, artworkLocalPathKey)
			}
		}
		setting.Value = value
		checked = append(checked, setting)
		normalized[setting.Key] = value
	}

	// A registry the document leaves unchanged isn't a change, so its binding
	// group isn't checked again on every start.
	declaredChanged := false
	for key, value := range declared {
		if stored[key] != value {
			normalized[key] = value
			declaredChanged = true
		}
	}
	prospective := make(map[string]string, len(stored)+len(normalized))
	maps.Copy(prospective, stored)
	maps.Copy(prospective, normalized)

	if artworkStorageLocked(stored) {
		if err := rejectArtworkIdentityChange(stored[blobstore.IdentitySettingKey], stored[blobstore.OperationalIdentitySettingKey],
			config.EffectiveAdminSettings(stored), config.EffectiveAdminSettings(prospective)); err != nil {
			return nil, fmt.Errorf("%s would move storage that already holds data; set them to the recorded location or unset them, and move storage with a managed storage transition in Admin settings",
				strings.Join(checked.VarsFor(storageLocationKeys), ", "))
		}
	}

	skipBindingsOutsideDeclaration(prospective, normalized, declaredBy)

	if err := validateProspectiveAdminSettings(adminSettingsValidationSnapshot(prospective, normalized), redisBootstrapAvailable); err != nil {
		vars := checked.VarsFor(nil)
		if declaredChanged {
			vars = append(vars, declaredBy)
		}
		return nil, fmt.Errorf("the settings from the environment are invalid (%s): %w", strings.Join(vars, ", "), err)
	}
	return checked, nil
}

// skipBindingsOutsideDeclaration leaves out of the startup check each stored
// provider binding the deployment can't have broken: one the environment
// doesn't set that names a provider the document doesn't declare (with no
// document, every stored binding). Declaring never removes a provider, so
// such a binding is as valid as it was, and one already naming a missing
// provider (as a restore into another deployment can leave) stays the
// admin's to fix instead of stopping startup.
func skipBindingsOutsideDeclaration(prospective, fromEnvironment map[string]string, declaredBy string) {
	registry, err := config.ParseAIProviderRegistry(prospective[config.AIProvidersSettingKey])
	if err != nil {
		return // The registry check reports it.
	}
	for _, key := range config.AIProviderBindingKeys() {
		if _, set := fromEnvironment[key]; set {
			continue
		}
		provider, found := registry.Find(strings.TrimSpace(prospective[key]))
		if !found || declaredBy == "" || provider.ManagedBy != declaredBy {
			prospective[key] = ""
		}
	}
}

// storageLocationKeys are the settings rejectArtworkIdentityChange compares.
var storageLocationKeys = map[string]bool{
	artworkStorageBackendKey: true,
	artworkLocalPathKey:      true,
	s3PublicEndpointKey:      true,
	s3PublicBucketKey:        true,
	s3PublicKeyPrefixKey:     true,
	s3PrivateEndpointKey:     true,
	s3PrivateBucketKey:       true,
	s3PrivateKeyPrefixKey:    true,
}
