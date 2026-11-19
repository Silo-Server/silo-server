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
