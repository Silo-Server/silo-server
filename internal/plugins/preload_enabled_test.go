package plugins

import (
	"context"
	"errors"
	"testing"
)

// One enabled plugin that no longer loads must not keep the server from
// starting: a fresh node loads every plugin from its stored archive.
func TestPreloadEnabledContinuesPastPluginThatFailsToLoad(t *testing.T) {
	store := newFakeServiceInstallationStore(&Installation{
		ID:          1,
		PluginID:    "test.broken",
		Kind:        KindPlugin,
		Enabled:     true,
		InstallPath: t.TempDir() + "/missing/plugin",
	})
	svc := &Service{installations: store}

	if err := svc.PreloadEnabled(context.Background()); err != nil {
		t.Fatalf("PreloadEnabled = %v, want nil with the broken plugin skipped", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.PreloadEnabled(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("PreloadEnabled(canceled) = %v, want context.Canceled", err)
	}
}
