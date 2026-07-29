package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestConnectInfoForConfigUsesBootstrapConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.JellyfinCompat.Enabled = true
	cfg.JellyfinCompat.PublicURL = "http://127.0.0.1:8096"
	cfg.JellyfinCompat.ServerName = "Silo"

	info := ConnectInfoForConfig(cfg, nil)

	if !info.Enabled {
		t.Fatal("Enabled = false, want true from bootstrap config")
	}
	if info.PublicURL != "http://127.0.0.1:8096" {
		t.Fatalf("PublicURL = %q, want the configured URL", info.PublicURL)
	}
	if info.ServerName != "Silo" {
		t.Fatalf("ServerName = %q, want Silo", info.ServerName)
	}
}

// The address fields apply without a restart, so a stored override wins.
func TestConnectInfoForConfigAddressSettingsOverrideConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.JellyfinCompat.Enabled = true
	cfg.JellyfinCompat.PublicURL = "http://127.0.0.1:8096"
	cfg.JellyfinCompat.ServerName = "Silo"

	info := ConnectInfoForConfig(cfg, map[string]string{
		"jellyfin_compat.public_url":  "https://compat.example.test",
		"jellyfin_compat.server_name": "Example Household",
	})

	if info.PublicURL != "https://compat.example.test" {
		t.Fatalf("PublicURL = %q, want the stored URL", info.PublicURL)
	}
	if info.ServerName != "Example Household" {
		t.Fatalf("ServerName = %q, want the stored name", info.ServerName)
	}
	if info.PendingRestart {
		t.Fatal("PendingRestart = true, want false when only address settings changed")
	}
}

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

// A blank stored value must not erase a configured one — stringSetting treats
// empty as "unset", and the card would otherwise render an empty server field.
func TestConnectInfoForConfigIgnoresBlankSettings(t *testing.T) {
	cfg := &config.Config{}
	cfg.JellyfinCompat.PublicURL = "http://127.0.0.1:8096"

	info := ConnectInfoForConfig(cfg, map[string]string{
		"jellyfin_compat.public_url": "   ",
	})

	if info.PublicURL != "http://127.0.0.1:8096" {
		t.Fatalf("PublicURL = %q, want the configured URL retained", info.PublicURL)
	}
}

func TestConnectInfoForConfigNilConfig(t *testing.T) {
	info := ConnectInfoForConfig(nil, map[string]string{
		"jellyfin_compat.enabled":    "true",
		"jellyfin_compat.public_url": "https://compat.example.test",
	})

	// No config means no running listener to report.
	if info.Enabled {
		t.Fatal("Enabled = true, want false without a bootstrap config")
	}
	if info.PublicURL != "https://compat.example.test" {
		t.Fatalf("PublicURL = %q, want the stored URL", info.PublicURL)
	}
}

// The admin status endpoint reports configured intent while connect-info
// reports the running listener, so a pending enable is expected to make them
// disagree — with connect-info flagging the restart rather than going quiet.
func TestConnectInfoDivergesFromWebComponentStatusUntilRestart(t *testing.T) {
	cfg := &config.Config{}
	cfg.JellyfinCompat.Enabled = false

	for _, raw := range []string{"true", "1", "yes", "TRUE"} {
		settings := map[string]string{"jellyfin_compat.enabled": raw}

		info := ConnectInfoForConfig(cfg, settings)
		if info.Enabled {
			t.Fatalf("ConnectInfo enabled = true for %q, want the not-yet-running listener", raw)
		}
		if !info.PendingRestart {
			t.Fatalf("ConnectInfo pending restart = false for %q, want true", raw)
		}
		if got := WebComponentStatusForConfig(cfg, settings).Enabled; !got {
			t.Fatalf("WebComponentStatus enabled = false for %q, want configured intent", raw)
		}
	}
}
