package api

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/google/uuid"
)

func TestInitialPlaybackConstructorRejectsMissingDependencies(t *testing.T) {
	policy := playback.RuntimeGrantPolicyV3{MaxDuration: 10 * time.Second, SafetyMargin: time.Second, RenewBefore: 3 * time.Second, PollInterval: time.Millisecond}
	flow, err := NewInitialPlaybackRuntime(t.Context(), nil, nil, nil, uuid.NewString(), policy, policy)
	if err == nil || flow != nil {
		t.Fatalf("flow=%v err=%v", flow, err)
	}
	flow, err = NewInitialPlaybackRuntime(t.Context(), nil, nil, nil, "not-an-installation", policy, policy)
	if err == nil || flow != nil {
		t.Fatalf("flow=%v err=%v", flow, err)
	}
}

type initialRouterShutdownControl struct {
	handlers.InitialPlaybackControlV3
	entered chan struct{}
	release chan struct{}
}

func (s initialRouterShutdownControl) ListInitialReconciliation(ctx context.Context, _ string, _ int) ([]playback.InitialActivationV3, error) {
	close(s.entered)
	<-ctx.Done()
	<-s.release
	return nil, ctx.Err()
}

type initialRouterShutdownSources struct {
	userstore.PlaybackSourceProvider
}
type initialRouterShutdownRecipes struct {
	handlers.InitialPlaybackRecipesV3
}
type initialRouterShutdownClock struct{}

func (initialRouterShutdownClock) Now() (time.Duration, error) { return time.Second, nil }

func TestInitialPlaybackRouterJoinsReconciliation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	control := initialRouterShutdownControl{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(control.release)
	flow := &handlers.InitialPlaybackFlowV3{InstallationID: uuid.NewString(), OwnerID: uuid.NewString(), Context: ctx, Control: control, Sources: initialRouterShutdownSources{}, Recipes: initialRouterShutdownRecipes{}, Clock: initialRouterShutdownClock{}, Policy: playback.RuntimeGrantPolicyV3{MaxDuration: 10 * time.Second, SafetyMargin: time.Second, RenewBefore: 3 * time.Second, PollInterval: time.Millisecond}, AcquireGrant: func(context.Context, string, playback.ExecutorNamespaceV3, playback.AttemptGrantPurposeV3) (*playback.RuntimeGrantV3, error) {
		return nil, errors.New("unused")
	}, ResolveRecipe: func(context.Context, string, playback.ExecutorNamespaceV3) (*playback.RecipeCard, error) {
		return nil, errors.New("unused")
	}}
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	var work []<-chan struct{}
	_ = NewRouter(Dependencies{Config: cfg, AppContext: ctx, SessionMgr: playback.NewSessionManager(0, 0), InitialPlayback: flow, RegisterShutdownWork: func(done <-chan struct{}) { work = append(work, done) }})
	select {
	case <-control.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("reconciliation not started")
	}
	if len(work) < 2 {
		t.Fatalf("missing runtime/reconciliation joins: %d", len(work))
	}
	cancel()
	// Registry order is initial runtime, then its reconciliation worker.
	select {
	case <-work[1]:
		t.Fatal("reconciliation join returned before worker exited")
	default:
	}
	control.release <- struct{}{}
	for _, done := range work {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("registered worker did not stop")
		}
	}
}
