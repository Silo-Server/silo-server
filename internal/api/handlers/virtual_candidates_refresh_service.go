package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// virtualCandidatesRefreshTimeout bounds one source's provider re-list so a
// hung provider cannot hold the request (or a coalesced waiter) forever.
const virtualCandidatesRefreshTimeout = 30 * time.Second

// ErrVirtualRefreshProvider marks a provider-side refresh failure: the caller
// answers a retryable dependency problem and never touches the persisted rows.
var ErrVirtualRefreshProvider = errors.New("virtual candidate provider refresh failed")

// errVirtualNoCandidates marks a provider listing that completed successfully
// with zero candidates. It is a valid empty answer, not a provider failure:
// discovery answers it as success and leaves the stored rows untouched. It is
// wrapped in ErrVirtualRefreshProvider so the refresh surfaces keep their
// existing retryable-error contract.
var errVirtualNoCandidates = errors.New("provider returned no candidates")

// refreshEmbeddedURLPattern matches request URLs embedded in transport error
// text (a *url.Error renders as `Get "https://host/path?query": ...`). Provider
// URLs may carry credentials or tokens, so they are redacted from any message
// a client can read; only the bare host is ever reported (see
// refreshProviderCause).
var refreshEmbeddedURLPattern = regexp.MustCompile(`https?://[^\s"']+`)

// providerUnavailableError converts a failed provider listing into the
// retryable 503 the refresh surface answers, carrying the underlying cause in
// the message. A bare "the provider could not be reached" cannot distinguish
// a dead endpoint from a slow one, a rate limit, an ID-translation failure,
// or an empty answer — and the async job persists exactly this message, so
// the version-list banner can only show what is kept here. Discovery does not
// reach this mapping for an empty answer: it treats that as a successful empty
// result, while an explicit refresh keeps the retryable answer.
func providerUnavailableError(err error) error {
	if errors.Is(err, errVirtualNoCandidates) {
		return apiError(http.StatusServiceUnavailable, "unavailable", "The provider answered but listed no candidates; try again.")
	}
	if cause := refreshProviderCause(err); cause != "" {
		return apiError(http.StatusServiceUnavailable, "unavailable", "The provider could not be reached ("+cause+"); try again.")
	}
	return apiError(http.StatusServiceUnavailable, "unavailable", "The provider could not be reached; try again.")
}

// refreshProviderCause extracts a user-safe cause from a failed provider
// listing. Inner errors are codebase-generated and URL-free by convention,
// except transport errors (*url.Error) which embed the request URL: those are
// reduced to host plus root cause. It returns "" when there is nothing useful
// to add.
func refreshProviderCause(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		root := urlErr.Err
		seen := map[error]bool{}
		for root != nil && !seen[root] {
			seen[root] = true
			next := errors.Unwrap(root)
			if next == nil {
				break
			}
			root = next
		}
		host := strings.TrimSpace(urlErr.URL)
		if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil && parsed.Hostname() != "" {
			host = parsed.Hostname()
		}
		if host == "" {
			host = "the provider"
		}
		if root == nil {
			return host
		}
		return host + ": " + root.Error()
	}
	msg := err.Error()
	msg = strings.TrimSpace(strings.TrimPrefix(msg, ErrVirtualRefreshProvider.Error()+": "))
	msg = strings.TrimSpace(strings.TrimPrefix(msg, ErrVirtualRefreshProvider.Error()))
	if msg == "" {
		return ""
	}
	return refreshEmbeddedURLPattern.ReplaceAllString(msg, "the provider")
}

// VirtualCandidateFiles resolves an item's media files by content or episode
// id. Either may be nil when the store cannot answer for that kind.
type VirtualCandidateFiles func(ctx context.Context, id string) ([]*models.MediaFile, error)

// VirtualCandidatesDetailReader is the watch-detail read the version-list
// surface uses; *ItemsHandler implements it.
type VirtualCandidatesDetailReader interface {
	WatchDetail(ctx context.Context, userID int, profileID, contentID string, filter catalog.AccessFilter) (*catalog.WatchDetail, error)
}

// VirtualDiscoveryMode says why an item's provider candidates are being
// listed, and therefore whether the listing may start a background
// preparation probe.
//
// The mode is the enforcement boundary, not a log attribute. Only a
// detail-page read may spend provider traffic on a preparation: the explicit
// discover operation is callable for any item by any client, so it lists and
// persists candidates and prepares nothing.
type VirtualDiscoveryMode string

const (
	// VirtualDiscoveryListingOnly lists and persists candidates without
	// starting any preparation probe.
	VirtualDiscoveryListingOnly VirtualDiscoveryMode = "listing_only"
	// VirtualDiscoveryDetail is a detail-page read: its listing may start the
	// pre-play preparation probe for the item's default version.
	VirtualDiscoveryDetail VirtualDiscoveryMode = "detail"
)

// VirtualCandidatesRefreshService is the "Refresh List" action: it force-lists
// a virtual item's provider candidates and persists them through the existing
// ReplaceVirtualCandidates path, preserving every known-working row, then
// answers the post-refresh version list in the watch-detail shape.
//
// The provider work is coalesced per source group, so concurrent refreshes of
// the same title share one provider round-trip instead of storming it. A
// provider failure is retryable and never destructive: the persisted rows are
// only replaced by a successful fresh listing.
type VirtualCandidatesRefreshService struct {
	ListCached VirtualPlaybackStreamLister
	// ListFresh force-lists provider candidates for one virtual source path.
	ListFresh VirtualPlaybackStreamLister
	// Persist replaces the stored candidates for one source row; it is the same
	// ReplaceVirtualCandidates sink the playback path uses.
	Persist VirtualPlaybackStreamSink
	// ContentFiles / EpisodeFiles resolve the item's media files so the virtual
	// source rows can be found. Nil disables that lookup.
	ContentFiles VirtualCandidateFiles
	EpisodeFiles VirtualCandidateFiles
	// Detail reads the pre- and post-refresh watch detail, which also enforces
	// access and produces the version wire shape.
	Detail VirtualCandidatesDetailReader
	// ScheduleDefaultFileProbe, when non-nil, is called from inside the shared
	// listing work immediately after a discovery run successfully persists one
	// source group (never for :refresh runs, never for a listing-only
	// discovery). The hand-off is the successful persistence itself, not the
	// request's return: the request-side caller can time out, disconnect, or
	// release its budget before the winner lands the listing, while the
	// detached work — and the chain scheduled from it — continues. The
	// callback must be non-blocking: admission (an atomic check) only; the
	// chain's own reads and probing happen in admitted work.
	// The access filter is the one that authorized the discovery run (the
	// singleflight winner's, when callers coalesce), so the chain resolves the
	// same visible default the discovery response offered.
	// PlaybackHandler.ScheduleDefaultFileProbe implements this.
	ScheduleDefaultFileProbe func(contentID string, source *models.MediaFile, streams []VirtualPlaybackStream, filter catalog.AccessFilter)
	// SeedListing, when non-nil, is called from inside the shared work for
	// every source group whose provider listing succeeded, with the listing
	// this run already paid for. The playback start that follows reuses it
	// instead of listing the provider a second time for the same answer (see
	// PlaybackHandler.SeedVirtualListing). It runs for discovery, refresh, and
	// the async job alike: whichever path paid for the listing, the next start
	// should not pay for it again. It must be cheap and non-blocking — a cache
	// write, no provider traffic and no catalog write.
	SeedListing func(source *models.MediaFile, streams []VirtualPlaybackStream)
	// ScheduleDefaultFileWarm, when non-nil, is called after a detail-page
	// discovery resolves the item's default version, so the provider-side fetch
	// for the row a play would use starts before the play press. It is the
	// explicit-selection warm-up entry point with the resolved default file id:
	// the same fill, admission key, slots, short damper and no-state-write rule.
	// It is scheduled only for VirtualDiscoveryDetail without an explicit
	// selection; a selection read is already owned by the watch-detail select
	// path, and refresh/listing-only runs warm nothing.
	// PlaybackHandler.ScheduleDefaultFileWarm implements this.
	ScheduleDefaultFileWarm func(contentID string, fileID int, filter catalog.AccessFilter)
	// Events, when non-nil, publishes the versions_updated catalog event after a
	// detail-page discovery persists a listing. Discovery completes detached
	// from the request that started it (catalog detail does not wait), so
	// without this publish an open detail page would keep showing the version
	// list it happened to read until the client reloaded. The first-playback
	// sink and the refresh executor publish at the same point for their own
	// paths; :refresh keeps publishing only from the executor's completion, so
	// this field never doubles that event.
	Events *notifications.Hub

	flight      singleflight.Group
	sourceLocks [64]sync.Mutex
}

// RefreshVirtualCandidates re-lists and persists the item's virtual candidates
// and returns the retained list. It is the handler behind
// POST /api/v2/media/{media_id}/virtual-candidates:refresh.
// virtualURIScheme is the URI scheme of persisted virtual candidate rows.
const virtualURIScheme = "virtual"

func (s *VirtualCandidatesRefreshService) RefreshVirtualCandidates(ctx context.Context, userID int, profileID, contentID string, filter catalog.AccessFilter) ([]catalog.FileVersion, error) {
	if s == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Virtual candidate refresh is unavailable")
	}
	// A refresh runs under operator intent: it force-lists and persists, and
	// prepares nothing.
	return s.listVirtualCandidates(ctx, userID, profileID, contentID, filter, s.ListFresh, "refresh:", VirtualDiscoveryListingOnly)
}

// DiscoverVirtualCandidates lists the item's provider candidates and persists
// them. The mode decides whether the persisted listing may start a
// preparation probe: a detail-page read may, a bare listing request may not.
func (s *VirtualCandidatesRefreshService) DiscoverVirtualCandidates(ctx context.Context, userID int, profileID, contentID string, filter catalog.AccessFilter, mode VirtualDiscoveryMode) ([]catalog.FileVersion, error) {
	if s == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Virtual candidate discovery is unavailable")
	}
	return s.listVirtualCandidates(ctx, userID, profileID, contentID, filter, s.ListCached, "discover:", mode)
}

// HasVirtualSource reports whether the item holds at least one persisted
// virtual candidate group. It reads the same server-side rows as discovery,
// independent of any viewer redaction: catalog detail responses strip file
// paths for most viewers, so a caller that redacted versions cannot decide
// eligibility from them. The caller must already have authorized the viewer's
// access to the item; this lookup is content-keyed and carries no filtering.
func (s *VirtualCandidatesRefreshService) HasVirtualSource(ctx context.Context, contentID string) (bool, error) {
	if s == nil {
		return false, nil
	}
	sources, err := s.virtualSources(ctx, contentID)
	if err != nil {
		return false, err
	}
	return len(sources) > 0, nil
}

// lockSource serializes the provider list and its persistence for one source.
// A listing that starts before a detail run must not commit its candidates
// after the detail run has persisted and scheduled its preparation.
func (s *VirtualCandidatesRefreshService) lockSource(key string) func() {
	var hash uint32
	for i := 0; i < len(key); i++ {
		hash = hash*16777619 ^ uint32(key[i])
	}
	mu := &s.sourceLocks[hash%uint32(len(s.sourceLocks))]
	mu.Lock()
	return mu.Unlock
}

func (s *VirtualCandidatesRefreshService) listVirtualCandidates(ctx context.Context, userID int, profileID, contentID string, filter catalog.AccessFilter, lister VirtualPlaybackStreamLister, keyPrefix string, mode VirtualDiscoveryMode) ([]catalog.FileVersion, error) {
	if lister == nil || s.Persist == nil || s.Detail == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Virtual candidate refresh is unavailable")
	}
	// The pre-refresh read validates the target and the caller's access through
	// the same path the version list uses.
	before, err := s.Detail.WatchDetail(ctx, userID, profileID, contentID, filter)
	if err != nil {
		return nil, err
	}
	if keyPrefix == "discover:" && (before == nil || (before.Type != itemTypeMovie && before.Type != itemTypeEpisode)) {
		return nil, apiError(http.StatusUnprocessableEntity, "validation_failed", "Select a movie or an individual episode to discover versions.")
	}
	sources, err := s.virtualSources(ctx, contentID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load virtual candidates")
	}
	if len(sources) == 0 {
		return nil, apiError(http.StatusUnprocessableEntity, "validation_failed", "The item has no virtual candidates to refresh.")
	}
	// Only a detail-page read may schedule the pre-play probe chain. The
	// ":refresh" job path force-lists under operator intent and carries its own
	// enrichers; a chain on top would double-probe rows that path already
	// targets. A bare listing request is callable for any item by any client,
	// so it prepares nothing either.
	var chainKey string
	if mode == VirtualDiscoveryDetail {
		chainKey = contentID
	}
	sawCandidateListing := false
	for _, source := range sources {
		// The mode is part of the flight key. A listing-only run and a
		// detail run are different work: coalescing them would let the
		// winner's intent decide the other's preparation, so a listing-only
		// winner would swallow a detail caller's preparation while a detail
		// winner would prepare for a caller that only asked for a listing.
		key := keyPrefix + string(mode) + "\x00" + virtualSourceRefreshKey(source)
		// Do not bind the shared work to the first caller's context: a client
		// disconnect must not cancel the provider re-list (or strand a
		// coalesced waiter on a canceled context). The shared work runs under
		// the request context detached from cancellation (but with its own
		// timeout, applied inside refreshSource); waiters still observe their
		// own cancellation while waiting via the select below.
		workCtx := context.WithoutCancel(ctx)
		refreshCh := s.flight.DoChan(key, func() (any, error) {
			releaseSource := s.lockSource(virtualSourceRefreshKey(source))
			defer releaseSource()
			_, err := s.listSource(workCtx, source, userID, profileID, lister, chainKey, filter)
			return nil, err
		})
		var refreshRes singleflight.Result
		select {
		case <-ctx.Done():
			// This waiter is going away; the shared work continues for the
			// other waiters under its detached context. Report the waiter's
			// cancellation, not the work's outcome.
			return nil, apiError(http.StatusServiceUnavailable, "unavailable", "The refresh was interrupted; try again.")
		case refreshRes = <-refreshCh:
		}
		if refreshRes.Err != nil {
			if keyPrefix == "discover:" && errors.Is(refreshRes.Err, errVirtualNoCandidates) {
				// The provider answered with zero candidates. Discovery treats
				// that as a valid empty result rather than a dependency
				// failure; the listing was not persisted, so stored rows stay
				// untouched.
				continue
			}
			if errors.Is(refreshRes.Err, ErrVirtualRefreshProvider) {
				return nil, providerUnavailableError(refreshRes.Err)
			}
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to refresh virtual candidates")
		}
		sawCandidateListing = true
		// A coalesced waiter reuses the winner's persistence; nothing more to
		// do for this source.
		_ = refreshRes.Shared
	}
	if keyPrefix == "discover:" && !sawCandidateListing {
		// Every provider source answered with an empty listing. Report the
		// true empty result instead of re-reading a detail whose versions were
		// never this discovery's answer.
		return []catalog.FileVersion{}, nil
	}
	detail, err := s.Detail.WatchDetail(ctx, userID, profileID, contentID, filter)
	if err != nil {
		return nil, err
	}
	// A detail read that resolved the default version should start the same
	// provider fetch a play would, not wait for the play press. The file id is
	// the top-sorted row the default-probe target resolves, so the warm uses
	// the explicit-selection entry point unchanged: same fill, admission key,
	// slots, damper and no-state-write. An explicit selection is already owned
	// by the watch-detail select path, so this never schedules a second warm;
	// listing-only and refresh runs never reach this branch at all.
	if mode == VirtualDiscoveryDetail && filter.SelectedFileID <= 0 && s.ScheduleDefaultFileWarm != nil {
		if fileID, ok := defaultVirtualWarmFileID(detail); ok {
			s.ScheduleDefaultFileWarm(contentID, fileID, filter)
		}
	}
	return detail.Versions, nil
}

// defaultVirtualWarmFileID returns the top-sorted virtual version a detail read
// resolved as its default, the same row the default probe chain targets. A
// local row has no provider fetch to warm, and a detail with no versions has no
// resolved default, so both report false.
func defaultVirtualWarmFileID(detail *catalog.WatchDetail) (int, bool) {
	if detail == nil || len(detail.Versions) == 0 {
		return 0, false
	}
	version := detail.Versions[0]
	if version.FileID <= 0 {
		return 0, false
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(version.FilePath)), virtualPlaybackPrefix) {
		return 0, false
	}
	return version.FileID, true
}

// RefreshSources force-lists and persists every virtual source of the item
// without a watch-detail read. It is the asynchronous job's provider step: the
// caller already enforced access when it accepted the job. A nil/empty source
// set returns an error so the job fails rather than silently doing nothing.
//
// It returns the union of the freshly listed provider streams so the job can
// dedup the indexer search against the releases the provider already has and
// target enrichment at the candidates that were just persisted.
func (s *VirtualCandidatesRefreshService) RefreshSources(ctx context.Context, contentID string, userID int, profileID string) ([]*models.MediaFile, []VirtualPlaybackStream, error) {
	return s.refreshSources(ctx, contentID, userID, profileID, false)
}

// RefreshSourcesAllowEmpty is RefreshSources for the asynchronous refresh job.
// A fileless episode can still be searched on the indexers even when it has no
// provider source row yet; the job must not abort before that search. The
// caller treats an empty source list as a successful provider result.
func (s *VirtualCandidatesRefreshService) RefreshSourcesAllowEmpty(ctx context.Context, contentID string, userID int, profileID string) ([]*models.MediaFile, []VirtualPlaybackStream, error) {
	return s.refreshSources(ctx, contentID, userID, profileID, true)
}

func (s *VirtualCandidatesRefreshService) refreshSources(ctx context.Context, contentID string, userID int, profileID string, allowEmpty bool) ([]*models.MediaFile, []VirtualPlaybackStream, error) {
	if s == nil || s.ListFresh == nil || s.Persist == nil {
		return nil, nil, apiError(http.StatusServiceUnavailable, "unavailable", "Virtual candidate refresh is unavailable")
	}
	sources, err := s.virtualSources(ctx, contentID)
	if err != nil {
		return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load virtual candidates")
	}
	if len(sources) == 0 {
		if allowEmpty {
			return nil, nil, nil
		}
		return nil, nil, apiError(http.StatusUnprocessableEntity, "validation_failed", "The item has no virtual candidates to refresh.")
	}
	var streams []VirtualPlaybackStream
	for _, source := range sources {
		key := virtualSourceRefreshKey(source)
		// Do not bind the shared work to the caller's context: a client
		// disconnect (or a job cancellation during a coalesced call) must not
		// cancel a provider re-list another caller is waiting on. The shared
		// work runs detached with its own timeout applied inside refreshSource.
		workCtx := context.WithoutCancel(ctx)
		refreshCh := s.flight.DoChan(key, func() (any, error) {
			releaseSource := s.lockSource(virtualSourceRefreshKey(source))
			defer releaseSource()
			fresh, err := s.refreshSource(workCtx, source, userID, profileID)
			if err != nil {
				return nil, err
			}
			return fresh, nil
		})
		var refreshRes singleflight.Result
		select {
		case <-ctx.Done():
			// This waiter is going away; the shared work continues for the
			// other waiters under its detached context. Report the waiter's
			// cancellation, not the work's outcome.
			return nil, nil, apiError(http.StatusServiceUnavailable, "unavailable", "The refresh was interrupted; try again.")
		case refreshRes = <-refreshCh:
		}
		if refreshRes.Err != nil {
			if errors.Is(refreshRes.Err, ErrVirtualRefreshProvider) {
				// The job treats a provider re-list failure as fatal: the
				// version list cannot be trusted if the provider was not
				// reachable, and the executor documents this degradation.
				return nil, nil, providerUnavailableError(refreshRes.Err)
			}
			return nil, nil, refreshRes.Err
		}
		// Singleflight delivers the shared value to the winner and to every
		// coalesced waiter alike, so the caller appends the same listing it
		// would have produced alone.
		if fresh, ok := refreshRes.Val.([]VirtualPlaybackStream); ok {
			streams = append(streams, fresh...)
		}
	}
	return sources, streams, nil
}

// refreshSource force-lists one virtual source group and persists the result,
// returning the persisted streams so the caller can reuse the listing. The
// empty chain key schedules nothing: refresh runs target rows through their
// own job machinery.
func (s *VirtualCandidatesRefreshService) refreshSource(ctx context.Context, source *models.MediaFile, userID int, profileID string) ([]VirtualPlaybackStream, error) {
	return s.listSource(ctx, source, userID, profileID, s.ListFresh, "", catalog.AccessFilter{})
}

func (s *VirtualCandidatesRefreshService) listSource(ctx context.Context, source *models.MediaFile, userID int, profileID string, lister VirtualPlaybackStreamLister, chainKey string, filter catalog.AccessFilter) ([]VirtualPlaybackStream, error) {
	if source == nil {
		return nil, nil
	}
	listCtx, cancel := context.WithTimeout(ctx, virtualCandidatesRefreshTimeout)
	defer cancel()
	streams, err := lister.ListVirtualPlaybackStreams(
		listCtx, source.FilePath, userID, profileID, source.VirtualOwnerInstallationID,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVirtualRefreshProvider, err)
	}
	if len(streams) == 0 {
		// A zero-count listing is a valid empty provider answer, not a
		// reachability failure. It must not reach ReplaceVirtualCandidates:
		// persisting it would sweep or rewrite healthy stored candidates.
		// Discovery treats the sentinel as an empty success; the explicit
		// refresh surfaces keep answering it as retryable.
		return nil, fmt.Errorf("%w: %w", ErrVirtualRefreshProvider, errVirtualNoCandidates)
	}
	// Persist under the listing timeout, not the detached caller context: the
	// database write must not outlive the provider budget that bounds this
	// refresh. (The detached work context only shields the re-list from a
	// waiter disconnect; it must not make persistence unbounded.)
	if err := s.Persist(listCtx, source, streams); err != nil {
		return nil, err
	}
	if chainKey != "" {
		// The persisted version list changed. Announce it from inside the
		// detached shared work so every client drops its cached list even when
		// the request that started the discovery has already been answered (and
		// possibly disconnected). This is the event the waiting watch path used
		// to make visible by reloading the detail in-request; catalog detail no
		// longer waits, so it is the only notification that path gets.
		s.publishVersionsUpdated(ctx, chainKey)
	}
	if s.SeedListing != nil {
		// Hand the listing this run just paid for to the playback start. The
		// persist above committed it, so the entry names the same candidates
		// the catalog now serves; a start that follows resolves from it without
		// a second provider round-trip. Pass a copy: the singleflight value must
		// not be aliased by a cache entry.
		listed := make([]VirtualPlaybackStream, len(streams))
		copy(listed, streams)
		s.SeedListing(source, listed)
	}
	if chainKey != "" && s.ScheduleDefaultFileProbe != nil {
		// Persistence landed: schedule the pre-play probe chain from here —
		// inside the detached shared work, not on the request side — so a
		// winner whose waiter has already disconnected still schedules it.
		// The callback only admits and snapshots; it must never delay the shared
		// answer coalesced waiters read. Pass a copy so the singleflight value
		// cannot be aliased by the chain. The filter is the winner's — for a
		// coalesced wave that is the run whose persistence this is.
		copied := make([]VirtualPlaybackStream, len(streams))
		copy(copied, streams)
		s.ScheduleDefaultFileProbe(chainKey, source, copied, filter)
	}
	return streams, nil
}

// publishVersionsUpdated announces a persisted discovery listing through the
// shared event authority the refresh executor, the liveness check, and the
// first-playback sink use, so every path emits the same event shape.
//
// The publish is detached from the listing context's cancellation but keeps
// its values, and it is bounded by its own short budget: the listing has
// already committed, so a caller disconnect must not drop the notification.
func (s *VirtualCandidatesRefreshService) publishVersionsUpdated(ctx context.Context, contentID string) {
	if s == nil || s.Events == nil || strings.TrimSpace(contentID) == "" {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), virtualCandidatePublishBudget)
	defer cancel()
	publishVirtualVersionsUpdatedEvent(publishCtx, nil, s.Events.EventsHub(), contentID)
}

// virtualSources groups the item's virtual media files into one source row per
// persisted candidate group. A provider-neutral row (no ?result=) is preferred
// so its exact file_path is preserved; a candidate-only item synthesizes the
// neutral path the group already uses.
func (s *VirtualCandidatesRefreshService) virtualSources(ctx context.Context, contentID string) ([]*models.MediaFile, error) {
	var files []*models.MediaFile
	if s.ContentFiles != nil {
		contentFiles, err := s.ContentFiles(ctx, contentID)
		if err != nil {
			return nil, err
		}
		files = append(files, contentFiles...)
	}
	if s.EpisodeFiles != nil {
		episodeFiles, err := s.EpisodeFiles(ctx, contentID)
		if err != nil {
			return nil, err
		}
		files = append(files, episodeFiles...)
	}
	byGroup := map[string]*models.MediaFile{}
	for _, file := range files {
		if !isVirtualPlaybackFile(file) {
			continue
		}
		if _, ok := virtualCandidateGroupURI(file.FilePath); !ok {
			continue
		}
		key := virtualSourceRefreshKey(file)
		existing, exists := byGroup[key]
		if !exists {
			byGroup[key] = file
			continue
		}
		if virtualResultCandidateID(existing.FilePath) != "" && virtualResultCandidateID(file.FilePath) == "" {
			byGroup[key] = file
		}
	}
	sources := make([]*models.MediaFile, 0, len(byGroup))
	for _, file := range byGroup {
		source := *file
		if virtualResultCandidateID(source.FilePath) != "" {
			if group, ok := virtualCandidateGroupURI(source.FilePath); ok {
				source.FilePath = group
			}
		}
		sources = append(sources, &source)
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].FilePath != sources[j].FilePath {
			return sources[i].FilePath < sources[j].FilePath
		}
		return sources[i].ID < sources[j].ID
	})
	return sources, nil
}

// virtualSourceRefreshKey identifies one persisted candidate group: the
// provider-neutral path plus the rows' episode, library and owner scope, the
// same tuple ReplaceVirtualCandidates replaces.
func virtualSourceRefreshKey(file *models.MediaFile) string {
	if file == nil {
		return ""
	}
	group, _ := virtualCandidateGroupURI(file.FilePath)
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", group, file.EpisodeID, file.MediaFolderID, file.VirtualOwnerInstallationID)
}

// virtualCandidateGroupURI mirrors the scanner's candidate group: the virtual
// URI without its result= pick. It is the identity the stored candidates share.
func virtualCandidateGroupURI(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != virtualURIScheme || parsed.Host == "" {
		return "", false
	}
	query := parsed.Query()
	query.Del("result")
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String(), true
}
