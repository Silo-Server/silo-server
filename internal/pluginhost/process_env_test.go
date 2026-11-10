package pluginhost

import (
	"runtime"
	"slices"
	"strconv"
	"testing"
)

func TestProcessEnvKeepsSecretsAndRuntimeTuningFromPlugins(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("GOMEMLIMIT", "3GiB")
	t.Setenv("TZ", "UTC")

	env := ProcessEnv()

	if !slices.Contains(env, "TZ=UTC") {
		t.Errorf("ProcessEnv() dropped allowlisted TZ: %q", env)
	}
	for _, kv := range env {
		if kv == "DATABASE_URL=postgres://example" || kv == "GOMEMLIMIT=3GiB" {
			t.Errorf("ProcessEnv() passed server-only %q to the plugin", kv)
		}
	}
	want := "GOMAXPROCS=" + strconv.Itoa(min(pluginMaxProcs, runtime.GOMAXPROCS(0)))
	if !slices.Contains(env, want) {
		t.Errorf("ProcessEnv() = %q, want it to contain %q", env, want)
	}
}
