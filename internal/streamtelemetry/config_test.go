package streamtelemetry

import (
	"testing"
	"time"
)

func TestConfigFromEnvValidation(t *testing.T) {
	// Telemetry observes unless it is switched off, and leaves distributed mode
	// unsettled so wiring can derive it from Redis.
	t.Run("defaults", func(t *testing.T) {
		clearConfigEnv(t)
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || cfg.Distributed || cfg.DistributedExplicit || cfg.SweepInterval != time.Second || cfg.Retention != 5*time.Minute || cfg.MaxObservations != 50_000 ||
			cfg.Freshness != 5*time.Second || cfg.MembershipTTL != time.Minute || cfg.KeyPrefix != "silo:stelem" || cfg.FullResyncEvery != 60 || cfg.MaxPublishers != 256 || cfg.MaxMergedSessions != 50_000 || cfg.MaxMergedTransfers != 50_000 {
			t.Fatalf("defaults = %+v", cfg)
		}
	})
	// The kill switch has to work, and has to fail towards off: an operator who
	// mistypes it was reaching for "stop observing", so a value nobody can parse
	// stops observation rather than quietly leaving it running.
	for _, test := range []struct {
		name    string
		value   string
		enabled bool
	}{
		{"empty stays on", "", true},
		{"whitespace stays on", "   ", true},
		{"explicit false kills", "false", false},
		{"explicit off kills", "off", false},
		{"malformed value kills", "flase", false},
		{"explicit true stays on", "true", true},
	} {
		t.Run("enabled switch: "+test.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv(enabledEnv, test.value)
			if cfg := ConfigFromEnv("node"); cfg.Enabled != test.enabled {
				t.Fatalf("SILO_STREAM_TELEMETRY_ENABLED=%q gave Enabled=%v, want %v", test.value, cfg.Enabled, test.enabled)
			}
		})
	}
	// Wiring derives distributed mode from Redis only when the operator left the
	// variable alone, so "set but false" and "unset" must stay distinguishable.
	for _, test := range []struct {
		name        string
		value       string
		distributed bool
		explicit    bool
	}{
		{"unset leaves the mode to the caller", "", false, false},
		{"whitespace leaves the mode to the caller", "  ", false, false},
		{"explicit true pins it on", "true", true, true},
		{"explicit false pins it off", "false", false, true},
		{"malformed value pins it off", "flase", false, true},
	} {
		t.Run("distributed switch: "+test.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv(distributedEnv, test.value)
			cfg := ConfigFromEnv("node")
			if cfg.Distributed != test.distributed || cfg.DistributedExplicit != test.explicit {
				t.Fatalf("SILO_STREAM_TELEMETRY_DISTRIBUTED=%q gave %v/%v, want %v/%v", test.value, cfg.Distributed, cfg.DistributedExplicit, test.distributed, test.explicit)
			}
		})
	}
	// A rejected distributed configuration has to pin the mode off too, or the
	// caller's Redis derivation would re-enable exactly what was just refused.
	t.Run("rejected distributed config pins the mode off", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(freshnessEnv, "10s")
		t.Setenv(membershipTTLEnv, "10s")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || cfg.Distributed || !cfg.DistributedExplicit {
			t.Fatalf("config = %+v", cfg)
		}
	})
	t.Run("valid distributed overrides", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(sweepIntervalEnv, "2s")
		t.Setenv(freshnessEnv, "7s")
		t.Setenv(membershipTTLEnv, "20s")
		t.Setenv(keyPrefixEnv, "custom:telemetry")
		t.Setenv(fullResyncEveryEnv, "7")
		t.Setenv(maxPublishersEnv, "8")
		t.Setenv(maxMergedSessionsEnv, "9")
		t.Setenv(maxMergedTransfersEnv, "10")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || !cfg.Distributed || cfg.Freshness != 7*time.Second || cfg.MembershipTTL != 20*time.Second || cfg.KeyPrefix != "custom:telemetry" || cfg.FullResyncEvery != 7 || cfg.MaxPublishers != 8 || cfg.MaxMergedSessions != 9 || cfg.MaxMergedTransfers != 10 {
			t.Fatalf("distributed overrides = %+v", cfg)
		}
	})
	t.Run("valid enabled overrides", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(sweepIntervalEnv, "250ms")
		t.Setenv(retentionEnv, "6m")
		t.Setenv(maxSessionsEnv, "12")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || cfg.SweepInterval != 250*time.Millisecond || cfg.Retention != 6*time.Minute || cfg.MaxSessions != 12 {
			t.Fatalf("overrides = %+v", cfg)
		}
	})
	t.Run("invalid enabled disables", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(sweepIntervalEnv, "0s")
		if cfg := ConfigFromEnv("node"); cfg.Enabled {
			t.Fatalf("invalid config remained enabled: %+v", cfg)
		}
	})
	t.Run("invalid core disables the default-on process", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(maxTransfersEnv, "not-a-number")
		cfg := ConfigFromEnv("node")
		if cfg.Enabled || cfg.MaxTransfers != 10_000 {
			t.Fatalf("invalid config remained enabled: %+v", cfg)
		}
	})
	t.Run("invalid disabled is ignored", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "false")
		t.Setenv(maxTransfersEnv, "not-a-number")
		cfg := ConfigFromEnv("node")
		if cfg.Enabled || cfg.MaxTransfers != 10_000 {
			t.Fatalf("disabled invalid config = %+v", cfg)
		}
	})
	for name, variable := range map[string]string{
		"freshness": freshnessEnv, "membership ttl": membershipTTLEnv, "full resync": fullResyncEveryEnv,
		"max publishers": maxPublishersEnv, "max sessions": maxMergedSessionsEnv, "max transfers": maxMergedTransfersEnv,
	} {
		t.Run("invalid distributed "+name+" falls back local", func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv(enabledEnv, "true")
			t.Setenv(distributedEnv, "true")
			t.Setenv(variable, "invalid")
			cfg := ConfigFromEnv("node")
			if !cfg.Enabled || cfg.Distributed {
				t.Fatalf("invalid distributed config = %+v", cfg)
			}
		})
	}
	t.Run("invalid distributed while disabled warns and stays disabled", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "false")
		t.Setenv(distributedEnv, "false")
		t.Setenv(maxPublishersEnv, "0")
		cfg := ConfigFromEnv("node")
		if cfg.Enabled || cfg.Distributed {
			t.Fatalf("disabled config = %+v", cfg)
		}
	})
	t.Run("freshness below three sweeps", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(sweepIntervalEnv, "2s")
		t.Setenv(freshnessEnv, "5s")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || cfg.Distributed {
			t.Fatalf("config = %+v", cfg)
		}
	})
	// Setting ONE variable must not disable distributed mode by colliding with
	// the other knob's default: the unset knob moves instead.
	t.Run("sweep interval alone raises the unset freshness", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(sweepIntervalEnv, "2s") // default freshness 5s < 3*2s
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || !cfg.Distributed {
			t.Fatalf("one variable disabled distributed mode: %+v", cfg)
		}
		if cfg.SweepInterval != 2*time.Second || cfg.Freshness != 6*time.Second {
			t.Fatalf("sweep/freshness = %v/%v, want 2s/6s", cfg.SweepInterval, cfg.Freshness)
		}
	})
	t.Run("freshness alone lowers the unset sweep interval", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(freshnessEnv, "2s") // default sweep 1s is fine; 2s < 3s is not
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || !cfg.Distributed {
			t.Fatalf("one variable disabled distributed mode: %+v", cfg)
		}
		if cfg.Freshness != 2*time.Second || cfg.SweepInterval > cfg.Freshness/3 {
			t.Fatalf("sweep/freshness = %v/%v", cfg.SweepInterval, cfg.Freshness)
		}
	})
	t.Run("freshness alone raises the unset membership ttl", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(freshnessEnv, "60s") // default membership TTL is also 60s
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || !cfg.Distributed {
			t.Fatalf("one variable disabled distributed mode: %+v", cfg)
		}
		if cfg.Freshness != 60*time.Second || cfg.MembershipTTL <= cfg.Freshness {
			t.Fatalf("freshness/membership = %v/%v", cfg.Freshness, cfg.MembershipTTL)
		}
	})
	t.Run("membership ttl alone lowers the unset freshness", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(membershipTTLEnv, "4s") // default freshness 5s outlives it
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || !cfg.Distributed {
			t.Fatalf("one variable disabled distributed mode: %+v", cfg)
		}
		if cfg.MembershipTTL != 4*time.Second || cfg.MembershipTTL <= cfg.Freshness {
			t.Fatalf("freshness/membership = %v/%v", cfg.Freshness, cfg.MembershipTTL)
		}
	})
	t.Run("membership not above freshness", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(freshnessEnv, "10s")
		t.Setenv(membershipTTLEnv, "10s")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || cfg.Distributed {
			t.Fatalf("config = %+v", cfg)
		}
	})
	t.Run("whitespace prefix", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(keyPrefixEnv, "bad prefix")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || cfg.Distributed {
			t.Fatalf("config = %+v", cfg)
		}
	})
	t.Run("membership expiry overflow", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(distributedEnv, "true")
		t.Setenv(membershipTTLEnv, "2562047h47m16s")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || cfg.Distributed {
			t.Fatalf("config = %+v", cfg)
		}
	})
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{enabledEnv, sweepIntervalEnv, retentionEnv, maxSessionsEnv, maxTransfersEnv, maxObservationsEnv,
		distributedEnv, freshnessEnv, membershipTTLEnv, keyPrefixEnv, fullResyncEveryEnv, maxPublishersEnv, maxMergedSessionsEnv, maxMergedTransfersEnv,
		familiesEnv, viewTTLEnv} {
		t.Setenv(name, "")
	}
}

// The family gate is a narrowing/kill switch, not a staged rollout: every
// declared family is observed by default, and naming
// SILO_STREAM_TELEMETRY_FAMILIES narrows observation or drops one misbehaving
// family while the rest keep observing.
func TestConfigFamilyGate(t *testing.T) {
	t.Run("unset observes every declared family", func(t *testing.T) {
		clearConfigEnv(t)
		cfg := ConfigFromEnv("node")
		if len(cfg.Families) != 0 {
			t.Fatalf("families = %+v, want unset", cfg.Families)
		}
		for _, family := range AllFamilies {
			if !cfg.ObservesFamily(family) {
				t.Fatalf("%s not observed by default", family)
			}
		}
		if got := cfg.ObservedFamilies(); len(got) != 5 ||
			got[0] != "abs" || got[1] != "jellycompat" || got[2] != "native" || got[3] != "proxy" || got[4] != "transcode_node" {
			t.Fatalf("observed families = %v", got)
		}
	})
	t.Run("FAMILIES=proxy observes only proxy", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(familiesEnv, "proxy")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled {
			t.Fatalf("config = %+v", cfg)
		}
		if !cfg.ObservesFamily(FamilyProxy) {
			t.Fatal("proxy not observed")
		}
		for _, family := range []Family{FamilyNative, FamilyJellycompat, FamilyABS, FamilyTranscodeNode} {
			if cfg.ObservesFamily(family) {
				t.Fatalf("%s observed despite FAMILIES=proxy", family)
			}
		}
		if got := cfg.ObservedFamilies(); len(got) != 1 || got[0] != "proxy" {
			t.Fatalf("observed families = %v", got)
		}
	})
	t.Run("explicit list narrows and widens", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(familiesEnv, " Native , jellycompat ,, ABS ")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled {
			t.Fatalf("config = %+v", cfg)
		}
		for _, family := range []Family{FamilyNative, FamilyJellycompat, FamilyABS} {
			if !cfg.ObservesFamily(family) {
				t.Fatalf("%s not observed", family)
			}
		}
		for _, family := range []Family{FamilyProxy, FamilyTranscodeNode} {
			if cfg.ObservesFamily(family) {
				t.Fatalf("%s observed despite an explicit list omitting it", family)
			}
		}
		if got := cfg.ObservedFamilies(); len(got) != 3 || got[0] != "abs" || got[1] != "jellycompat" || got[2] != "native" {
			t.Fatalf("observed families = %v", got)
		}
	})
	t.Run("unknown family disables telemetry", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(familiesEnv, "native,not_a_family")
		cfg := ConfigFromEnv("node")
		if cfg.Enabled {
			t.Fatal("a typo in the family list must disable telemetry rather than silently observe nothing")
		}
	})
	t.Run("only whitespace falls back to observing every family", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv(enabledEnv, "true")
		t.Setenv(familiesEnv, "  ,  ")
		cfg := ConfigFromEnv("node")
		if !cfg.Enabled || !cfg.ObservesFamily(FamilyNative) || !cfg.ObservesFamily(FamilyABS) {
			t.Fatalf("config = %+v", cfg)
		}
	})
}
