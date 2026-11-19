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

	if err := validateProspectiveAdminSettings(adminSettingsValidationSnapshot(prospective, normalized), redisBootstrapAvailable); err != nil {
		return nil, fmt.Errorf("the settings from the environment are invalid (%s): %w", strings.Join(checked.VarsFor(nil), ", "), err)
	}
	return checked, nil
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
