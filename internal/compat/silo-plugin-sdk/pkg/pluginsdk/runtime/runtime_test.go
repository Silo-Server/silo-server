package runtime_test

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	runtime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

func TestRuntimeBootstrapCompiles(t *testing.T) {
	t.Helper()

	_ = runtime.ProtocolVersion
	_ = runtime.HandshakeConfig()
	_ = runtime.ServeConfig{}
	_ = &pluginv1.PluginManifest{}
}

func TestHost_NilWhenBrokerUnset(t *testing.T) {
	// Without a prior GRPCServer call having stashed the broker, runtime.Host()
	// returns nil. The full broker dial path is exercised by the cross-repo
	// integration test in Phase 7.
	if got := runtime.Host(); got != nil {
		t.Errorf("runtime.Host() = %v, want nil before broker is bound", got)
	}
}

func TestSetHostBrokerID_DoesNotPanic(t *testing.T) {
	// Compile-time + smoke test that SetHostBrokerID is callable. Real broker
	// behavior is integration-tested.
	runtime.SetHostBrokerID(0)
}
