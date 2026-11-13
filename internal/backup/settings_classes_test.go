package backup

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
)

// settingRegistry is every server setting the server's registries name: the
// admin defaults, the sensitive settings, the branding assets, the rate
// limits, and the route and access tables.
func settingRegistry() []string {
	keys := config.AdminSettingDefaultKeys()
	keys = append(keys, slices.Collect(maps.Keys(catalog.SensitiveSettingKeys))...)
	keys = append(keys, branding.AssetSettingKeys()...)
	keys = append(keys, slices.Collect(maps.Keys(ratelimit.DefaultSettings()))...)
	keys = append(keys, AccessSettingKeys()...)
	for _, r := range settingRoutes {
		keys = append(keys, r.keys()...)
	}
	// Settings the server writes with no admin default, by the constants
	// that name them where there is one.
	keys = append(keys, serveridentity.Key, settingJWTSecret, settingABSJWTSecret,
		catalog.SearchSettingProvider, catalog.SearchSettingMeilisearchURL, catalog.SearchSettingMeilisearchAPIKey,
		catalog.SearchSettingMeilisearchIndex, catalog.SearchSettingMeilisearchIndexTypes,
		config.AITextProviderSettingKey, config.AISpeechProviderSettingKey, config.EmbeddingProviderSettingKey,
		config.MediaImageSweepCheckpointKey, config.ChapterThumbnailOriginalsCleanupKey, config.DashboardHiddenAttentionSettingKey,
		mail.SettingSMTPHost, mail.SettingSMTPPassword,
		"playback.hw_device", "download.artifact_dir", "recommendations.embedding_lock",
		"recommendations.policy_marked_generation", "ui.admin_theme_vars", "defaults.card_overlays",
		// The legacy subtitle model the AI settings still read.
		"subtitle_ai.chat_model")
	slices.Sort(keys)
	return slices.Compact(keys)
}

// TestEverySettingIsClassified: every registered setting has a kind, and
// every listed key or prefix names a setting, so the classification can
// neither miss one nor keep one that is gone.
func TestEverySettingIsClassified(t *testing.T) {
	registry := settingRegistry()
	for _, key := range registry {
		if _, ok := SettingKindOf(key); !ok {
			t.Errorf("setting %s has no kind: add it to settings_classes.go", key)
		}
	}
	for _, entry := range slices.Concat(serverSpecificSettings, restoredSettings) {
		named := slices.ContainsFunc(registry, func(key string) bool {
			return key == entry || isSettingPrefix(entry) && strings.HasPrefix(key, entry)
		})
		if !named {
			t.Errorf("classification entry %s names no registered setting", entry)
		}
	}
	for _, r := range settingRoutes {
		for _, key := range []string{r.enabled, r.transport} {
			if kind, _ := SettingKindOf(key); key != "" && kind != SettingKindStricter {
				t.Errorf("route switch %s is %s, want stricter", key, kind)
			}
		}
	}
}

// TestEverySwitchSettingIsDecided: a setting whose default is a boolean, a
// switch, is a stricter setting, a server-specific one, or restored with its
// reason (restoredFlagSettings), so a new switch cannot reach a restore
// without someone deciding whether it grants or sends anything.
func TestEverySwitchSettingIsDecided(t *testing.T) {
	for _, key := range settingRegistry() {
		def := settingDefault(key)
		if def != "true" && def != "false" {
			continue
		}
		kind, _ := SettingKindOf(key)
		_, restored := restoredFlagSettings[key]
		switch {
		case kind == SettingKindStricter || kind == SettingKindServerSpecific:
			if restored {
				t.Errorf("switch %s is %s and in restoredFlagSettings", key, kind)
			}
		case !restored:
			t.Errorf("switch %s is restored as the backup has it with no reason: add it to accessSettings or restoredFlagSettings", key)
		}
	}
	for key := range restoredFlagSettings {
		if def := settingDefault(key); def != "true" && def != "false" {
			t.Errorf("restoredFlagSettings names %s, which is not a switch setting", key)
		}
	}
}

// TestEveryCredentialSettingIsDecided: every encrypted setting is either
// one a full restore takes from the live server (outboundBundles), or one
// of this server's own keys and storage, which a restore handles on its own
// (signing keys rotate, storage is checked against this deployment's, a
// storage transition is the live server's, Redis and the backup bucket are
// this server's), and
// every outbound setting is a classified one.
func TestEveryCredentialSettingIsDecided(t *testing.T) {
	own := func(key string) bool {
		return key == settingJWTSecret || key == settingABSJWTSecret || key == "storage.transition.target" ||
			key == "notifications.web_push.vapid_keypair" || strings.HasPrefix(key, "s3.") || strings.HasPrefix(key, "redis.") ||
			strings.HasPrefix(key, backupBucketSettingPrefix)
	}
	outbound := outboundKeys()
	for key := range catalog.SensitiveSettingKeys {
		if !own(key) && !slices.Contains(outbound, key) {
			t.Errorf("encrypted setting %s is neither carried from the live server (outboundBundles) nor this server's own", key)
		}
	}
	for _, key := range outbound {
		if _, ok := SettingKindOf(key); !ok {
			t.Errorf("outbound setting %s has no kind", key)
		}
	}
}

// TestOutboundBundlesCoverTheirConsumers: each unit a full restore takes
// from the live server holds every setting its consumer reads to reach and
// sign in to the service, so a restore never mixes the two servers' values,
// and every endpoint and secret route is in a unit.
func TestOutboundBundlesCoverTheirConsumers(t *testing.T) {
	unitOf := func(key string) int {
		for i, b := range outboundBundles {
			if slices.Contains(b.keys, key) {
				return i
			}
		}
		return -1
	}
	smtp := mail.SMTPSettingKeys()
	for _, key := range []string{mail.SettingSMTPHost, mail.SettingSMTPPort, mail.SettingSMTPUsername, mail.SettingSMTPPassword, mail.SettingFromAddress} {
		if !slices.Contains(smtp, key) {
			t.Fatalf("the mail sender's keys %v leave out %s", smtp, key)
		}
	}
	for consumer, keys := range map[string][]string{"mail": smtp, "AI": config.AIConnectionSettingKeys()} {
		unit := unitOf(keys[0])
		for _, key := range keys {
			if unitOf(key) != unit || unit < 0 {
				t.Errorf("%s setting %s is not in the unit of %s", consumer, key, keys[0])
			}
		}
	}
	for _, r := range settingRoutes {
		for _, key := range append([]string{r.endpoint}, r.secrets...) {
			if unitOf(key) < 0 {
				t.Errorf("route setting %s is in no outbound unit", key)
			}
		}
	}
}
