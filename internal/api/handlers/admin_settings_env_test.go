package handlers

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
)

func readEnv(t *testing.T, env map[string]string) config.EnvSettings {
	t.Helper()
	settings, err := config.ReadEnvSettings(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestCheckEnvironmentSettings(t *testing.T) {
	checked, err := CheckEnvironmentSettings(nil, readEnv(t, map[string]string{"SILO_TRUSTED_PROXIES": " 10.0.0.0/8 ,203.0.113.7/32 "}), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := checked.All()["clientip.trusted_proxies"]; got != "10.0.0.0/8, 203.0.113.7/32" {
		t.Errorf("trusted proxies = %q, want the list an admin save stores", got)
	}

	for name, env := range map[string]map[string]string{
		"per-key":     {"SILO_TRUSTED_PROXIES": "not-a-cidr"},
		"cross-field": {"SILO_ARTWORK_STORAGE": "s3"},
	} {
		_, err := CheckEnvironmentSettings(nil, readEnv(t, env), false)
		for variable := range env {
			if err == nil || !strings.Contains(err.Error(), variable) {
				t.Errorf("%s: error = %v, want one naming %s", name, err, variable)
			}
		}
	}
}

// Once artwork is written, its location moves only through a managed
// transition, from the environment as from an admin save.
func TestCheckEnvironmentSettingsKeepsRecordedStorage(t *testing.T) {
	root := t.TempDir()
	stored := map[string]string{
		"artwork.storage_backend":    "local",
		"artwork.local_path":         root,
		blobstore.IdentitySettingKey: "local|" + root,
	}
	_, err := CheckEnvironmentSettings(stored, readEnv(t, map[string]string{"SILO_ARTWORK_PATH": t.TempDir()}), false)
	if err == nil || !strings.Contains(err.Error(), "SILO_ARTWORK_PATH") {
		t.Errorf("moving recorded storage: error = %v, want a refusal naming SILO_ARTWORK_PATH", err)
	}
	if _, err := CheckEnvironmentSettings(stored, readEnv(t, map[string]string{"SILO_ARTWORK_PATH": root, "SILO_S3_PUBLIC_REGION": "eu-west-1"}), false); err != nil {
		t.Errorf("recorded location with other storage settings: %v", err)
	}
}

// Backups, the Discord application, MDBList, the Meilisearch index, the
// web media origin, prepared-download budget and realtime monitoring can be
// set from the environment, with an admin save's checks.
func TestCheckEnvironmentSettingsDeploymentSettings(t *testing.T) {
	checked, err := CheckEnvironmentSettings(nil, readEnv(t, map[string]string{
		"SILO_WEB_MEDIA_URL":                     "https://media.example.com/",
		"SILO_MEILISEARCH_INDEX":                 "silo-home",
		"SILO_MDBLIST_API_KEY":                   "mdblist-key",
		"SILO_DISCORD_CLIENT_ID":                 "123456789012345678",
		"SILO_DISCORD_CLIENT_SECRET":             "discord-secret",
		"SILO_DISCORD_BOT_TOKEN":                 "discord-token",
		"SILO_DOWNLOAD_ARTIFACT_MAX_BYTES":       "10737418240",
		"SILO_SCANNER_REALTIME_MONITORING":       "false",
		"SILO_BACKUP_ENABLED":                    "false",
		"SILO_BACKUP_TIME":                       "04:30",
		"SILO_BACKUP_KEEP":                       "14",
		"SILO_BACKUP_INCLUDE_SUBTITLES":          "false",
		"SILO_BACKUP_INCLUDE_INTRO_FINGERPRINTS": "true",
	}), false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"server.web_media_url":              "https://media.example.com",
		"catalog.search.meilisearch.index":  "silo-home",
		"mdblist.api_key":                   "mdblist-key",
		"discord.client_id":                 "123456789012345678",
		"discord.client_secret":             "discord-secret",
		"discord.bot_token":                 "discord-token",
		"download.artifact_max_bytes":       "10737418240",
		"scanner.realtime_monitoring":       "false",
		"backup.enabled":                    "false",
		"backup.time":                       "04:30",
		"backup.keep":                       "14",
		"backup.include_subtitles":          "false",
		"backup.include_intro_fingerprints": "true",
	}
	got := checked.Values(config.EnvScopeStored)
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}

	// An invalid value stops startup with the variable's name.
	env := map[string]string{"SILO_BACKUP_TIME": "25:00"}
	if _, err := config.ReadEnvSettings(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "SILO_BACKUP_TIME") {
		t.Errorf("SILO_BACKUP_TIME=25:00: error = %v, want a refusal naming it", err)
	}
}
