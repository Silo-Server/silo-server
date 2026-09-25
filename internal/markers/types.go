package markers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	settingEnabled  = "true"
	settingDisabled = "false"
)

const (
	SettingMode          = "markers.mode"
	SettingLazyPlayback  = "markers.lazy_playback"
	SettingOnlineStorage = "markers.online_storage"
)

type OnlineStorage string

const (
	OnlineStorageStored   OnlineStorage = "stored"
	OnlineStorageOnDemand OnlineStorage = "on_demand"
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
	// RefreshedProviders includes successful misses, allowing the writer to
	// remove obsolete ranges from those providers without clearing failed ones.
	RefreshedProviders []string
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
	// SourceClass/ProviderID/Algorithm apply); population sets them per marker
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

// SubmissionConflictError marks a provider refusal that cannot succeed when
// the exact same payload is retried. A changed marker produces a different
// content hash and remains eligible for a later submission.
type SubmissionConflictError struct {
	Provider   string
	HTTPStatus int
	Message    string
}

func (e *SubmissionConflictError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Provider != "" {
		return fmt.Sprintf("%s: submission conflict", e.Provider)
	}
	return "submission conflict"
}

// SubmissionInvalidError marks a provider refusal of the submitted item itself,
// such as an unknown season. Retrying the same target fails the same way until
// the provider's catalog or our metadata changes.
type SubmissionInvalidError struct {
	Provider   string
	HTTPStatus int
	Message    string
}

func (e *SubmissionInvalidError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Provider != "" {
		return fmt.Sprintf("%s: submission rejected as invalid", e.Provider)
	}
	return "submission rejected as invalid"
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

// UseConfigStore attaches the settings used for provider selection and priority.
// Without one, all registered providers participate in registration order.
func (r *Registry) UseConfigStore(store *ProviderConfigStore) {
	if r != nil {
		r.config = store
	}
}

type providerResult struct {
	entry     fetchEntry
	result    Result
	refreshed bool
}

// mergeProviderResults preserves every range from the winning provider for each
// kind. Priority selects across providers; their quality signals break ties.
func mergeProviderResults(results []providerResult) Result {
	type candidate struct {
		rank   mergeCandidate
		ranges []Marker
	}
	best := make(map[MarkerKind]candidate)
	merged := Result{SourceClass: models.MarkerSourceOnline}
	for _, fetched := range results {
		if fetched.refreshed {
			merged.RefreshedProviders = append(merged.RefreshedProviders, fetched.entry.provider.ID())
		}
		grouped := make(map[MarkerKind]candidate)
		for _, m := range fetched.result.Markers {
			if m.Kind < MarkerKindIntro || m.Kind > MarkerKindPreview || m.Start < 0 || m.End <= m.Start {
				continue
			}
			m.SourceClass = firstNonEmpty(m.SourceClass, fetched.result.SourceClass, models.MarkerSourceOnline)
			m.ProviderID = fetched.entry.provider.ID()
			m.Algorithm = firstNonEmpty(m.Algorithm, fetched.result.Algorithm)
			rank := mergeCandidate{marker: m, priority: fetched.entry.priority}
			group := grouped[m.Kind]
			if len(group.ranges) == 0 || rank.better(group.rank) {
				group.rank = rank
			}
			group.ranges = append(group.ranges, m)
			grouped[m.Kind] = group
		}
		for kind, group := range grouped {
			if old, ok := best[kind]; !ok || group.rank.better(old.rank) {
				best[kind] = group
			}
		}
	}
	for _, kind := range []MarkerKind{MarkerKindIntro, MarkerKindCredits, MarkerKindRecap, MarkerKindPreview} {
		ranges := best[kind].ranges
		sort.SliceStable(ranges, func(i, j int) bool {
			if ranges[i].Start != ranges[j].Start {
				return ranges[i].Start < ranges[j].Start
			}
			return ranges[i].End < ranges[j].End
		})
		merged.Markers = append(merged.Markers, ranges...)
	}
	return merged
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
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority < entries[j].priority
		}
		return entries[i].provider.ID() < entries[j].provider.ID()
	})
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
	case "":
		return ModeBoth
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
		if normalized != settingEnabled && normalized != settingDisabled {
			return "", fmt.Errorf("%w: %s must be true or false", ErrInvalidSetting, SettingLazyPlayback)
		}
		return normalized, nil
	case SettingOnlineStorage:
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized != string(OnlineStorageStored) && normalized != string(OnlineStorageOnDemand) {
			return "", fmt.Errorf("%w: %s must be stored or on_demand", ErrInvalidSetting, SettingOnlineStorage)
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
