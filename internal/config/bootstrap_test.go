package config

import (
	"strings"
	"testing"
)

func TestBootstrapProviderTranscodeLimit(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/silo")
	t.Setenv("SECRET_KEY", strings.Repeat("x", 32))
	for _, tc := range []struct {
		value   string
		want    int
		invalid bool
	}{
		{"", 0, false}, {"0", 0, false}, {"4", 4, false},
		{"2147483647", 2147483647, false}, {"2147483648", 0, true},
		{"-1", 0, true}, {"four", 0, true}, {"999999999999999999999999999", 0, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("SILO_TRANSCODE_MAX_JOBS", tc.value)
			cfg, err := LoadBootstrap("")
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "SILO_TRANSCODE_MAX_JOBS") {
					t.Fatalf("invalid limit: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TranscodeMaxJobs != tc.want {
				t.Fatalf("limit %q was not loaded", tc.value)
			}
		})
	}
}

func TestLoadDatabaseURLNeedsNoSecretKey(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/silo")
	t.Setenv("SECRET_KEY", "")
	url, err := LoadDatabaseURL("")
	if err != nil || url != "postgres://example.invalid/silo" {
		t.Fatalf("LoadDatabaseURL = %q, %v", url, err)
	}
	// The server itself still refuses to start without the key.
	if _, err := LoadBootstrap(""); err == nil {
		t.Fatal("LoadBootstrap accepted a missing SECRET_KEY")
	}

	t.Setenv("DATABASE_URL", "")
	if _, err := LoadDatabaseURL(""); err == nil {
		t.Fatal("LoadDatabaseURL accepted a missing DATABASE_URL")
	}
}
