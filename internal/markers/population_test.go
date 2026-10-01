package markers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type populationSettings map[string]string

func (s populationSettings) Get(_ context.Context, key string) (string, error) { return s[key], nil }

type populationResolver struct{ ids ExternalIDs }

func (r populationResolver) ResolveForFile(context.Context, *models.MediaFile) (ExternalIDs, error) {
	return r.ids, nil
}

type populationProvider struct {
	id       string
	revision string
	fetch    func() (Result, error)
	calls    int
}

func (p *populationProvider) ID() string            { return p.id }
func (p *populationProvider) CacheRevision() string { return p.revision }
func (p *populationProvider) FetchMarkers(context.Context, Request) (Result, error) {
	p.calls++
	return p.fetch()
}

type populationRecorder struct {
	completions map[string]FetchCompletion
	cooldowns   map[string]time.Time
	cached      map[string]Result
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
func (s *populationRecorder) Candidates(context.Context, map[string]string, int, int, bool) ([]int, error) {
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
	_, changed, err := service.Populate(t.Context(), &models.MediaFile{ID: 1})
	if err != nil || changed || store.claimed != 0 {
		t.Fatalf("before setup: changed=%v err=%v claims=%d", changed, err, store.claimed)
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
