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
	// ProviderID and Algorithm identify the source of this individual marker.
	// They are usually empty for a single-provider Result (the Result-level
	// ProviderID/Algorithm apply); FetchMerged sets them per marker so a merged
	// result records correct per-segment provenance.
	ProviderID string
	Algorithm  string
}

type Registry struct {
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
	for _, existing := range r.providers {
		if existing.ID() == id {
			return fmt.Errorf("marker provider %q already registered", id)
		}
	}
	r.providers = append(r.providers, provider)
	return nil
}

func (r *Registry) Providers() []Provider {
	if r == nil || len(r.providers) == 0 {
		return nil
	}
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

func (r *Registry) FetchFirstHit(ctx context.Context, req Request) (Result, bool, error) {
	if r == nil || len(r.providers) == 0 {
		return Result{}, false, nil
	}

	var lastErr error
	for _, provider := range r.providers {
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
// segment kind, the best candidate — ranked by submission count, then
// confidence, then the provider's fetch priority (lower preferred). The winning
// markers are stamped with their provider/algorithm so the write path records
// correct per-segment provenance. With a single enabled provider this returns
// the same result as FetchFirstHit.
func (r *Registry) FetchMerged(ctx context.Context, req Request) (Result, bool, error) {
	if r == nil || len(r.providers) == 0 {
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
	if r.config == nil {
		entries := make([]fetchEntry, 0, len(r.providers))
		for i, p := range r.providers {
			entries = append(entries, fetchEntry{provider: p, priority: i})
		}
		return entries
	}
	priority := make(map[string]int)
	for _, c := range r.config.EnabledForFetch() {
		priority[c.Provider] = c.FetchPriority
	}
	entries := make([]fetchEntry, 0, len(r.providers))
	for _, p := range r.providers {
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
// submissions, then higher confidence, then lower (preferred) fetch priority.
func (a mergeCandidate) better(b mergeCandidate) bool {
	if a.marker.SubmissionCount != b.marker.SubmissionCount {
		return a.marker.SubmissionCount > b.marker.SubmissionCount
	}
	if a.marker.Confidence != b.marker.Confidence {
		return a.marker.Confidence > b.marker.Confidence
	}
	return a.priority < b.priority
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
