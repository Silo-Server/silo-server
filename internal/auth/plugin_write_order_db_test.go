package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// A plugin write that guards sign-in (plugins.signInGuard) takes its
// installation row and then the settings lock. A key sign-in by a key bound
// to that installation's provider, and a switch-off with its sweep, take the
// installation before the settings lock too, so neither forms a cycle with
// it. Staged: a holder keeps the settings lock; the key path (or the
// switch-off) asks for it first, the plugin write takes its installation row
// and asks second; then the holder lets go. With the key path's or the
// sweep's installation lock after the settings lock, the key path got the
// settings lock, waited on the installation the plugin write held, and the
// plugin write waited on the settings lock: Postgres aborted one.
func TestPluginWriteAgainstKeyPathsDB(t *testing.T) {
	env := newKeyEnv(t, "pluginorder")
	t.Cleanup(saveSettings(t, env.pool, config.AuthAppKeySignInSettingKey))
	bindings := plugins.NewRuntimeConfigStore(env.pool)
	pluginWrite := func(ctx context.Context) error {
		return bindings.UpsertAuthBinding(ctx, plugins.AuthBinding{InstallationID: env.installationID, CapabilityID: "oidc", Enabled: false})
	}
	for _, tc := range []struct {
		name  string
		party func(t *testing.T) func(context.Context) error
	}{
		{"a provider-bound key's sign-in", func(t *testing.T) func(context.Context) error {
			device, _ := env.enrollFromProvider(t)
			req := env.request(t, message{purpose: appkeys.PurposeSignIn, signer: device})
			return func(ctx context.Context) error {
				_, err := env.signIn.SignIn(ctx, req)
				return err
			}
		}},
		{"a switch-off and its sweep", func(t *testing.T) func(context.Context) error {
			return func(ctx context.Context) error { return switchOff(ctx, env.pool) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.setSetting(t, config.AuthAppKeySignInSettingKey, "true")
			party := tc.party(t)
			ctx := t.Context()
			hold, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { rollbackWithin(hold) })
			if err := lockServerSettings(ctx, hold); err != nil {
				t.Fatal(err)
			}
			deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			results := [2]chan error{make(chan error, 1), make(chan error, 1)}
			returned := make(chan error, 2)
			ahead := []int32{backendPID(t, hold)}
			var queued []int32
			for i, run := range []func(context.Context) error{party, pluginWrite} {
				go func() {
					err := run(deadline)
					results[i] <- err
					returned <- fmt.Errorf("party %d: %w", i+1, err)
				}()
				pid := waitForPartyBehind(t, env.pool, ahead, queued, returned)
				queued = append(queued, pid)
				ahead = append(ahead, pid)
			}
			if err := hold.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			for i, name := range []string{tc.name, "the plugin write"} {
				if err := <-results[i]; err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}
		})
	}
}
