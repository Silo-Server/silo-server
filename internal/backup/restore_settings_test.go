package backup

import (
	"regexp"
	"testing"
)

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
