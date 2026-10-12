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

func TestStartupBootstrapAllowsOptInUnboundTranscodeOnly(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SECRET_KEY", "")
	t.Setenv("SILO_NODE_ENROLLMENT_TOKEN", strings.Repeat("t", 48))
	t.Setenv("SILO_NODE_BINDING_FILE", "/private/node-binding.json")
	t.Setenv("SILO_TRANSCODE_MAX_JOBS", "4")
	for _, mode := range []string{"integrated", "api", "proxy", "transcode"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("MODE", mode)
			bc, err := LoadStartupBootstrap("", true)
			if mode != "transcode" {
				if err == nil {
					t.Fatal("non-transcode worker started unbound")
				}
				return
			}
			if err != nil || bc.DatabaseURL != "" || len(bc.SecretKey) != 0 || bc.TranscodeMaxJobs != 4 || bc.EnrollmentToken == "" {
				t.Fatalf("unbound bootstrap: %+v %v", bc, err)
			}
			if _, err := LoadBootstrap(""); err == nil {
				t.Fatal("legacy/maintenance loader became unbound")
			}
			t.Setenv("SILO_TRANSCODE_MAX_JOBS", "-1")
			if _, err := LoadStartupBootstrap("", true); err == nil {
				t.Fatal("unbound node accepted negative provider cap")
			}
		})
	}
}
func TestStartupBootstrapRefusesUnprotectedOrPartialEnrollment(t *testing.T) {
	t.Setenv("MODE", "transcode")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SECRET_KEY", "")
	t.Setenv("SILO_NODE_ENROLLMENT_TOKEN", "")
	t.Setenv("SILO_NODE_BINDING_FILE", "")
	if _, err := LoadStartupBootstrap("", true); err == nil {
		t.Fatal("unprotected unbound node allowed")
	}
	t.Setenv("SILO_NODE_ENROLLMENT_TOKEN", strings.Repeat("t", 48))
	t.Setenv("DATABASE_URL", "postgres://old-customer.invalid/silo")
	if _, err := LoadStartupBootstrap("", true); err == nil {
		t.Fatal("partial customer bootstrap accepted")
	}
}
