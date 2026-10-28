package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Environment variables that set the catalog search connection. While one is
// set, its value wins on every start of an API server: it is validated the way
// an admin save is, stored so the admin UI shows the value in effect, and the
// admin UI cannot change it. A blank or unset variable leaves the stored value
// alone. The values are per process, so every API server sharing a database
// needs the same ones.
const (
	EnvSearchProvider    = "SILO_SEARCH_PROVIDER"
	EnvMeilisearchURL    = "SILO_MEILISEARCH_URL"
	EnvMeilisearchAPIKey = "SILO_MEILISEARCH_API_KEY"
)

var searchSettingEnvVars = [...]struct{ env, key string }{
	{EnvSearchProvider, SearchSettingProvider},
	{EnvMeilisearchURL, SearchSettingMeilisearchURL},
	{EnvMeilisearchAPIKey, SearchSettingMeilisearchAPIKey},
}

// SearchSettingsFromEnv returns the search settings the environment sets,
// keyed by setting name and normalized as an admin save normalizes them.
func SearchSettingsFromEnv(getenv func(string) string) (map[string]string, error) {
	values := map[string]string{}
	for _, v := range searchSettingEnvVars {
		raw := strings.TrimSpace(getenv(v.env))
		if raw == "" {
			continue
		}
		normalized, err := config.NormalizeAdminSetting(v.key, raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", v.env, err)
		}
		values[v.key] = normalized
	}
	return values, nil
}

// StoreSearchSettings writes the values that differ from stored, the settings
// just read from store, in one batch, so readers see all of them or none. It
// takes the encrypting store so the API key is never written in plaintext.
func StoreSearchSettings(ctx context.Context, store *EncryptedSettingsRepo, stored, values map[string]string) error {
	changed := map[string]string{}
	for key, value := range values {
		if stored[key] != value {
			changed[key] = value
		}
	}
	if len(changed) == 0 {
		return nil
	}
	return store.SetMany(ctx, changed)
}
