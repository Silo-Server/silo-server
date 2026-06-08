package markers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	SettingMode         = "markers.mode"
	SettingLazyPlayback = "markers.lazy_playback"
)

type Mode string

const (
	ModeOff    Mode = "off"
	ModeLocal  Mode = "local"
	ModeOnline Mode = "online"
	ModeBoth   Mode = "both"
)

var ErrInvalidSetting = errors.New("invalid marker setting")

type Provider interface {
	ID() string
	FetchMarkers(ctx context.Context, req Request) (Result, error)
}

const (
	ProviderSourceBuiltIn = "built_in"
	ProviderSourcePlugin  = "plugin"
)

// ProviderDescriptor is optional provider metadata surfaced by admin APIs.
type ProviderDescriptor struct {
	ID                   string
	DisplayName          string
	SourceType           string
	PluginID             string
	PluginInstallationID int
	CapabilityID         string
}

type DescribedProvider interface {
	ProviderDescription() ProviderDescriptor
}

// SubmissionRequirements describes IDs the generic contribution path can
// validate before calling a provider. Providers may still perform richer
// validation inside SubmitMarker.
type SubmissionRequirements struct {
	RequiredExternalIDs []string
}

type SubmissionRequirementProvider interface {
	SubmissionRequirements() SubmissionRequirements
}

type ItemKind int

const (
	ItemKindEpisode ItemKind = iota + 1
	ItemKindMovie
)

type MarkerKind int

const (
	MarkerKindIntro MarkerKind = iota + 1
	MarkerKindCredits
	MarkerKindRecap
	MarkerKindPreview
)

// Canonical keys for Request.ExternalIDs. Providers consult these so we
// don't scatter raw "tmdb"/"imdb" string literals across the codebase.
const (
	ExternalIDKeyTMDB = "tmdb"
	ExternalIDKeyIMDB = "imdb"
	ExternalIDKeyTVDB = "tvdb"
)

type Request struct {
	Kind          ItemKind
	ExternalIDs   map[string]string
	SeasonNumber  int
	EpisodeNumber int
	Duration      time.Duration
}

type Result struct {
	SourceClass string
	ProviderID  string
	Algorithm   string
	Markers     []Marker
}

type Marker struct {
	Kind            MarkerKind
	Start           time.Duration
	End             time.Duration
	Confidence      float64
	SubmissionCount int
	SourceClass     string
	// ProviderID and Algorithm identify the source of this individual marker.
	// They are usually empty for a single-provider Result (the Result-level
	// SourceClass/ProviderID/Algorithm apply); FetchMerged sets them per marker
	// so a merged result records correct per-segment provenance.
	ProviderID string
	Algorithm  string
}

// SubmissionRequest is a single-segment contribution to an online marker
// source. Start is nil when the segment begins at the start of the file
// (intro/recap); End is nil when it runs to the end (credits/preview).
type SubmissionRequest struct {
	Kind          ItemKind
	ExternalIDs   map[string]string
	SeasonNumber  int
	EpisodeNumber int
	Segment       MarkerKind
	Start         *time.Duration
	End           *time.Duration
	Duration      time.Duration
}

// SubmissionStatus values returned by a Submitter.
const (
	SubmissionStatusPending  = "pending"
	SubmissionStatusAccepted = "accepted"
	SubmissionStatusRejected = "rejected"
)

// SubmissionResult is the provider's acknowledgement of a submitted segment.
type SubmissionResult struct {
	ID     string
	Status string
	Weight float64
}

// UserStats is a contribution-account summary used to validate a key and show
// contribution totals in the admin UI.
type UserStats struct {
	Total          int
	Accepted       int
	Pending        int
	Rejected       int
	AcceptanceRate float64
	CurrentStreak  int
	BestStreak     int
}

// RetryAfterError marks provider errors that should pause contribution work
// until RetryAfter has elapsed, such as TheIntroDB usage-limit responses.
type RetryAfterError struct {
	Provider   string
	RetryAfter time.Duration
	Message    string
}

func (e *RetryAfterError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Provider != "" {
		return fmt.Sprintf("%s: retry after %s", e.Provider, e.RetryAfter)
	}
	return fmt.Sprintf("retry after %s", e.RetryAfter)
}

// RetryAfter extracts a provider backoff duration from err.
func RetryAfter(err error) (time.Duration, bool) {
	var retryErr *RetryAfterError
	if errors.As(err, &retryErr) && retryErr != nil {
		return retryErr.RetryAfter, true
	}
	return 0, false
}

// Submitter is an optional capability implemented by providers that accept
// marker contributions. The contribution service type-asserts a Provider to
// Submitter; non-contributing providers simply don't implement it.
type Submitter interface {
	Provider
	SubmitMarker(ctx context.Context, req SubmissionRequest) (SubmissionResult, error)
	FetchUserStats(ctx context.Context) (UserStats, error)
}

type Registry struct {
	mu        sync.RWMutex
	providers []Provider
	logger    *slog.Logger
	config    *ProviderConfigStore
}

func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{logger: logger}
}

func (r *Registry) Register(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("marker provider is nil")
	}
	id := strings.TrimSpace(provider.ID())
	if id == "" {
		return fmt.Errorf("marker provider ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.providers {
		if existing.ID() == id {
			return fmt.Errorf("marker provider %q already registered", id)
		}
	}
	r.providers = append(r.providers, provider)
	return nil
}

func (r *Registry) Providers() []Provider {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.providers) == 0 {
		return nil
	}
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

// SetProviders atomically replaces the registered provider set. It is used when
// plugin lifecycle changes require rebuilding the marker-provider list.
func (r *Registry) SetProviders(providers []Provider) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[string]struct{}, len(providers))
	next := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			return fmt.Errorf("marker provider is nil")
		}
		id := strings.TrimSpace(provider.ID())
		if id == "" {
			return fmt.Errorf("marker provider ID is required")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("marker provider %q already registered", id)
		}
		seen[id] = struct{}{}
		next = append(next, provider)
	}
	r.providers = next
	return nil
}

func (r *Registry) FetchFirstHit(ctx context.Context, req Request) (Result, bool, error) {
	providers := r.Providers()
	if len(providers) == 0 {
		return Result{}, false, nil
	}

	var lastErr error
	for _, provider := range providers {
		result, err := provider.FetchMarkers(ctx, req)
		if err != nil {
			lastErr = err
			r.logProviderError(provider.ID(), req, err)
			continue
		}
		if len(result.Markers) == 0 {
			continue
		}
		if strings.TrimSpace(result.ProviderID) == "" {
			result.ProviderID = provider.ID()
		}
		if strings.TrimSpace(result.SourceClass) == "" {
			result.SourceClass = models.MarkerSourceOnline
		}
		return result, true, nil
	}

	return Result{}, false, lastErr
}

// UseConfigStore attaches a per-provider config store so FetchMerged consults
// fetch_enabled / fetch_priority. Without one, all registered providers
// participate in registration order.
func (r *Registry) UseConfigStore(store *ProviderConfigStore) {
	if r != nil {
		r.config = store
	}
}

// FetchMerged queries every fetch-enabled provider concurrently and keeps, per
// segment kind, the best candidate — ranked by provider fetch priority (lower
// preferred), then submission count, then confidence, then provider id for
// deterministic output. Submission count and confidence are provider-local
// quality signals; explicit admin priority is the cross-provider winner rule.
// The winning markers are stamped with source/provider/algorithm so the write
// path records correct per-segment provenance. With a single enabled provider
// this returns the same result as FetchFirstHit.
func (r *Registry) FetchMerged(ctx context.Context, req Request) (Result, bool, error) {
	if r == nil || len(r.Providers()) == 0 {
		return Result{}, false, nil
	}

	entries := r.fetchEntries()
	if len(entries) == 0 {
		return Result{}, false, nil
	}

	type fetched struct {
		entry  fetchEntry
		result Result
		err    error
	}
	out := make([]fetched, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e fetchEntry) {
			defer wg.Done()
			res, err := e.provider.FetchMarkers(ctx, req)
			out[i] = fetched{entry: e, result: res, err: err}
		}(i, e)
	}
	wg.Wait()

	best := make(map[MarkerKind]mergeCandidate)
	var lastErr error
	for _, f := range out {
		if f.err != nil {
			lastErr = f.err
			r.logProviderError(f.entry.provider.ID(), req, f.err)
			continue
		}
		for _, m := range f.result.Markers {
			m.SourceClass = firstNonEmpty(m.SourceClass, f.result.SourceClass, models.MarkerSourceOnline)
			m.ProviderID = firstNonEmpty(m.ProviderID, f.result.ProviderID, f.entry.provider.ID())
			m.Algorithm = firstNonEmpty(m.Algorithm, f.result.Algorithm)
			cand := mergeCandidate{marker: m, priority: f.entry.priority}
			if cur, ok := best[m.Kind]; !ok || cand.better(cur) {
				best[m.Kind] = cand
			}
		}
	}
	if len(best) == 0 {
		return Result{}, false, lastErr
	}

	merged := Result{SourceClass: models.MarkerSourceOnline}
	for _, kind := range []MarkerKind{MarkerKindIntro, MarkerKindCredits, MarkerKindRecap, MarkerKindPreview} {
		if cand, ok := best[kind]; ok {
			merged.Markers = append(merged.Markers, cand.marker)
		}
	}
	return merged, true, nil
}

type fetchEntry struct {
	provider Provider
	priority int
}

// fetchEntries returns the providers to query with their priorities. When a
// config store is set only fetch-enabled providers participate, ordered by
// fetch_priority; otherwise all providers participate in registration order.
func (r *Registry) fetchEntries() []fetchEntry {
	providers := r.Providers()
	if r.config == nil {
		entries := make([]fetchEntry, 0, len(providers))
		for i, p := range providers {
			entries = append(entries, fetchEntry{provider: p, priority: i})
		}
		return entries
	}
	priority := make(map[string]int)
	for _, c := range r.config.EnabledForFetch() {
		priority[c.Provider] = c.FetchPriority
	}
	entries := make([]fetchEntry, 0, len(providers))
	for _, p := range providers {
		if prio, ok := priority[p.ID()]; ok {
			entries = append(entries, fetchEntry{provider: p, priority: prio})
		}
	}
	return entries
}

type mergeCandidate struct {
	marker   Marker
	priority int
}

// better reports whether a should win over b for the same segment kind: more
// preferred fetch priority, then more submissions, then higher confidence, then
// deterministic provider id ordering.
func (a mergeCandidate) better(b mergeCandidate) bool {
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	if a.marker.SubmissionCount != b.marker.SubmissionCount {
		return a.marker.SubmissionCount > b.marker.SubmissionCount
	}
	if a.marker.Confidence != b.marker.Confidence {
		return a.marker.Confidence > b.marker.Confidence
	}
	return a.marker.ProviderID < b.marker.ProviderID
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (r *Registry) logProviderError(providerID string, req Request, err error) {
	logger := slog.Default()
	if r != nil && r.logger != nil {
		logger = r.logger
	}
	logger.Warn("marker provider fetch failed",
		"provider", providerID,
		"kind", req.Kind,
		"external_ids", sanitizeExternalIDs(req.ExternalIDs),
		"error", err,
	)
}

func NormalizeMode(raw string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case ModeOff:
		return ModeOff
	case ModeOnline:
		return ModeOnline
	case ModeBoth:
		return ModeBoth
	case ModeLocal:
		return ModeLocal
	default:
		return ModeLocal
	}
}

func ShouldRunLocal(mode Mode) bool {
	return mode == ModeLocal || mode == ModeBoth
}

func NormalizeSetting(key, value string) (string, error) {
	switch key {
	case SettingMode:
		normalized := string(NormalizeMode(value))
		if normalized != strings.ToLower(strings.TrimSpace(value)) {
			return "", fmt.Errorf("%w: %s must be one of off, local, online, both", ErrInvalidSetting, SettingMode)
		}
		return normalized, nil
	case SettingLazyPlayback:
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized != "true" && normalized != "false" {
			return "", fmt.Errorf("%w: %s must be true or false", ErrInvalidSetting, SettingLazyPlayback)
		}
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: unsupported marker setting %s", ErrInvalidSetting, key)
	}
}

func sanitizeExternalIDs(ids map[string]string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]string, len(ids))
	for key, value := range ids {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	return out
}
