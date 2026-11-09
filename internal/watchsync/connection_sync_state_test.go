package watchsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// watchedImporterDuring runs during while the import reads the provider,
// standing in for work elsewhere on the same connection mid-sync.
type watchedImporterDuring struct {
	watchedImporterStub
	during  func(context.Context)
	cursors map[string]string
}

func (p watchedImporterDuring) FetchWatchedBatch(ctx context.Context, _ ServerConfig, _ Connection) (WatchedImportBatch, error) {
	p.during(ctx)
	return WatchedImportBatch{UpdatedCursors: p.cursors}, nil
}

func TestServiceSyncFlowKeepsConnectionChangesMadeDuringIt(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	saved := Connection{
		ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1",
		ProviderAccountID: "account", AccessToken: testOldAccessToken, RefreshToken: testOldRefreshToken,
		TokenExpiresAt: new(now.Add(time.Hour)), ImportWatchedEnabled: true, ScrobbleEnabled: true,
		SyncCursors: map[string]string{"trakt.watched": "cursor-1"},
	}
	key := connectionKey(saved.Provider, saved.UserID, saved.ProfileID)

	for _, change := range []string{"token refresh", "disconnect", "rate limit", "other cursors"} {
		t.Run(change, func(t *testing.T) {
			repo := newServiceFakeRepo()
			repo.connections[key] = cloneConnectionForTest(saved)
			service := NewService(repo, NewRegistry()).
				WithMatcher(unmatchedMatcherStub{}).
				WithWatchState(noopWatchState{})
			service.now = func() time.Time { return now }
			importer := watchedImporterDuring{
				cursors: map[string]string{"trakt.watched": "cursor-2"},
				during: func(ctx context.Context) {
					switch change {
					case "token refresh":
						refreshed := connectionWithTokens(saved, TokenSet{
							AccessToken: "new-access", RefreshToken: "new-refresh", TokenExpiresAt: new(now.Add(2 * time.Hour)),
						})
						if _, err := repo.UpdateConnectionTokens(ctx, saved, refreshed); err != nil {
							t.Fatal(err)
						}
					case "disconnect":
						if err := repo.DeleteConnection(ctx, saved.Provider, saved.UserID, saved.ProfileID); err != nil {
							t.Fatal(err)
						}
					case "rate limit":
						if _, err := repo.DeferConnectionsForAccount(ctx, saved.Provider, saved.ProviderAccountID, now.Add(time.Hour), "rate limited"); err != nil {
							t.Fatal(err)
						}
					case "other cursors":
						current := repo.connections[key]
						current.SyncCursors = map[string]string{"trakt.watched": "cursor-1", "trakt.ratings": "ratings-2"}
						current.ScrobbleEnabled = false
						repo.connections[key] = current
					}
				},
			}

			// The flow holds the copy loaded before the change, as a sync run does.
			if _, err := service.ImportWatched(t.Context(), cloneConnectionForTest(saved), ServerConfig{}, importer); err != nil {
				t.Fatalf("ImportWatched: %v", err)
			}

			current, exists := repo.connections[key]
			if change == "disconnect" {
				if exists {
					t.Fatalf("disconnected connection restored: %+v", current)
				}
				return
			}
			if !exists || current.LastInboundSyncAt == nil || !current.LastInboundSyncAt.Equal(now) ||
				current.SyncCursors["trakt.watched"] != "cursor-2" {
				t.Fatalf("flow bookkeeping not saved: %+v", current)
			}
			switch change {
			case "token refresh":
				if current.AccessToken != "new-access" || current.RefreshToken != "new-refresh" {
					t.Fatalf("refreshed tokens overwritten: access=%q refresh=%q", current.AccessToken, current.RefreshToken)
				}
			case "rate limit":
				if current.RateLimitedUntil == nil || !current.RateLimitedUntil.Equal(now.Add(time.Hour)) ||
					current.LastError != "rate limited" {
					t.Fatalf("deferral set during the flow lost: until=%v error=%q", current.RateLimitedUntil, current.LastError)
				}
			case "other cursors":
				if current.SyncCursors["trakt.ratings"] != "ratings-2" || current.ScrobbleEnabled {
					t.Fatalf("changes made during the flow lost: %+v", current)
				}
			}
		})
	}
}

// watchedAndProgressImporter deletes the connection while watched history
// is read, then returns readErr, and records whether progress was read
// afterwards.
type watchedAndProgressImporter struct {
	watchedImporterStub
	repo         *serviceFakeRepo
	readErr      error
	progressRead *bool
}

func (p watchedAndProgressImporter) Capabilities() Capabilities {
	return Capabilities{ImportWatched: true, ImportProgress: true}
}

func (p watchedAndProgressImporter) FetchWatchedBatch(ctx context.Context, _ ServerConfig, conn Connection) (WatchedImportBatch, error) {
	if err := p.repo.DeleteConnection(ctx, conn.Provider, conn.UserID, conn.ProfileID); err != nil {
		return WatchedImportBatch{}, err
	}
	return WatchedImportBatch{}, p.readErr
}

func (p watchedAndProgressImporter) FetchProgress(context.Context, ServerConfig, Connection) ([]RemoteProgress, error) {
	*p.progressRead = true
	return nil, nil
}

func TestServiceSyncRunStopsWhenTheConnectionIsDisconnected(t *testing.T) {
	for name, readErr := range map[string]error{
		"flow succeeds": nil,
		// A plugin call that finds the connection gone fails the flow.
		"flow fails on the missing connection": fmt.Errorf("plugin call: %w", ErrConnectionNotFound),
	} {
		t.Run(name, func(t *testing.T) {
			repo := newServiceFakeRepo()
			conn := Connection{
				ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", AccessToken: testAccessToken,
				ImportWatchedEnabled: true, ImportProgressEnabled: true,
			}
			key := connectionKey(conn.Provider, conn.UserID, conn.ProfileID)
			repo.connections[key] = conn
			progressRead := false
			registry := NewRegistry()
			if err := registry.Register(watchedAndProgressImporter{repo: repo, readErr: readErr, progressRead: &progressRead}); err != nil {
				t.Fatal(err)
			}
			service := NewService(repo, registry).
				WithMatcher(unmatchedMatcherStub{}).
				WithWatchState(noopWatchState{}).
				WithUserStoreProvider(staticStoreProvider{})

			err := service.SyncConnection(t.Context(), conn, "manual")
			if err == nil || !strings.Contains(err.Error(), ErrConnectionNotFound.Error()) {
				t.Fatalf("SyncConnection error = %v, want the disconnect reported", err)
			}
			if _, exists := repo.connections[key]; exists {
				t.Fatal("disconnected connection restored")
			}
			if progressRead {
				t.Fatal("the run kept syncing a disconnected connection")
			}
		})
	}
}

func TestServiceSyncFlowClearsOnlyAnEndedRateLimit(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for name, until := range map[string]time.Time{"ended": now.Add(-time.Minute), "running": now.Add(time.Minute)} {
		t.Run(name, func(t *testing.T) {
			repo := newServiceFakeRepo()
			conn := Connection{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", RateLimitedUntil: &until}
			key := connectionKey(conn.Provider, conn.UserID, conn.ProfileID)
			repo.connections[key] = conn
			service := NewService(repo, NewRegistry()).
				WithMatcher(unmatchedMatcherStub{}).
				WithWatchState(noopWatchState{})
			service.now = func() time.Time { return now }

			if _, err := service.ImportWatched(t.Context(), conn, ServerConfig{}, watchedImporterStub{}); err != nil {
				t.Fatalf("ImportWatched: %v", err)
			}
			got := repo.connections[key].RateLimitedUntil
			if name == "ended" && got != nil || name == "running" && (got == nil || !got.Equal(until)) {
				t.Fatalf("rate limit after the flow = %v", got)
			}
		})
	}
}

func TestServiceRecordsEventErrorsWithoutRestoringConnections(t *testing.T) {
	repo := newServiceFakeRepo()
	service := NewService(repo, NewRegistry())
	gone := Connection{ID: "conn-1", Provider: "trakt", UserID: 7, ProfileID: "profile-1", AccessToken: testOldAccessToken}

	service.recordLocalWatchEventError(t.Context(), gone, errors.New("provider unavailable"))
	// A failed token refresh leaves an empty connection behind.
	service.recordLocalWatchEventError(t.Context(), Connection{}, errors.New("refresh failed"))

	if len(repo.connections) != 0 {
		t.Fatalf("event errors created connections: %+v", repo.connections)
	}
}

func TestPluginProviderPersistsRotatedCredentialsOnlyOverTheOnesItUsed(t *testing.T) {
	used := Connection{
		ID: "connection", Provider: testPluginProviderKey, UserID: 1, ProfileID: "profile",
		AccessToken: "old", RefreshToken: "old-refresh",
	}
	rotated := &pluginv1.WatchSyncCredentials{AccessToken: "rotated", RefreshToken: "rotated-refresh"}

	t.Run("unchanged", func(t *testing.T) {
		repo := &fakePluginCredentialRepository{saved: used}
		provider := &PluginProvider{repository: repo}
		conn, err := provider.persistUpdatedCredentials(t.Context(), used, rotated)
		if err != nil || conn.AccessToken != "rotated" || repo.saved.AccessToken != "rotated" {
			t.Fatalf("conn=%+v saved=%+v err=%v", conn, repo.saved, err)
		}
	})
	t.Run("changed meanwhile", func(t *testing.T) {
		reconnected := used
		reconnected.AccessToken, reconnected.RefreshToken = "reconnected", "reconnected-refresh"
		repo := &fakePluginCredentialRepository{saved: reconnected}
		provider := &PluginProvider{repository: repo}
		conn, err := provider.persistUpdatedCredentials(t.Context(), used, rotated)
		if err != nil || conn.AccessToken != "rotated" {
			t.Fatalf("the call should finish with its rotated credentials: conn=%+v err=%v", conn, err)
		}
		if repo.saved.AccessToken != "reconnected" {
			t.Fatalf("stored credentials overwritten: %+v", repo.saved)
		}
	})
	t.Run("rotated again by a later call in the same flow", func(t *testing.T) {
		repo := &fakePluginCredentialRepository{saved: used}
		client := &fakeWatchSyncPluginClient{listResponses: []*pluginv1.WatchSyncListRemoteStateResponse{
			{UpdatedCredentials: rotated},
			{UpdatedCredentials: &pluginv1.WatchSyncCredentials{AccessToken: "rotated-again", RefreshToken: "rotated-again-refresh"}},
		}}
		provider, err := NewPluginProvider(PluginProviderOptions{
			InstallationID: 4, ProviderKey: testPluginProviderKey, CapabilityID: testPluginCapabilityID,
			Descriptor: &pluginv1.WatchSyncProviderDescriptor{
				AuthMethods:   []pluginv1.WatchSyncAuthMethod{pluginv1.WatchSyncAuthMethod_WATCH_SYNC_AUTH_METHOD_API_KEY},
				ImportWatched: true, MaxBatchSize: 25,
			},
			ResolveClient: func(context.Context, int, string) (WatchSyncPluginClient, error) { return client, nil },
			Repository:    repo,
		})
		if err != nil {
			t.Fatal(err)
		}
		// A flow hands both calls the connection it loaded at the start.
		for range 2 {
			if _, err := provider.FetchWatchedBatch(t.Context(), ServerConfig{}, used); err != nil {
				t.Fatal(err)
			}
		}
		if got := client.listRequests[1].GetContext().GetCredentials().GetAccessToken(); got != "rotated" {
			t.Fatalf("second call sent access token %q, want the rotated one", got)
		}
		if repo.saved.AccessToken != "rotated-again" {
			t.Fatalf("stored access token = %q, want the second rotation", repo.saved.AccessToken)
		}
	})
	t.Run("disconnected", func(t *testing.T) {
		repo := &fakePluginCredentialRepository{err: ErrConnectionNotFound}
		provider := &PluginProvider{repository: repo}
		if _, err := provider.persistUpdatedCredentials(t.Context(), used, rotated); !isRetryableProviderError(err) || !errors.Is(err, ErrConnectionNotFound) {
			t.Fatalf("err = %v, want a retryable error", err)
		}
	})
}
