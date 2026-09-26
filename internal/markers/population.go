package markers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	markerFetchHit      = "hit"
	markerFetchMiss     = "miss"
	markerFetchError    = "error"
	markerFetchLimited  = "limited"
	markerFetchOnDemand = "on_demand"

	markerPositiveTTL  = 7 * 24 * time.Hour
	markerMissTTL      = 24 * time.Hour
	markerMemoryTTL    = 15 * time.Minute
	markerFetchTimeout = 45 * time.Second
	markerMemoryLimit  = 512
)

type PopulationSettings interface {
	Get(context.Context, string) (string, error)
}

type PopulationOptions struct {
	Registry *Registry
	Resolver ExternalIDResolver
	Settings PopulationSettings
	Store    PopulationStore
	LoadFile func(context.Context, int) (*models.MediaFile, error)
	// RefreshProviders reconciles persisted provider settings and runtime
	// revisions before a lookup and before its result is applied.
	RefreshProviders func(context.Context) error
	// Write must guard the expected file identity and preserve manual segments.
	Write  func(context.Context, *models.MediaFile, Result) (bool, error)
	Notify func(context.Context, *models.MediaFile)
}

type cachedMarkerResult struct {
	result  Result
	expires time.Time
}

// PopulationService owns the online lookup path used by playback, catalog
// reads, and scheduled synchronization. Local detection remains independent.
type PopulationService struct {
	opts   PopulationOptions
	slots  chan struct{}
	mu     sync.Mutex
	memory map[string]cachedMarkerResult
}

func NewPopulationService(opts PopulationOptions) *PopulationService {
	return &PopulationService{opts: opts, slots: make(chan struct{}, 4), memory: make(map[string]cachedMarkerResult)}
}

func (s *PopulationService) OnlineStorage(ctx context.Context) (OnlineStorage, error) {
	if s == nil || s.opts.Settings == nil {
		return OnlineStorageStored, nil
	}
	raw, err := s.opts.Settings.Get(ctx, SettingOnlineStorage)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(raw) == "" {
		return OnlineStorageStored, nil
	}
	normalized, err := NormalizeSetting(SettingOnlineStorage, raw)
	return OnlineStorage(normalized), err
}

func (s *PopulationService) enabled(ctx context.Context) (bool, error) {
	if s == nil || s.opts.Registry == nil || s.opts.Resolver == nil || s.opts.Store == nil || s.opts.Settings == nil {
		return false, nil
	}
	complete, err := s.opts.Settings.Get(ctx, "setup.completed")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(complete) != settingEnabled {
		return false, nil
	}
	raw, err := s.opts.Settings.Get(ctx, SettingMode)
	if err != nil {
		return false, err
	}
	mode := NormalizeMode(raw)
	return mode == ModeOnline || mode == ModeBoth, nil
}

// Populate returns an effective file even when one provider fails. Stored mode
// persists the selected result; on-demand mode overlays it on a copy only. The
// boolean reports a persisted change or an available on-demand overlay.
func (s *PopulationService) Populate(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	if s != nil && s.opts.Settings != nil {
		storage, err := s.OnlineStorage(ctx)
		if err != nil {
			return file, false, err
		}
		if storage == OnlineStorageStored {
			lazy, err := s.opts.Settings.Get(ctx, SettingLazyPlayback)
			if err != nil {
				return file, false, err
			}
			if strings.TrimSpace(lazy) != settingEnabled {
				return file, false, nil
			}
		}
	}
	return s.populate(ctx, file, false, true)
}

// Refresh bypasses successful cached results for an explicit refresh.
// Active leases, provider cooldowns and failed-request backoff still apply.
func (s *PopulationService) Refresh(ctx context.Context, file *models.MediaFile) (*models.MediaFile, bool, error) {
	return s.populate(ctx, file, true, true)
}

func (s *PopulationService) populate(ctx context.Context, file *models.MediaFile, refresh, waitForLease bool) (*models.MediaFile, bool, error) {
	if file == nil || file.ID <= 0 {
		return file, false, nil
	}
	enabled, err := s.enabled(ctx)
	if err != nil || !enabled {
		return file, false, err
	}
	storage, err := s.OnlineStorage(ctx)
	if err != nil {
		return file, false, err
	}
	if s.opts.RefreshProviders != nil {
		if err := s.opts.RefreshProviders(ctx); err != nil {
			return file, false, err
		}
	}
	entries := s.opts.Registry.fetchEntries()
	if len(entries) == 0 {
		return file, false, nil
	}
	// A pass must finish writing before its two-minute leases expire. The file
	// lease also prevents different replicas from projecting competing provider
	// results onto the same file from independently read cache snapshots.
	ctx, cancelPass := context.WithTimeout(ctx, 90*time.Second)
	defer cancelPass()
	fileClaim, claimed, err := s.claimFile(ctx, file, waitForLease)
	if err != nil || !claimed {
		return file, false, err
	}
	defer func() {
		_ = s.complete(ctx, fileClaim, FetchCompletion{Outcome: markerFetchOnDemand, RetryAt: time.Now()})
	}()
	if s.opts.LoadFile != nil {
		loaded, err := s.opts.LoadFile(ctx, file.ID)
		if err != nil {
			return file, false, err
		}
		if loaded == nil {
			return file, false, nil
		}
		file = loaded
	}
	eligible, err := s.opts.Store.Eligible(ctx, file.ID)
	if err != nil || !eligible {
		return file, false, err
	}
	ids, err := s.opts.Resolver.ResolveForFile(ctx, file)
	if err != nil {
		return file, false, err
	}
	if !ids.HasAnyID() || (ids.Kind == ItemKindEpisode && (ids.SeasonNumber < 0 || ids.EpisodeNumber <= 0)) {
		return file, false, nil
	}
	identity := populationIdentity(file, ids, entries)
	request := Request{
		Kind:          ids.Kind,
		ExternalIDs:   ids.AsRequestMap(),
		SeasonNumber:  ids.SeasonNumber,
		EpisodeNumber: ids.EpisodeNumber,
		Duration:      time.Duration(file.Duration) * time.Second,
	}
	cached := make(map[string]Result)
	if storage == OnlineStorageStored {
		cached, err = s.opts.Store.Cached(ctx, file.ID, identity)
		if err != nil {
			return file, false, err
		}
	}
	results := make([]providerResult, 0, len(entries))
	type completedFetch struct {
		claim      FetchClaim
		completion FetchCompletion
	}
	var pending []completedFetch
	var failures []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		providerID := entry.provider.ID()
		key := identity + ":" + providerID
		if storage == OnlineStorageOnDemand && !refresh {
			if result, ok := s.cachedMemory(key); ok {
				results = append(results, providerResult{entry: entry, result: result, refreshed: true})
				continue
			}
		}
		// Reserve network capacity before taking a database lease, so requests
		// waiting behind other viewers cannot outlive their leases in the queue.
		select {
		case s.slots <- struct{}{}:
		case <-ctx.Done():
			failures = append(failures, ctx.Err())
			continue
		}
		claim, claimed, claimErr := s.opts.Store.Claim(ctx, file.ID, providerID, identity, providerRevision(entry.provider), storage == OnlineStorageOnDemand || refresh)
		if claimErr != nil || !claimed {
			<-s.slots
			if claimErr != nil {
				failures = append(failures, claimErr)
			}
			if result, ok := cached[providerID]; ok {
				results = append(results, providerResult{entry: entry, result: result})
			}
			continue
		}
		fetchCtx, cancel := context.WithTimeout(ctx, markerFetchTimeout)
		result, fetchErr := entry.provider.FetchMarkers(fetchCtx, request)
		cancel()
		<-s.slots
		if fetchErr != nil {
			failures = append(failures, s.recordFetchFailure(ctx, entry.provider, request, claim, fetchErr)...)
			if result, ok := cached[providerID]; ok {
				results = append(results, providerResult{entry: entry, result: result})
			}
			continue
		}
		result.ProviderID = providerID
		results = append(results, providerResult{entry: entry, result: result, refreshed: true})
		ttl := markerPositiveTTL
		outcome := markerFetchHit
		if len(result.Markers) == 0 {
			ttl = markerMissTTL
			outcome = markerFetchMiss
		}
		completion := FetchCompletion{Outcome: outcome, RetryAt: time.Now().Add(ttl), Result: &result}
		if storage == OnlineStorageOnDemand {
			s.remember(key, result)
			completion = FetchCompletion{Outcome: markerFetchOnDemand, RetryAt: time.Now()}
			if err := s.complete(ctx, claim, completion); err != nil {
				failures = append(failures, err)
			}
		} else {
			pending = append(pending, completedFetch{claim: claim, completion: completion})
		}
	}
	if len(results) == 0 {
		return file, false, errors.Join(failures...)
	}
	stillEnabled, settingsErr := s.enabled(ctx)
	currentStorage, storageErr := s.OnlineStorage(ctx)
	if settingsErr != nil || storageErr != nil || !stillEnabled || currentStorage != storage {
		for _, fetch := range pending {
			if err := s.complete(ctx, fetch.claim, FetchCompletion{Outcome: markerFetchError, RetryAt: time.Now().Add(time.Minute), Error: "marker settings changed"}); err != nil {
				failures = append(failures, err)
			}
		}
		return file, false, errors.Join(append(failures, settingsErr, storageErr)...)
	}
	current, checkErr := s.checkIdentity(ctx, file, identity)
	file = current
	if storage == OnlineStorageOnDemand {
		if checkErr != nil {
			return file, false, errors.Join(append(failures, checkErr)...)
		}
		effective := ApplyResult(file, s.selectResults(results))
		if s.opts.Notify != nil {
			s.opts.Notify(ctx, effective)
		}
		return effective, true, errors.Join(failures...)
	}
	var wrote bool
	merged := s.selectResults(results)
	if checkErr != nil {
		err = checkErr
	} else if s.opts.Write == nil {
		err = errors.New("marker writer unavailable")
	} else {
		wrote, err = s.opts.Write(ctx, file, merged)
	}
	if err != nil {
		failures = append(failures, err)
	}
	for _, fetch := range pending {
		completion := fetch.completion
		if err != nil {
			completion = FetchCompletion{Outcome: markerFetchError, RetryAt: time.Now().Add(time.Minute), Error: "marker storage failed"}
		}
		if completeErr := s.complete(ctx, fetch.claim, completion); completeErr != nil {
			failures = append(failures, completeErr)
		}
	}
	if wrote {
		file = ApplyResult(file, merged)
		if s.opts.LoadFile != nil {
			if refreshed, loadErr := s.opts.LoadFile(ctx, file.ID); loadErr == nil && refreshed != nil {
				file = refreshed
			} else if loadErr != nil {
				failures = append(failures, loadErr)
			}
		}
		if s.opts.Notify != nil {
			s.opts.Notify(ctx, file)
		}
	}
	return file, wrote, errors.Join(failures...)
}

func (s *PopulationService) recordFetchFailure(ctx context.Context, provider Provider, request Request, claim FetchClaim, fetchErr error) []error {
	providerID := provider.ID()
	after, limited := RetryAfter(fetchErr)
	if !limited {
		s.opts.Registry.logProviderError(providerID, request, fetchErr)
	}
	failures := []error{fmt.Errorf("marker provider %s: %w", providerID, fetchErr)}
	retry := fetchRetry(claim.Failures)
	outcome := markerFetchError
	if limited {
		if after > 0 {
			retry = after
		}
		outcome = markerFetchLimited
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err := s.opts.Store.Cooldown(cleanupCtx, providerID, providerRevision(provider), time.Now().Add(retry)); err != nil {
			failures = append(failures, err)
		}
		cancel()
	}
	if err := s.complete(ctx, claim, FetchCompletion{Outcome: outcome, RetryAt: time.Now().Add(retry), Error: fetchErr.Error()}); err != nil {
		failures = append(failures, err)
	}
	return failures
}

// Selection settings can change while a request is in flight. Apply current
// priorities to the fetched/cached responses without invalidating those caches.
func (s *PopulationService) selectResults(results []providerResult) Result {
	entries := make(map[string]fetchEntry)
	for _, entry := range s.opts.Registry.fetchEntries() {
		entries[entry.provider.ID()] = entry
	}
	current := make([]providerResult, 0, len(results))
	for _, result := range results {
		if entry, ok := entries[result.entry.provider.ID()]; ok {
			result.entry = entry
			current = append(current, result)
		}
	}
	return mergeProviderResults(current)
}

func (s *PopulationService) claimFile(ctx context.Context, file *models.MediaFile, wait bool) (FetchClaim, bool, error) {
	delay := 50 * time.Millisecond
	for {
		claim, claimed, err := s.opts.Store.Claim(ctx, file.ID, "@population", models.MarkerFileIdentity(file), "", true)
		if err != nil || claimed || !wait {
			return claim, claimed, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return FetchClaim{}, false, ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, 500*time.Millisecond)
	}
}

func (s *PopulationService) checkIdentity(ctx context.Context, file *models.MediaFile, identity string) (*models.MediaFile, error) {
	if err := ctx.Err(); err != nil {
		return file, err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return file, context.DeadlineExceeded
	}
	if s.opts.RefreshProviders != nil {
		if err := s.opts.RefreshProviders(ctx); err != nil {
			return file, err
		}
	}
	current := file
	if s.opts.LoadFile != nil {
		loaded, err := s.opts.LoadFile(ctx, file.ID)
		if err != nil {
			return file, err
		}
		if loaded == nil {
			return file, errors.New("marker file no longer exists")
		}
		current = loaded
	}
	ids, err := s.opts.Resolver.ResolveForFile(ctx, current)
	if err != nil {
		return current, err
	}
	if populationIdentity(current, ids, s.opts.Registry.fetchEntries()) != identity {
		return current, errors.New("marker file identity changed during lookup")
	}
	return current, nil
}

func (s *PopulationService) complete(ctx context.Context, claim FetchClaim, result FetchCompletion) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.opts.Store.Complete(cleanup, claim, result)
}

func populationIdentity(file *models.MediaFile, ids ExternalIDs, entries []fetchEntry) string {
	revisions := make([][2]string, 0, len(entries))
	for _, entry := range entries {
		revisions = append(revisions, [2]string{entry.provider.ID(), providerRevision(entry.provider)})
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i][0] < revisions[j][0] })
	payload, _ := json.Marshal(struct {
		File      string
		IDs       ExternalIDs
		Providers [][2]string
	}{models.MarkerFileIdentity(file), ids, revisions})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func providerRevision(provider Provider) string {
	if versioned, ok := provider.(interface{ CacheRevision() string }); ok {
		return versioned.CacheRevision()
	}
	return ""
}

func fetchRetry(failures int) time.Duration {
	if failures > 6 {
		failures = 6
	}
	if failures < 0 {
		failures = 0
	}
	retry := time.Minute * time.Duration(1<<failures)
	if retry > time.Hour {
		return time.Hour
	}
	return retry
}

func (s *PopulationService) cachedMemory(key string) (Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.memory[key]
	if !ok {
		return Result{}, false
	}
	if !time.Now().Before(entry.expires) {
		delete(s.memory, key)
		return Result{}, false
	}
	return entry.result, true
}

func (s *PopulationService) remember(key string, result Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.memory) >= markerMemoryLimit {
		var oldest string
		var expires time.Time
		for k, v := range s.memory {
			if oldest == "" || v.expires.Before(expires) {
				oldest = k
				expires = v.expires
			}
		}
		delete(s.memory, oldest)
	}
	s.memory[key] = cachedMarkerResult{result: result, expires: time.Now().Add(markerMemoryTTL)}
}

type SyncSummary struct {
	Considered int  `json:"considered"`
	Updated    int  `json:"updated"`
	Failed     int  `json:"failed"`
	Skipped    bool `json:"skipped,omitempty"`
}

func (s *PopulationService) Sync(ctx context.Context, progress func(float64, string)) (SyncSummary, error) {
	summary := SyncSummary{}
	enabled, err := s.enabled(ctx)
	if err != nil {
		return summary, err
	}
	storage, err := s.OnlineStorage(ctx)
	if err != nil {
		return summary, err
	}
	if !enabled || storage != OnlineStorageStored || s.opts.LoadFile == nil {
		summary.Skipped = true
		if progress != nil {
			progress(100, "Online marker sync is disabled")
		}
		return summary, nil
	}
	if s.opts.RefreshProviders != nil {
		if err := s.opts.RefreshProviders(ctx); err != nil {
			return summary, err
		}
	}
	entries := s.opts.Registry.fetchEntries()
	providers := make(map[string]string, len(entries))
	for _, entry := range entries {
		providers[entry.provider.ID()] = providerRevision(entry.provider)
	}
	if len(providers) == 0 {
		summary.Skipped = true
		if progress != nil {
			progress(100, "No online marker providers are enabled")
		}
		return summary, nil
	}
	for _, unqueried := range []bool{true, false} {
		afterID := 0
		for {
			if err := ctx.Err(); err != nil {
				return summary, err
			}
			ids, err := s.opts.Store.Candidates(ctx, providers, afterID, 100, unqueried)
			if err != nil {
				return summary, err
			}
			if len(ids) == 0 {
				break
			}
			for _, id := range ids {
				if err := ctx.Err(); err != nil {
					return summary, err
				}
				afterID = id
				summary.Considered++
				file, err := s.opts.LoadFile(ctx, id)
				if err != nil {
					summary.Failed++
					continue
				}
				_, changed, err := s.populate(ctx, file, false, false)
				if err != nil {
					summary.Failed++
				}
				if changed {
					summary.Updated++
				}
				if progress != nil {
					progress(0, fmt.Sprintf("Checked %d files; updated %d", summary.Considered, summary.Updated))
				}
			}
		}
	}
	if progress != nil {
		progress(100, fmt.Sprintf("Updated %d files; %d failed", summary.Updated, summary.Failed))
	}
	return summary, nil
}
