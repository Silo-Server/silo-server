package backup

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/markers"
)

// TestAccessSettingWider: a restore keeps or writes an access setting only
// when its value grants no more than the live one.
func TestAccessSettingWider(t *testing.T) {
	for _, tc := range []struct {
		key, restored, live string
		wider               bool
	}{
		{config.AccessUnratedContentSettingKey, "allow", "hide", true},
		{config.AccessUnratedContentSettingKey, "allow", "", true}, // unset hides
		{config.AccessUnratedContentSettingKey, "hide", "allow", false},
		{config.AuthProviderRecheckOutagePolicySettingKey, config.AuthRecheckFailOpen, config.AuthRecheckFailClosed, true},
		{config.AuthProviderRecheckOutagePolicySettingKey, "", config.AuthRecheckFailClosed, true}, // the default fails open
		{settingLocalPasswordLogin, "true", "false", true},
		{settingLocalPasswordLogin, "false", "true", false},
		{"signup.enabled", "true", "", true}, // sign-up is off by default
		{"jellyfin_compat.enabled", "", "false", true},
		{config.AuthRefreshTokenExpirySettingKey, "90d", "30d", true},
		{config.AuthRefreshTokenExpirySettingKey, "7d", "30d", false},
		{config.AuthRefreshTokenExpirySettingKey, "forever", "30d", true}, // unreadable keeps the live value
		{settingSMTPSecurity, "none", "starttls", true},
		{settingSMTPSecurity, "tls", "none", false},
		{settingTrustedProxies, "10.0.0.0/8, 203.0.113.7", "10.0.0.0/8", true},
		{settingTrustedProxies, "10.1.0.0/16", "10.0.0.0/8", false},
		{"ratelimit.enabled", "false", "true", true},
		{settingReleaseEvents, "true", "false", true},
		{settingReleaseEvents, "false", "", false},
		{markers.SettingShareByDefault, "true", "", true},
		{markers.SettingMode, "both", "local", true},
		{"ratelimit.auth.login.requests_per_minute", "1000", "20", true},
		{"ratelimit.auth.login.requests_per_minute", "5", "", false},
		{"metadata_ai.on_view", "auto", "off", true},
		{"metadata_ai.on_view", "button", "", true}, // unset is off
		{"metadata_ai.on_view", "off", "button", false},
		{config.AIServiceTierSettingKey, "priority", "", true},
		{config.AIServiceTierSettingKey, "", "ultrafast", false},
		{"subtitle_ai.transcribe_quota_jobs", "0", "10", true}, // 0 is no limit
		{"subtitle_ai.transcribe_quota_jobs", "20", "10", true},
		{"subtitle_ai.transcribe_quota_jobs", "5", "0", false},
		{"subtitle_ai.transcribe_quota_period", "day", "month", true},
		{"subtitle_ai.transcribe_quota_period", "month", "", false},
	} {
		access, wider := AccessSettingWider(tc.key, tc.restored, tc.live)
		if !access || wider != tc.wider {
			t.Errorf("AccessSettingWider(%s, %q, %q) = %v, %v; want access, wider %v", tc.key, tc.restored, tc.live, access, wider, tc.wider)
		}
	}
	if access, _ := AccessSettingWider("server.log_level", "debug", "info"); access {
		t.Error("server.log_level is not an access setting")
	}
}

// TestAccessSettingsAreClassified: every access setting is one this server
// has, with a default, so an unset row compares as the server reads it.
func TestAccessSettingsAreClassified(t *testing.T) {
	for _, key := range AccessSettingKeys() {
		// Sharing markers is off unless set, and the standard AI service
		// tier is the empty value; their comparisons read an unset value as
		// the server does.
		if settingDefault(key) == "" && key != markers.SettingShareByDefault && key != config.AIServiceTierSettingKey {
			t.Errorf("access setting %s has no default", key)
		}
	}
}
