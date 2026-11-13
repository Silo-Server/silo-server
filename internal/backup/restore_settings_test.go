package backup

import (
	"regexp"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/branding"
)

// TestEverySettingHasAPage: every registered setting a selective restore
// may write is on a settings page, and a page's server-specific class
// agrees with the setting's kind (SettingKindOf).
func TestEverySettingHasAPage(t *testing.T) {
	assets := branding.AssetSettingKeys()
	for _, key := range settingRegistry() {
		kind, _ := SettingKindOf(key)
		rule, ok := classifySetting(key)
		switch {
		case kind == SettingKindServerSpecific:
			if ok && rule.class != SettingServerSpecific {
				t.Errorf("%s is server-specific, but its page %q shows it as %s", key, rule.page, rule.class)
			}
		case slices.Contains(assets, key):
		case !ok:
			t.Errorf("setting %s (%s) is on no settings page", key, kind)
		case rule.class == SettingServerSpecific:
			t.Errorf("setting %s is %s, but its page %q shows it as server-specific", key, kind, rule.page)
		default:
			if _, known := settingsPageByID(rule.page); !known {
				t.Errorf("setting %s names unknown page %q", key, rule.page)
			}
		}
	}
}

// TestServerSpecificSettingsAreNeverOffered pins the spec's list of settings
// that describe this server rather than how it behaves.
func TestServerSpecificSettingsAreNeverOffered(t *testing.T) {
	for _, key := range []string{
		"server.identity_id", "server.public_url", "artwork.local_path", "s3.private_secret_key",
		"catalog.search.meilisearch.api_key", "userdb.backend", "notifications.push_relay_api_key", "auth.jwt_secret",
		"backup.keep", "storage.transition.target", "redis.url", "branding.mark_ref",
		"recommendations.embedding_lock", "recommendations.policy_marked_generation",
	} {
		if _, offered := RestorableSetting(key); offered {
			t.Errorf("%q is offered for restore", key)
		}
	}
	for key, want := range map[string]SettingClass{
		"email.smtp_password": SettingExternalService, "branding.server_name": SettingPortable,
		"catalog.search.meilisearch.timeout_ms": SettingPortable,
	} {
		rule, offered := RestorableSetting(key)
		if !offered || rule.class != want {
			t.Errorf("%q: offered %v class %q, want %q", key, offered, rule.class, want)
		}
	}
}

// TestAccessSettingsAreComparedOnRestore: an access setting (accessSettings)
// a selective restore offers is compared with the live value; every other
// one is server-specific and never written.
func TestAccessSettingsAreComparedOnRestore(t *testing.T) {
	for _, key := range AccessSettingKeys() {
		rule, ok := classifySetting(key)
		if !ok {
			t.Errorf("access setting %s is not classified", key)
			continue
		}
		if _, offered := RestorableSetting(key); !offered && rule.class != SettingServerSpecific {
			t.Errorf("access setting %s is neither offered nor server-specific: %+v", key, rule)
		}
	}
}

// TestTrustSettingsAreStricter: every setting named like a network-security
// or sign-in trust toggle (private destinations, trusted proxies, origins,
// TLS verification, sign-up and sign-in switches) is a stricter setting, so
// no restore loosens it.
func TestTrustSettingsAreStricter(t *testing.T) {
	looksLikeTrust := regexp.MustCompile(`allow_private|trusted|allowed_origins|cors|insecure|skip_verify|tls_verify|signup\.enabled|local_password_login|email_auto_match|self_service`)
	for _, key := range settingRegistry() {
		if kind, _ := SettingKindOf(key); looksLikeTrust.MatchString(key) && kind != SettingKindStricter {
			t.Errorf("%s looks like a trust setting and is %s; add it to accessSettings", key, kind)
		}
	}
}
