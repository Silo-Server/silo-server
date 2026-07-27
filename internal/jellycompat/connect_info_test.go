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

func TestConnectInfoForConfigSettingsOverrideConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.JellyfinCompat.Enabled = true
	cfg.JellyfinCompat.PublicURL = "http://127.0.0.1:8096"
	cfg.JellyfinCompat.ServerName = "Silo"

	info := ConnectInfoForConfig(cfg, map[string]string{
		"jellyfin_compat.enabled":     "false",
		"jellyfin_compat.public_url":  "https://compat.example.test",
		"jellyfin_compat.server_name": "Example Household",
	})

	if info.Enabled {
		t.Fatal("Enabled = true, want the stored setting to disable it")
	}
	if info.PublicURL != "https://compat.example.test" {
		t.Fatalf("PublicURL = %q, want the stored URL", info.PublicURL)
	}
	if info.ServerName != "Example Household" {
		t.Fatalf("ServerName = %q, want the stored name", info.ServerName)
	}
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

	if !info.Enabled {
		t.Fatal("Enabled = false, want the stored setting to enable it")
	}
	if info.PublicURL != "https://compat.example.test" {
		t.Fatalf("PublicURL = %q, want the stored URL", info.PublicURL)
	}
}

// The status endpoint and the connect-info endpoint must agree on whether the
// compat API is on; they share compatEnabled to guarantee it.
func TestConnectInfoEnabledMatchesWebComponentStatus(t *testing.T) {
	cfg := &config.Config{}
	cfg.JellyfinCompat.Enabled = false

	for _, raw := range []string{"true", "1", "yes", "TRUE"} {
		settings := map[string]string{"jellyfin_compat.enabled": raw}
		if got := ConnectInfoForConfig(cfg, settings).Enabled; !got {
			t.Fatalf("ConnectInfo enabled = false for %q, want true", raw)
		}
		if got := WebComponentStatusForConfig(cfg, settings).Enabled; !got {
			t.Fatalf("WebComponentStatus enabled = false for %q, want true", raw)
		}
	}
}
