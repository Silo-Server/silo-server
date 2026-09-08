package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type initialSupportedProvider struct {
	userstore.UserStoreProvider
	userstore.PlaybackSourceProvider
}

type initialUnsupportedProvider struct{ userstore.UserStoreProvider }

func TestInitialPlaybackStartupDefaultOff(t *testing.T) {
	// OFF must not inspect, replace or initialize any existing dependency.
	deps := api.Dependencies{}
	before := deps
	if err := configureInitialPlaybackStartup(&config.BootstrapConfig{}, &deps); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, deps) {
		t.Fatal("OFF changed dependencies")
	}
	if err := configureInitialPlaybackStartup(&config.BootstrapConfig{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestInitialPlaybackStartupRefusesIncompleteAssembly(t *testing.T) {
	on := &config.BootstrapConfig{InitialPlaybackEnabled: true, Mode: "integrated"}
	if err := configureInitialPlaybackStartup(on, nil); err == nil {
		t.Fatal("missing dependencies accepted")
	}
	// These zero clients never connect: structural validation must fail first.
	deps := api.Dependencies{AppContext: t.Context(), DB: new(pgxpool.Pool), RedisClient: new(redis.Client), Config: &config.Config{Auth: config.AuthConfig{JWTSecret: "test"}}, UserStoreProvider: initialUnsupportedProvider{}}
	if err := configureInitialPlaybackStartup(on, &deps); err == nil || !strings.Contains(err.Error(), "captured playback sources") {
		t.Fatalf("unsupported provider: %v", err)
	}
	if deps.InitialPlayback != nil {
		t.Fatal("partial runtime published")
	}
}

func TestInitialPlaybackStartupTestingPolicies(t *testing.T) {
	owner, grant := initialPlaybackTestingPolicies()
	if err := owner.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := grant.Validate(); err != nil {
		t.Fatal(err)
	}
	if grant.MaxDuration >= owner.MaxDuration-owner.SafetyMargin {
		t.Fatal("executor grant outlives owner testing budget")
	}
}

func TestInitialPlaybackStartupRequiresShutdown(t *testing.T) {
	on := &config.BootstrapConfig{InitialPlaybackEnabled: true, Mode: "integrated"}
	deps := api.Dependencies{AppContext: t.Context(), DB: new(pgxpool.Pool), RedisClient: new(redis.Client), Config: &config.Config{Auth: config.AuthConfig{JWTSecret: "test"}}, UserStoreProvider: initialSupportedProvider{}}
	if err := configureInitialPlaybackStartup(on, &deps); err == nil || !strings.Contains(err.Error(), "shutdown work registry") {
		t.Fatalf("missing shutdown: %v", err)
	}
	deps.RedisClient = nil
	if err := configureInitialPlaybackStartup(on, &deps); err == nil || !strings.Contains(err.Error(), "Redis") {
		t.Fatalf("missing Redis: %v", err)
	}
	if deps.InitialPlayback != nil {
		t.Fatal("failed startup published a runtime")
	}
}
