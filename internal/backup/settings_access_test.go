package backup

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/markers"
)

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

// A restored password policy that is weaker than the live one is wider, so a
// restore keeps the live rules.
func TestPasswordPolicyRestoreNeverWeakens(t *testing.T) {
	for _, tt := range []struct {
		key, restored, live string
		wider               bool
	}{
		{config.AuthPasswordMinLengthSettingKey, "8", "12", true},
		{config.AuthPasswordMinLengthSettingKey, "", "12", true},
		{config.AuthPasswordMinLengthSettingKey, "16", "12", false},
		{config.AuthPasswordMinLengthSettingKey, "12", "", false},
		// Out of range applies as 8, so it is weaker than a live 12.
		{config.AuthPasswordMinLengthSettingKey, "99", "12", true},
		// A value the setting refuses never replaces a live one, even the default.
		{config.AuthPasswordMinLengthSettingKey, "99", "8", true},
		{config.AuthPasswordMinLengthSettingKey, "abc", "", true},
		{config.AuthPasswordRequireUppercaseSettingKey, "false", "true", true},
		{config.AuthPasswordRequireUppercaseSettingKey, "true", "", false},
		{config.AuthPasswordRequireSpecialSettingKey, "", "true", true},
		{config.AuthPasswordRequireSpecialSettingKey, "true", "false", false},
	} {
		access, wider := AccessSettingWider(tt.key, tt.restored, tt.live)
		if !access || wider != tt.wider {
			t.Errorf("AccessSettingWider(%s, %q, %q) = %v, %v; want true, %v", tt.key, tt.restored, tt.live, access, wider, tt.wider)
		}
	}
}
