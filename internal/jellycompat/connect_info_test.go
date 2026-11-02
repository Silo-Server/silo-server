package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// jellyfin_compat.enabled is restart-required, so the stored value describes
// intent, not the running listener. Reporting the override would promise
// credentials for a listener that does not exist yet.
func TestConnectInfoForConfigEnabledTracksRunningListener(t *testing.T) {
	t.Run("stored disable does not hide a running listener", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.JellyfinCompat.Enabled = true

		info := ConnectInfoForConfig(cfg, map[string]string{
			"jellyfin_compat.enabled": "false",
		})

		if !info.Enabled {
			t.Fatal("Enabled = false, want the running listener's state")
		}
		if !info.PendingRestart {
			t.Fatal("PendingRestart = false, want the pending change surfaced")
		}
	})

	t.Run("stored enable does not promise an absent listener", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.JellyfinCompat.Enabled = false

		info := ConnectInfoForConfig(cfg, map[string]string{
			"jellyfin_compat.enabled": "true",
		})

		if info.Enabled {
			t.Fatal("Enabled = true, but no listener is running until restart")
		}
		if !info.PendingRestart {
			t.Fatal("PendingRestart = false, want the pending change surfaced")
		}
	})

	t.Run("agreement reports no pending restart", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.JellyfinCompat.Enabled = true

		info := ConnectInfoForConfig(cfg, map[string]string{
			"jellyfin_compat.enabled": "true",
		})

		if !info.Enabled || info.PendingRestart {
			t.Fatalf("got Enabled=%v PendingRestart=%v, want true/false",
				info.Enabled, info.PendingRestart)
		}
	})
}
