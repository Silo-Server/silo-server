package markers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/models"
)

type populationSettings map[string]string

func (s populationSettings) Get(_ context.Context, key string) (string, error) { return s[key], nil }

// contextSettings reads settings the way the database does: not at all once
// the request context is done.
type contextSettings struct{ populationSettings }

func (s contextSettings) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.populationSettings.Get(ctx, key)
}

type populationResolver struct{ ids ExternalIDs }

func (r populationResolver) ResolveForFile(context.Context, *models.MediaFile) (ExternalIDs, error) {
	return r.ids, nil
}

type populationProvider struct {
	id       string
	revision string
	fetch    func() (Result, error)
	// fetchContext, when set, replaces fetch for tests that need the
	// request context.
	fetchContext func(context.Context) (Result, error)
	calls        int
}

func (p *populationProvider) ID() string            { return p.id }
func (p *populationProvider) CacheRevision() string { return p.revision }
func (p *populationProvider) FetchMarkers(ctx context.Context, _ Request) (Result, error) {
	p.calls++
	if p.fetchContext != nil {
		return p.fetchContext(ctx)
	}
	return p.fetch()
}

type populationRecorder struct {
	completions map[string]FetchCompletion
	cooldowns   map[string]time.Time
	cached      map[string]Result
	released    []string
	claimed     int
}

func (s *populationRecorder) Eligible(context.Context, int) (bool, error) { return true, nil }
func (s *populationRecorder) Claim(_ context.Context, id int, provider, identity, _ string, _ bool) (FetchClaim, bool, error) {
	s.claimed++
	return FetchClaim{FileID: id, Provider: provider, Identity: identity, Token: "test-token"}, true, nil
}
func (s *populationRecorder) Complete(_ context.Context, claim FetchClaim, result FetchCompletion) error {
	if s.completions == nil {
		s.completions = make(map[string]FetchCompletion)
	}
	s.completions[claim.Provider] = result
	return nil
}
func (s *populationRecorder) Release(_ context.Context, claim FetchClaim) error {
	s.released = append(s.released, claim.Provider)
	return nil
}
func (s *populationRecorder) Cooldown(_ context.Context, provider, _ string, until time.Time) error {
	if s.cooldowns == nil {
		s.cooldowns = make(map[string]time.Time)
	}
	s.cooldowns[provider] = until
	return nil
}
func (s *populationRecorder) Cached(context.Context, int, string) (map[string]Result, error) {
	return s.cached, nil
}
func (s *populationRecorder) CooldownEnd(context.Context, map[string]string) (time.Time, error) {
	return time.Time{}, nil
}
func (s *populationRecorder) Candidates(context.Context, map[string]string) ([]int, error) {
	return nil, nil
}

func populationFixture(t *testing.T, storage OnlineStorage, provider *populationProvider) (*PopulationService, *populationRecorder) {
	t.Helper()
	registry := NewRegistry(nil)
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	store := &populationRecorder{}
	service := NewPopulationService(PopulationOptions{
		Registry: registry, Store: store,
		Settings: populationSettings{"setup.completed": "true", SettingMode: "online", SettingOnlineStorage: string(storage), SettingLazyPlayback: "true"},
		Resolver: populationResolver{ExternalIDs{Kind: ItemKindEpisode, TmdbID: "42", SeasonNumber: 1, EpisodeNumber: 2}},
	})
	return service, store
}

func TestPopulationOnDemandKeepsManualAndRepeatedRangesWithoutPersisting(t *testing.T) {
	provider := &populationProvider{id: "provider", fetch: func() (Result, error) {
		return Result{Markers: []Marker{
			{Kind: MarkerKindIntro, Start: 0, End: 40 * time.Second},
			{Kind: MarkerKindCredits, Start: 800 * time.Second, End: 850 * time.Second},
			{Kind: MarkerKindCredits, Start: 900 * time.Second, End: 950 * time.Second},
		}}, nil
	}}
	service, store := populationFixture(t, OnlineStorageOnDemand, provider)
	start, end, source := 10.0, 30.0, models.MarkerSourceManual
	file := &models.MediaFile{ID: 1, Duration: 1000, IntroStart: &start, IntroEnd: &end, IntroMarkersSource: &source}
	service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) {
		t.Fatal("on-demand wrote markers")
		return false, nil
	}
	notified := 0
	service.opts.Notify = func(context.Context, *models.MediaFile) { notified++ }
	for range 2 {
		effective, changed, err := service.Populate(t.Context(), file)
		if err != nil || !changed {
			t.Fatalf("Populate: changed=%v err=%v", changed, err)
		}
		if *effective.IntroStart != 10 || *effective.IntroEnd != 30 || len(effective.MarkerSegments) != 3 {
			t.Fatalf("effective marker projection: %+v", effective.MarkerSegments)
		}
	}
	if provider.calls != 1 {
		t.Fatalf("cached provider requests=%d, want 1", provider.calls)
	}
	if file.CreditsStart != nil || len(file.MarkerSegments) != 0 {
		t.Fatal("on-demand mutated input")
	}
	if notified != 2 {
		t.Fatalf("notifications=%d, want 2", notified)
	}
	for _, completion := range store.completions {
		if completion.Result != nil {
			t.Fatal("on-demand persisted a provider response")
		}
	}
}

func TestPopulationRequiresCompletedSetup(t *testing.T) {
	provider := &populationProvider{id: "provider", fetch: func() (Result, error) { t.Fatal("request before setup completion"); return Result{}, nil }}
	service, store := populationFixture(t, OnlineStorageStored, provider)
	service.opts.Settings.(populationSettings)["setup.completed"] = "false"
	var logs bytes.Buffer
	service.opts.Registry.logger = slog.New(slog.NewTextHandler(&logs, nil))
	// With online lookups switched off, finishing the wizard would not start
	// them, so the notice must not suggest it.
	service.opts.Settings.(populationSettings)[SettingMode] = "local"
	if _, _, err := service.Refresh(t.Context(), &models.MediaFile{ID: 1}); err != nil || logs.Len() != 0 {
		t.Fatalf("local mode before setup: err=%v logs=%q", err, logs.String())
	}
	service.opts.Settings.(populationSettings)[SettingMode] = "online"
	for range 2 {
		_, changed, err := service.Populate(t.Context(), &models.MediaFile{ID: 1})
		if err != nil || changed || store.claimed != 0 {
			t.Fatalf("before setup: changed=%v err=%v claims=%d", changed, err, store.claimed)
		}
		if _, changed, err := service.Refresh(t.Context(), &models.MediaFile{ID: 1}); err != nil || changed || store.claimed != 0 {
			t.Fatalf("refresh before setup: changed=%v err=%v claims=%d", changed, err, store.claimed)
		}
	}
	// The skip is otherwise silent: playback and admin refresh report a
	// queued lookup that never reaches a provider.
	if got := strings.Count(logs.String(), "paused until the setup wizard is finished"); got != 1 {
		t.Fatalf("setup wait logged %d times, want once:\n%s", got, logs.String())
	}
}

func TestPopulationRejectsFileReplacementDuringLookup(t *testing.T) {
	for _, storage := range []OnlineStorage{OnlineStorageStored, OnlineStorageOnDemand} {
		t.Run(string(storage), func(t *testing.T) {
			file := &models.MediaFile{ID: 1, Duration: 1000, FileHash: "old"}
			provider := &populationProvider{id: "provider", fetch: func() (Result, error) {
				replacement := *file
				replacement.FileHash = "new"
				file = &replacement
				return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}, nil
			}}
			service, _ := populationFixture(t, storage, provider)
			service.opts.LoadFile = func(context.Context, int) (*models.MediaFile, error) { copy := *file; return &copy, nil }
			service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) {
				t.Fatal("wrote result for old file")
				return false, nil
			}
			result, changed, err := service.Populate(t.Context(), file)
			if err == nil || changed || result.FileHash != "new" || result.IntroStart != nil {
				t.Fatalf("stale result applied: changed=%v err=%v hash=%q", changed, err, result.FileHash)
			}
		})
	}
}

func TestPopulationQuotaPreservesCachedPreferredProvider(t *testing.T) {
	limited := &populationProvider{id: "preferred", fetch: func() (Result, error) { return Result{}, &RetryAfterError{RetryAfter: 2 * time.Hour} }}
	service, store := populationFixture(t, OnlineStorageStored, limited)
	fallback := &populationProvider{id: "fallback", fetch: func() (Result, error) {
		return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 40 * time.Second}}}, nil
	}}
	if err := service.opts.Registry.Register(fallback); err != nil {
		t.Fatal(err)
	}
	store.cached = map[string]Result{"preferred": {Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}}
	var written Result
	service.opts.Write = func(_ context.Context, _ *models.MediaFile, result Result) (bool, error) {
		written = result
		return true, nil
	}
	_, changed, err := service.Populate(t.Context(), &models.MediaFile{ID: 1, Duration: 1000})
	if err == nil || !changed {
		t.Fatalf("partial success: changed=%v err=%v", changed, err)
	}
	if len(written.Markers) != 1 || written.Markers[0].ProviderID != "preferred" || written.Markers[0].End != 30*time.Second {
		t.Fatalf("discarded cached preferred markers: %+v", written)
	}
	if time.Until(store.cooldowns["preferred"]) < 119*time.Minute {
		t.Fatal("provider cooldown was not recorded")
	}
	if store.completions["preferred"].Outcome != "limited" || store.completions["fallback"].Result == nil {
		t.Fatalf("incorrect request states: %+v", store.completions)
	}
}

// Recent releases keep short retries while crowd-sourced markers arrive;
// older ones follow the long durations a quota-limited sync needs.
func TestPopulationStoredFreshnessFollowsReleaseDate(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		name     string
		markers  []Marker
		released time.Duration
		want     time.Duration
	}{
		{"recent miss", nil, -2 * day, markerRecentMissTTL},
		{"recent hit", []Marker{{Kind: MarkerKindIntro, End: 30 * time.Second}}, -2 * day, markerRecentPositiveTTL},
		{"old miss", nil, -365 * day, markerMissTTL},
		{"old hit", []Marker{{Kind: MarkerKindIntro, End: 30 * time.Second}}, -365 * day, markerPositiveTTL},
		{"upcoming miss", nil, 2 * day, markerRecentMissTTL},
		{"far future miss", nil, 90 * day, markerMissTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &populationProvider{id: "provider", fetch: func() (Result, error) { return Result{Markers: tc.markers}, nil }}
			service, store := populationFixture(t, OnlineStorageStored, provider)
			service.opts.Resolver = populationResolver{ExternalIDs{Kind: ItemKindMovie, TmdbID: "42", Released: time.Now().Add(tc.released)}}
			service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) { return true, nil }
			if _, _, err := service.Populate(t.Context(), &models.MediaFile{ID: 1, Duration: 1000}); err != nil {
				t.Fatal(err)
			}
			if got := time.Until(store.completions["provider"].RetryAt); got < tc.want-time.Minute || got > tc.want {
				t.Fatalf("result fresh for %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPopulationDoesNotMarkFailedStorageFresh(t *testing.T) {
	provider := &populationProvider{id: "provider", fetch: func() (Result, error) {
		return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}, nil
	}}
	service, store := populationFixture(t, OnlineStorageStored, provider)
	service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) {
		return false, errors.New("write unavailable")
	}
	_, changed, err := service.Populate(t.Context(), &models.MediaFile{ID: 1, Duration: 1000})
	if err == nil || changed {
		t.Fatalf("failed write: changed=%v err=%v", changed, err)
	}
	if completion := store.completions["provider"]; completion.Outcome != "error" || completion.Result != nil {
		t.Fatalf("failed write marked fresh: %+v", completion)
	}
}

func TestRegistryPreservesEveryRangeFromWinningProvider(t *testing.T) {
	registry := NewRegistry(nil)
	_ = registry.Register(&fakeProvider{id: "preferred", result: Result{Markers: []Marker{
		{Kind: MarkerKindCredits, Start: 900 * time.Second, End: 950 * time.Second},
		{Kind: MarkerKindCredits, Start: 800 * time.Second, End: 850 * time.Second},
	}}})
	_ = registry.Register(&fakeProvider{id: "fallback", result: Result{Markers: []Marker{{Kind: MarkerKindCredits, Start: 600 * time.Second, End: 999 * time.Second, Confidence: 1}}}})
	result, ok, err := populateRegistryForTest(t.Context(), registry)
	if err != nil || !ok || len(result.Markers) != 2 {
		t.Fatalf("multi-range merge: %+v, %v", result, err)
	}
	if result.Markers[0].Start != 800*time.Second || result.Markers[1].Start != 900*time.Second || result.Markers[0].ProviderID != "preferred" {
		t.Fatalf("merged ranges: %+v", result.Markers)
	}
}

func TestPopulationStoredPlaybackToggleDoesNotBlockExplicitRefresh(t *testing.T) {
	provider := &populationProvider{id: "provider", fetch: func() (Result, error) { return Result{}, nil }}
	service, _ := populationFixture(t, OnlineStorageStored, provider)
	service.opts.Settings.(populationSettings)[SettingLazyPlayback] = "false"
	service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) { return false, nil }
	file := &models.MediaFile{ID: 1, Duration: 1000}
	if _, _, err := service.Populate(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 {
		t.Fatal("stored playback bypassed disabled lazy lookup")
	}
	if _, _, err := service.Refresh(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatal("explicit refresh did not fetch")
	}
}

func TestPopulationOnDemandRefreshBypassesMemoryCache(t *testing.T) {
	result := Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 10 * time.Second, End: 30 * time.Second}}}
	provider := &populationProvider{id: "provider", fetch: func() (Result, error) { return result, nil }}
	service, store := populationFixture(t, OnlineStorageOnDemand, provider)
	file := &models.MediaFile{ID: 1, Duration: 1000}
	if _, _, err := service.Populate(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	result = Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 10 * time.Second, End: 40 * time.Second}}}
	effective, _, err := service.Refresh(t.Context(), file)
	if err != nil || provider.calls != 2 || effective.IntroEnd == nil || *effective.IntroEnd != 40 {
		t.Fatalf("refresh did not fetch corrected markers: calls=%d markers=%+v err=%v", provider.calls, effective.MarkerSegments, err)
	}
	result = Result{}
	effective, _, err = service.Refresh(t.Context(), file)
	if err != nil || provider.calls != 3 || len(effective.MarkerSegments) != 0 {
		t.Fatalf("refresh did not fetch withdrawn markers: calls=%d markers=%+v err=%v", provider.calls, effective.MarkerSegments, err)
	}
	if _, _, err := service.Populate(t.Context(), file); err != nil || provider.calls != 3 {
		t.Fatalf("ordinary lookup did not reuse refreshed cache: calls=%d err=%v", provider.calls, err)
	}
	for _, completion := range store.completions {
		if completion.Result != nil {
			t.Fatal("on-demand refresh persisted a provider response")
		}
	}
}

func TestPopulationRejectsChangedSettingsAndCredentials(t *testing.T) {
	for _, change := range []string{"mode", "storage", "credentials"} {
		t.Run(change, func(t *testing.T) {
			provider := &populationProvider{id: "provider", revision: "old"}
			service, store := populationFixture(t, OnlineStorageStored, provider)
			provider.fetch = func() (Result, error) {
				switch change {
				case "mode":
					service.opts.Settings.(populationSettings)[SettingMode] = "off"
				case "storage":
					service.opts.Settings.(populationSettings)[SettingOnlineStorage] = "on_demand"
				case "credentials":
					provider.revision = "new"
				}
				return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}, nil
			}
			service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) {
				t.Fatal("applied stale settings or credentials")
				return false, nil
			}
			_, changed, _ := service.Populate(t.Context(), &models.MediaFile{ID: 1, Duration: 1000})
			if changed {
				t.Fatal("returned stale overlay")
			}
			if completion := store.completions[provider.id]; completion.Outcome != "error" || completion.Result != nil {
				t.Fatalf("stored stale lookup result: %+v", completion)
			}
		})
	}
}

func TestPopulationReturnsConcurrentManualEditAfterStoredNoOp(t *testing.T) {
	file := &models.MediaFile{ID: 1, Duration: 1000}
	provider := &populationProvider{id: "provider", fetch: func() (Result, error) {
		start, end, source := 15.0, 45.0, models.MarkerSourceManual
		latest := *file
		latest.IntroStart = &start
		latest.IntroEnd = &end
		latest.IntroMarkersSource = &source
		file = &latest
		return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}, nil
	}}
	service, _ := populationFixture(t, OnlineStorageStored, provider)
	service.opts.LoadFile = func(context.Context, int) (*models.MediaFile, error) { copy := *file; return &copy, nil }
	service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) { return false, nil }
	result, changed, err := service.Populate(t.Context(), file)
	if err != nil || changed || result.IntroStart == nil || *result.IntroStart != 15 || *result.IntroEnd != 45 {
		t.Fatalf("lost concurrent manual edit: changed=%v err=%v file=%+v", changed, err, result)
	}
}

func TestPopulationUsesPriorityChangedDuringFetch(t *testing.T) {
	first := &populationProvider{id: "first"}
	service, _ := populationFixture(t, OnlineStorageStored, first)
	config := &ProviderConfigStore{cache: map[string]ProviderConfig{
		"first":  {Provider: "first", FetchEnabled: true, FetchPriority: 1},
		"second": {Provider: "second", FetchEnabled: true, FetchPriority: 2},
	}}
	service.opts.Registry.UseConfigStore(config)
	first.fetch = func() (Result, error) {
		config.mu.Lock()
		config.cache["first"] = ProviderConfig{Provider: "first", FetchEnabled: true, FetchPriority: 3}
		config.mu.Unlock()
		return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}, nil
	}
	second := &populationProvider{id: "second", fetch: func() (Result, error) {
		return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 40 * time.Second}}}, nil
	}}
	if err := service.opts.Registry.Register(second); err != nil {
		t.Fatal(err)
	}
	var written Result
	service.opts.Write = func(_ context.Context, _ *models.MediaFile, result Result) (bool, error) {
		written = result
		return true, nil
	}
	if _, _, err := service.Populate(t.Context(), &models.MediaFile{ID: 1, Duration: 1000}); err != nil {
		t.Fatal(err)
	}
	if len(written.Markers) != 1 || written.Markers[0].ProviderID != "second" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("stale priority or repeated fetch: %+v; calls=%d/%d", written.Markers, first.calls, second.calls)
	}
}

// expiringContext is a caller context with a read's short deadline, which
// passes when expire is called, so a test can run it out mid-request without
// racing a timer.
type expiringContext struct {
	context.Context
	expired chan struct{}
}

func newExpiringContext(parent context.Context) *expiringContext {
	return &expiringContext{Context: parent, expired: make(chan struct{})}
}

func (c *expiringContext) expire()                     { close(c.expired) }
func (c *expiringContext) Done() <-chan struct{}       { return c.expired }
func (c *expiringContext) Deadline() (time.Time, bool) { return time.Now().Add(5 * time.Second), true }
func (c *expiringContext) Err() error {
	select {
	case <-c.expired:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// A viewer leaving, or a read running out its own short deadline, says
// nothing about the provider. The request must not be stored as a provider
// failure, which would hold the title in failure backoff with no markers.
func TestPopulationCanceledLookupIsNotAProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
		want error
		// start returns the caller context and a function that ends it.
		start func(context.Context) (context.Context, func())
	}{
		{"viewer left", codes.Canceled, context.Canceled, func(parent context.Context) (context.Context, func()) {
			ctx, cancel := context.WithCancel(parent)
			return ctx, cancel
		}},
		{"read deadline", codes.DeadlineExceeded, context.DeadlineExceeded, func(parent context.Context) (context.Context, func()) {
			ctx := newExpiringContext(parent)
			return ctx, ctx.expire
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, end := tc.start(t.Context())
			provider := &populationProvider{id: "provider", fetchContext: func(ctx context.Context) (Result, error) {
				end()
				<-ctx.Done()
				// Plugin providers answer through gRPC, which reports the
				// caller's cancellation as a status error.
				return Result{}, status.Error(tc.code, ctx.Err().Error())
			}}
			service, store := populationFixture(t, OnlineStorageStored, provider)
			store.cached = map[string]Result{"provider": {Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}}
			var logs bytes.Buffer
			service.opts.Registry.logger = slog.New(slog.NewTextHandler(&logs, nil))
			service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) {
				t.Error("wrote markers for a lookup that fetched nothing new")
				return false, nil
			}
			_, changed, err := service.Populate(ctx, &models.MediaFile{ID: 1, Duration: 1000})
			if changed || !errors.Is(err, tc.want) {
				t.Fatalf("Populate: changed=%v err=%v, want %v", changed, err, tc.want)
			}
			if completion, ok := store.completions["provider"]; ok {
				t.Fatalf("abandoned request stored as %+v", completion)
			}
			if len(store.released) != 1 || store.released[0] != "provider" {
				t.Fatalf("released claims=%v, want the provider's", store.released)
			}
			if strings.Contains(logs.String(), "marker provider fetch failed") {
				t.Fatalf("abandoned request logged as a provider failure:\n%s", logs.String())
			}
		})
	}
}

// A provider answer costs quota. When the viewer leaves just as it arrives,
// it must still be saved rather than recorded as a failed request.
func TestPopulationSavesFetchedMarkersAfterViewerLeaves(t *testing.T) {
	for _, tc := range []struct {
		name string
		// settingsUseContext makes the settings re-check fail on a
		// canceled context; otherwise the identity check is the first
		// step after the fetch to notice it.
		settingsUseContext bool
	}{
		{"settings re-check", true},
		{"identity check", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			intro := Marker{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}
			provider := &populationProvider{id: "provider", fetchContext: func(context.Context) (Result, error) {
				cancel()
				return Result{Markers: []Marker{intro}}, nil
			}}
			service, store := populationFixture(t, OnlineStorageStored, provider)
			if tc.settingsUseContext {
				service.opts.Settings = contextSettings{service.opts.Settings.(populationSettings)}
			}
			var written Result
			service.opts.Write = func(ctx context.Context, _ *models.MediaFile, result Result) (bool, error) {
				if err := ctx.Err(); err != nil {
					return false, err
				}
				written = result
				return true, nil
			}
			_, changed, err := service.Populate(ctx, &models.MediaFile{ID: 1, Duration: 1000})
			if !changed || !errors.Is(err, context.Canceled) {
				t.Fatalf("Populate: changed=%v err=%v", changed, err)
			}
			if completion := store.completions["provider"]; completion.Outcome != markerFetchHit || completion.Result == nil {
				t.Fatalf("fetched answer stored as %+v", completion)
			}
			if len(written.Markers) != 1 || written.Markers[0].End != intro.End {
				t.Fatalf("fetched markers not written: %+v", written)
			}
		})
	}
}

// Saving after the viewer leaves must not drop the stored answers of the
// providers it no longer asks.
func TestPopulationKeepsCachedProvidersAfterViewerLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := &populationProvider{id: "first", fetchContext: func(context.Context) (Result, error) {
		cancel()
		return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}, nil
	}}
	service, store := populationFixture(t, OnlineStorageStored, first)
	second := &populationProvider{id: "second", fetch: func() (Result, error) {
		t.Error("asked a provider after the viewer left")
		return Result{}, nil
	}}
	if err := service.opts.Registry.Register(second); err != nil {
		t.Fatal(err)
	}
	store.cached = map[string]Result{"second": {ProviderID: "second", Markers: []Marker{{Kind: MarkerKindCredits, Start: 800 * time.Second, End: 850 * time.Second}}}}
	var written Result
	service.opts.Write = func(_ context.Context, _ *models.MediaFile, result Result) (bool, error) {
		written = result
		return true, nil
	}
	if _, changed, err := service.Populate(ctx, &models.MediaFile{ID: 1, Duration: 1000}); !changed || !errors.Is(err, context.Canceled) {
		t.Fatalf("Populate: changed=%v err=%v", changed, err)
	}
	kinds := map[MarkerKind]string{}
	for _, marker := range written.Markers {
		kinds[marker.Kind] = marker.ProviderID
	}
	if kinds[MarkerKindIntro] != "first" || kinds[MarkerKindCredits] != "second" {
		t.Fatalf("written markers=%+v, want the fetched intro and the cached credits", written.Markers)
	}
}

// On-demand answers are completed as they arrive rather than saved at the
// end, so one fetched before the viewer left must still reach the caller and
// the players instead of waiting for the next lookup.
func TestPopulationOnDemandAppliesFetchedMarkersAfterViewerLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := &populationProvider{id: "first", fetch: func() (Result, error) {
		return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}, nil
	}}
	service, store := populationFixture(t, OnlineStorageOnDemand, first)
	second := &populationProvider{id: "second", fetchContext: func(ctx context.Context) (Result, error) {
		cancel()
		<-ctx.Done()
		return Result{}, status.Error(codes.Canceled, ctx.Err().Error())
	}}
	if err := service.opts.Registry.Register(second); err != nil {
		t.Fatal(err)
	}
	var notified *models.MediaFile
	service.opts.Notify = func(_ context.Context, file *models.MediaFile) { notified = file }
	effective, changed, err := service.Populate(ctx, &models.MediaFile{ID: 1, Duration: 1000})
	if !changed || !errors.Is(err, context.Canceled) {
		t.Fatalf("Populate: changed=%v err=%v", changed, err)
	}
	if effective.IntroEnd == nil || *effective.IntroEnd != 30 {
		t.Fatalf("fetched intro not applied: %+v", effective)
	}
	if notified == nil || notified.IntroEnd == nil {
		t.Fatal("players were not sent the fetched markers")
	}
	if len(store.released) != 1 || store.released[0] != "second" {
		t.Fatalf("released claims=%v, want the abandoned provider's", store.released)
	}
}

// Only the caller going away is exempt. A provider that times out on its
// own, or answers with a rate limit as the viewer leaves, is still recorded.
func TestPopulationStillRecordsProviderTimeoutsAndLimits(t *testing.T) {
	t.Run("provider timeout", func(t *testing.T) {
		provider := &populationProvider{id: "provider", fetch: func() (Result, error) {
			return Result{}, status.Error(codes.DeadlineExceeded, "context deadline exceeded")
		}}
		service, store := populationFixture(t, OnlineStorageStored, provider)
		service.opts.Registry.logger = slog.New(slog.DiscardHandler)
		if _, _, err := service.Populate(t.Context(), &models.MediaFile{ID: 1, Duration: 1000}); err == nil {
			t.Fatal("provider timeout returned no error")
		}
		if completion := store.completions["provider"]; completion.Outcome != markerFetchError {
			t.Fatalf("provider timeout stored as %+v", completion)
		}
		if len(store.released) != 0 {
			t.Fatalf("provider timeout released as abandoned: %v", store.released)
		}
	})
	t.Run("rate limit as the viewer leaves", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		provider := &populationProvider{id: "provider", fetchContext: func(context.Context) (Result, error) {
			cancel()
			return Result{}, &RetryAfterError{RetryAfter: time.Hour}
		}}
		service, store := populationFixture(t, OnlineStorageStored, provider)
		_, _, _ = service.Populate(ctx, &models.MediaFile{ID: 1, Duration: 1000})
		if completion := store.completions["provider"]; completion.Outcome != markerFetchLimited {
			t.Fatalf("rate limit stored as %+v", completion)
		}
		if time.Until(store.cooldowns["provider"]) < 59*time.Minute {
			t.Fatal("provider cooldown was not recorded")
		}
	})
}

// A save keeps the pass deadline while the caller waits, but a caller that
// has gone, or a pass with little time left, gets only the short grace.
func TestPopulationSaveDeadline(t *testing.T) {
	live, cancelLive := context.WithTimeout(t.Context(), time.Minute)
	defer cancelLive()
	gone, cancelGone := context.WithTimeout(t.Context(), time.Minute)
	cancelGone()
	nearlyDone, cancelNearlyDone := context.WithTimeout(t.Context(), time.Second)
	defer cancelNearlyDone()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want time.Duration
	}{
		{"caller waiting", live, time.Minute},
		{"caller gone", gone, markerSaveGrace},
		{"pass nearly over", nearlyDone, markerSaveGrace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := time.Until(saveDeadline(tc.ctx)); got > tc.want || got < tc.want-time.Second {
				t.Fatalf("save deadline in %v, want %v", got, tc.want)
			}
		})
	}
}

// A provider slower than a read's budget is cut off on every read. Once one
// read runs out of time waiting for it, other reads leave it alone for
// markerReadHold, while a lookup that can wait, such as playback, still asks.
func TestPopulationShortReadsHoldSlowProvider(t *testing.T) {
	for _, storage := range []OnlineStorage{OnlineStorageStored, OnlineStorageOnDemand} {
		t.Run(string(storage), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				provider := &populationProvider{id: "provider", fetchContext: func(ctx context.Context) (Result, error) {
					select {
					case <-time.After(10 * time.Second):
						return Result{Markers: []Marker{{Kind: MarkerKindIntro, Start: 0, End: 30 * time.Second}}}, nil
					case <-ctx.Done():
						return Result{}, status.FromContextError(ctx.Err()).Err()
					}
				}}
				service, store := populationFixture(t, storage, provider)
				service.opts.Write = func(context.Context, *models.MediaFile, Result) (bool, error) { return true, nil }
				lookup := func(budget time.Duration) (*models.MediaFile, error) {
					ctx, cancel := context.WithTimeout(t.Context(), budget)
					defer cancel()
					file, _, err := service.Populate(ctx, &models.MediaFile{ID: 1, Duration: 1000})
					return file, err
				}
				const read, playback = 5 * time.Second, 10 * time.Minute

				if _, err := lookup(read); !errors.Is(err, context.DeadlineExceeded) || provider.calls != 1 {
					t.Fatalf("first read: err=%v calls=%d", err, provider.calls)
				}
				if _, err := lookup(read); err != nil || provider.calls != 1 {
					t.Fatalf("read during the hold: err=%v calls=%d, want the provider left alone", err, provider.calls)
				}
				time.Sleep(markerReadHold)
				if _, err := lookup(read); !errors.Is(err, context.DeadlineExceeded) || provider.calls != 2 {
					t.Fatalf("read after the hold: err=%v calls=%d, want the provider asked again", err, provider.calls)
				}
				file, err := lookup(playback)
				if err != nil || provider.calls != 3 || file.IntroEnd == nil || *file.IntroEnd != 30 {
					t.Fatalf("playback during the hold: err=%v calls=%d, want the provider's answer", err, provider.calls)
				}
				if len(store.released) != 2 {
					t.Fatalf("released claims=%v, want the two cut-off reads", store.released)
				}
				if completion := store.completions["provider"]; completion.Outcome == markerFetchError {
					t.Fatalf("cut-off read stored as a provider failure: %+v", completion)
				}
			})
		})
	}
}

// A viewer leaving says nothing about how fast the provider is, so it does
// not hold the provider off from the next read.
func TestPopulationViewerLeavingDoesNotHoldProvider(t *testing.T) {
	provider := &populationProvider{id: "provider"}
	service, _ := populationFixture(t, OnlineStorageStored, provider)
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		provider.fetchContext = func(ctx context.Context) (Result, error) {
			cancel()
			<-ctx.Done()
			return Result{}, status.FromContextError(ctx.Err()).Err()
		}
		_, _, err := service.Populate(ctx, &models.MediaFile{ID: 1, Duration: 1000})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Populate: err=%v", err)
		}
	}
	if provider.calls != 2 {
		t.Fatalf("calls=%d, want the provider asked by both reads", provider.calls)
	}
}

// Holds are kept in memory per replica, so their number is capped like the
// on-demand answers; a full set gives up the hold that ends first.
func TestPopulationHoldsAreCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := NewPopulationService(PopulationOptions{})
		for i := range markerMemoryLimit + 1 {
			service.hold(fmt.Sprint(i))
			time.Sleep(time.Millisecond)
		}
		if len(service.holds) != markerMemoryLimit || service.held("0") || !service.held("1") || !service.held(fmt.Sprint(markerMemoryLimit)) {
			t.Fatalf("holds=%d, want %d without the first", len(service.holds), markerMemoryLimit)
		}
	})
}
