package handlers

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

// TestRestorableSettingsHaveAWritePath: every setting a selective restore
// may write goes through its editor's write path: rate limits through the
// rate-limit configuration's, every other one through the admin settings
// batch, which must accept it (with its default value) rather than refuse
// it as managed elsewhere.
func TestRestorableSettingsHaveAWritePath(t *testing.T) {
	h := &AdminHandler{}
	defaults := config.EffectiveAdminSettings(nil)
	maps.Copy(defaults, ratelimit.DefaultSettings())
	keys := slices.Sorted(maps.Keys(defaults))
	checked := 0
	for _, key := range keys {
		if _, ok := backup.RestorableSetting(key); !ok {
			continue
		}
		checked++
		if strings.HasPrefix(key, "ratelimit.") {
			continue // RateLimitHandler.ApplyRateLimitSettingsInTx
		}
		if machineManagedSettingKeys[key] {
			t.Errorf("%s is restorable but managed internally", key)
			continue
		}
		if _, code, err := h.normalizeBatchSetting(t.Context(), key, defaults[key]); err != nil && strings.Contains(err.Error(), "managed by") {
			t.Errorf("%s is restorable, but the settings batch refuses it (%s): %v", key, code, err)
		}
	}
	if checked < 50 {
		t.Fatalf("only %d restorable settings checked", checked)
	}
}
