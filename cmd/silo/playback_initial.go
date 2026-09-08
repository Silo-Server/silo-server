package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// These bounded testing policies match the isolated playback harness. They are
// not production sizing or a claim that an arbitrary deployment meets the budget.
func initialPlaybackTestingPolicies() (playback.RuntimeGrantPolicyV3, playback.RuntimeGrantPolicyV3) {
	return playback.RuntimeGrantPolicyV3{MaxDuration: 30 * time.Second, SafetyMargin: time.Second, RenewBefore: 10 * time.Second, PollInterval: 10 * time.Millisecond},
		playback.RuntimeGrantPolicyV3{MaxDuration: time.Second, SafetyMargin: 100 * time.Millisecond, RenewBefore: 200 * time.Millisecond, PollInterval: 10 * time.Millisecond}
}

func configureInitialPlaybackStartup(bootstrap *config.BootstrapConfig, deps *api.Dependencies) error {
	if !bootstrap.InitialPlaybackEnabled {
		return nil
	}
	if bootstrap.Mode != "integrated" && bootstrap.Mode != "api" {
		return errors.New("integrated or api mode required")
	}
	if deps == nil || deps.AppContext == nil || deps.DB == nil || deps.RedisClient == nil || deps.Config == nil || deps.Config.Auth.JWTSecret == "" {
		return errors.New("application context, PostgreSQL, Redis, configuration and signing key required")
	}
	sources, ok := deps.UserStoreProvider.(userstore.PlaybackSourceProvider)
	if !ok {
		return errors.New("configured user store must support captured playback sources")
	}
	if deps.RegisterShutdownWork == nil {
		return errors.New("shutdown work registry required")
	}
	startup, cancel := context.WithTimeout(deps.AppContext, 10*time.Second)
	defer cancel()
	if err := deps.DB.Ping(startup); err != nil {
		return fmt.Errorf("PostgreSQL unavailable: %w", err)
	}
	if err := deps.RedisClient.Ping(startup).Err(); err != nil {
		return fmt.Errorf("Redis unavailable: %w", err)
	}
	installation, err := diagnostics.ServerInstanceID(startup, catalog.NewServerSettingsRepo(deps.DB))
	if err != nil {
		return fmt.Errorf("persisted installation identity: %w", err)
	}
	owner, grant := initialPlaybackTestingPolicies()
	flow, err := api.NewInitialPlaybackRuntime(startup, deps.DB, deps.RedisClient, sources, installation, owner, grant)
	if err != nil {
		return fmt.Errorf("construct runtime: %w", err)
	}
	// Construction does not launch workers. Bind their lifetime to the app,
	// after bounding identity validation with the startup deadline.
	flow.Context = deps.AppContext
	flow.AuxiliaryEnabled = bootstrap.InitialPlaybackAPIOrigin != ""
	flow.TimelineResolver = catalog.NewClientPlaybackManifestResolver(deps.DB)
	deps.InitialPlayback = flow
	return nil
}
