package apiv2

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	opDiscoverVirtualCandidates      = "discoverVirtualCandidates"
	opRefreshVirtualCandidates       = "refreshVirtualCandidates"
	opCancelVirtualCandidatesRefresh = "cancelVirtualCandidatesRefresh"
	opRequestVirtualRelease          = "requestVirtualRelease"
)

// VirtualCandidatesRefreshService accepts the asynchronous re-list and answers
// the durable job to wait on, and cancels that re-list when the owner presses
// the locked control a second time.
type VirtualCandidatesRefreshService interface {
	CreateRefreshJob(ctx context.Context, userID int, profileID, contentID string, filter catalogpkg.AccessFilter) (*models.AdminJob, error)
	CancelRefreshJob(ctx context.Context, userID int, profileID, contentID string, filter catalogpkg.AccessFilter) (*models.AdminJob, error)
}

type VirtualCandidatesDiscoveryService interface {
	DiscoverVirtualCandidates(ctx context.Context, userID int, profileID, contentID string, filter catalogpkg.AccessFilter, mode handlers.VirtualDiscoveryMode) ([]catalogpkg.FileVersion, error)
	HasVirtualSource(ctx context.Context, contentID string) (bool, error)
}

// VirtualSelectionWarmService schedules the pre-play work for one virtual
// version a detail read surfaced: the inventory probe when the row is
// unprobed, and the provider-side fetch warm-up. *handlers.PlaybackHandler
// implements it. Nil leaves detail reads free of side effects.
type VirtualSelectionWarmService interface {
	ScheduleSelectedFileProbe(contentID string, fileID int, filter catalogpkg.AccessFilter)
	ScheduleSelectedFileWarm(contentID string, fileID int, filter catalogpkg.AccessFilter)
}

// preplayDiscoveryWatchBudget bounds version discovery started from
// GET /watch/{id}. The caller is about to play, but the discovery work is
// detached and the start path resolves its own candidate; waiting longer
// only delays the player's first request. Production provider listings
// measured 17–25 s, so the old 2 s gave up before even a mid-band listing
// landed and the start proceeded on the fast path with no fresh candidates.
// Five seconds covers the fast and mid band while keeping a cold watch read
// from turning into a multi-second stall; the long tail still degrades to
// the start path's own resolve. A var so tests can shrink the wait.
var preplayDiscoveryWatchBudget = 5 * time.Second

const (
	// preplayDiscoveryCatalogBudget bounds the detached discovery a catalog
	// leaf detail read starts. The read never waits for it: it answers from the
	// persisted version list and the detached run publishes versions_updated
	// when its listing lands, so this budget only bounds how long that provider
	// work may run. Twenty seconds admits the observed 17–25 s low band that the
	// old one-second default (and ten-second ceiling) cut off; work still runs
	// detached, so the value costs the reader nothing. Deployments tune it with
	// the admin Streaming setting virtual_library.catalog_discovery_budget_ms.
	preplayDiscoveryCatalogBudget = 20 * time.Second

	// preplayDiscoveryDetailRepeatWindow is how long a successful detail
	// discovery suppresses another list for the same viewer, item, selected
	// version and library. The player's inventory poll re-reads watch detail
	// at 2s/4s/8s/20s while a virtual probe is pending; a listing on every read
	// would rewrite all candidates and invalidate the probe's CAS snapshot.
	// Manual refresh and the explicit :discover operation do not use this memo.
	preplayDiscoveryDetailRepeatWindow = 30 * time.Second
	// preplayDiscoveryCatalogRepeatWindow is the catalog leaf read's memo
	// window. That read never selects a version (Android and other thin clients
	// pick client-side), and a remount used to miss the memo whenever the
	// selected row changed. The listing itself is item-wide, so a short
	// item-scoped window suppresses the 80–465-candidate re-enumeration a
	// remount repeats. It stays short enough that a genuinely new release shows
	// up on the next page visit.
	preplayDiscoveryCatalogRepeatWindow = 2 * time.Minute
	// preplayDiscoveryMaxRepeatWindow is the longest window any trigger uses;
	// only memo pruning reads it.
	preplayDiscoveryMaxRepeatWindow = preplayDiscoveryCatalogRepeatWindow
	// Bound the per-process memo so a busy catalog cannot grow it without
	// limit. Expired entries are swept first; if the map is still full, it is
	// cleared, which only costs one extra discovery per item.
	preplayDiscoveryDetailMemoMaxEntries = 512
)

// preplayDiscoveryRepeatWindow returns the memo window for one detail trigger.
// The catalog leaf read lives longer because it answers a browse read that
// remounts; the watch read is about to play and keeps the short
// poll-coalescing window. Both share one item-scoped memo key, so whichever
// trigger listed most recently decides freshness for the other.
func preplayDiscoveryRepeatWindow(trigger string) time.Duration {
	if trigger == "catalog_detail" {
		return preplayDiscoveryCatalogRepeatWindow
	}
	return preplayDiscoveryDetailRepeatWindow
}

const (
	// maxDetailVirtualWarmVersions caps how many virtual versions one catalog
	// detail read warms. Two matches the watch-detail prefetch cap and keeps a
	// many-version title from fanning out provider work on a browse read.
	maxDetailVirtualWarmVersions = 2
	// detailVirtualWarmRepeatWindow collapses repeated detail reads for the
	// same version. The handler's own chain admission already drops a duplicate
	// live chain; this window also covers the gap after one finishes while the
	// client is still opening or polling the page.
	detailVirtualWarmRepeatWindow = 30 * time.Second
	// detailVirtualWarmMemoMaxEntries bounds the memo so a busy catalog cannot
	// grow it without limit. Expired entries are swept first; a still-full map
	// is cleared, costing one extra warm per item.
	detailVirtualWarmMemoMaxEntries = 512
)

// preplayDiscoveryMemoKey identifies one viewer's detail read for one item.
//
// The key is item-scoped for every trigger. Discovery lists the item's provider
// candidates and persists them as the item's version rows; neither the listing
// nor the persistence depends on which row the reader currently has selected
// (filter.SelectedFileID only gates the default warm-up inside the discovery
// service). Keying the memo on the selection made every version pick and every
// picker open look like a new item to the repeat window, so the watch read ran
// another full provider listing — up to the watch budget — and rewrote every
// candidate row seconds after the item page had just done the same work. The
// per-selection work that genuinely depends on the row (inventory probe and
// provider fill warm-up) is scheduled separately from the read that carries the
// selection, so it is unaffected.
func preplayDiscoveryMemoKey(ctx context.Context, contentID string, filter catalogpkg.AccessFilter) string {
	userID := 0
	if claims := claimsFrom(ctx); claims != nil {
		userID = claims.UserID
	}
	libraryID := 0
	if filter.PresentationLibraryID != nil {
		libraryID = *filter.PresentationLibraryID
	}
	return strconv.Itoa(userID) + "\x00" + profileFrom(ctx) + "\x00" + contentID + "\x00" +
		strconv.Itoa(libraryID)
}

func (reg *Registry) preplayDiscoveryRecentlyRan(key string, window time.Duration) bool {
	if reg == nil || key == "" {
		return false
	}
	reg.preplayDiscoveryMu.Lock()
	defer reg.preplayDiscoveryMu.Unlock()
	seen, ok := reg.preplayDiscoverySeen[key]
	return ok && time.Since(seen) < window
}

func (reg *Registry) rememberPreplayDiscovery(key string) {
	if reg == nil || key == "" {
		return
	}
	now := time.Now()
	reg.preplayDiscoveryMu.Lock()
	defer reg.preplayDiscoveryMu.Unlock()
	if reg.preplayDiscoverySeen == nil {
		reg.preplayDiscoverySeen = make(map[string]time.Time)
	}
	if len(reg.preplayDiscoverySeen) >= preplayDiscoveryDetailMemoMaxEntries {
		for existing, seen := range reg.preplayDiscoverySeen {
			if now.Sub(seen) >= preplayDiscoveryMaxRepeatWindow {
				delete(reg.preplayDiscoverySeen, existing)
			}
		}
		if len(reg.preplayDiscoverySeen) >= preplayDiscoveryDetailMemoMaxEntries {
			reg.preplayDiscoverySeen = make(map[string]time.Time)
		}
	}
	// The window only decides freshness on read; retention uses the longest
	// window any trigger can ask for, so a shorter caller never evicts a live
	// catalog entry.
	reg.preplayDiscoverySeen[key] = now
}

// preplayDiscoveryCatalogBudgetFor resolves the catalog leaf detail budget:
// the configured setting when wired, otherwise the built-in default. A
// non-positive answer (unset reader, unreadable setting) falls back to the
// default rather than stretching the wait.
func (reg *Registry) preplayDiscoveryCatalogBudgetFor(ctx context.Context) time.Duration {
	if reg.deps.PreplayDiscoveryBudgetMs == nil {
		return preplayDiscoveryCatalogBudget
	}
	if ms := reg.deps.PreplayDiscoveryBudgetMs(ctx); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return preplayDiscoveryCatalogBudget
}

// beginPreplayDiscovery claims the in-flight marker for one discovery key.
// False means a listing for that key is already running and this read must not
// start a second one.
func (reg *Registry) beginPreplayDiscovery(key string) bool {
	if reg == nil || key == "" {
		return false
	}
	reg.preplayDiscoveryMu.Lock()
	defer reg.preplayDiscoveryMu.Unlock()
	if _, running := reg.preplayDiscoveryRunning[key]; running {
		return false
	}
	if reg.preplayDiscoveryRunning == nil {
		reg.preplayDiscoveryRunning = make(map[string]struct{})
	}
	reg.preplayDiscoveryRunning[key] = struct{}{}
	return true
}

func (reg *Registry) endPreplayDiscovery(key string) {
	if reg == nil || key == "" {
		return
	}
	reg.preplayDiscoveryMu.Lock()
	defer reg.preplayDiscoveryMu.Unlock()
	delete(reg.preplayDiscoveryRunning, key)
}

// detailDiscoveryAdmission is the cheap, local half of a detail read's
// pre-play discovery. It answers whether a provider listing may start and runs
// the eligibility read that also feeds the detail response's has_virtual_source
// hint; it never touches the provider.
//
// admitted is true only when the caller may run the listing; key names the
// coalescing identity for it. The caller decides whether to claim that key as
// an in-flight run (see startDetailVersionDiscovery).
func (reg *Registry) detailDiscoveryAdmission(ctx context.Context, contentID, mediaType string, filter catalogpkg.AccessFilter, trigger string) (claims *auth.Claims, hasVirtualSource bool, key string, admitted bool) {
	claims = claimsFrom(ctx)
	if claims == nil || (mediaType != themeOwnerMovie && mediaType != themeOwnerEpisode) ||
		reg.deps.VirtualCandidatesDiscovery == nil {
		return nil, false, "", false
	}
	// Eligibility reads the stored rows server-side instead of the caller's
	// (possibly path-redacted) version list, so ordinary viewers reach the
	// same discovery the watch path performs. It also feeds the detail
	// response's has_virtual_source hint, so it runs regardless of whether
	// discovery is enabled.
	virtual, err := reg.deps.VirtualCandidatesDiscovery.HasVirtualSource(ctx, contentID)
	if err != nil {
		slog.WarnContext(ctx, "pre-play version discovery skipped", "component", "apiv2", "item_id", contentID,
			"trigger", trigger, "reason", "eligibility_lookup_failed", "error", err)
		return nil, false, "", false
	}
	if !virtual {
		return nil, false, "", false
	}
	hasVirtualSource = true
	if reg.deps.PreplayDiscoveryEnabled == nil || !reg.deps.PreplayDiscoveryEnabled(ctx) {
		return nil, hasVirtualSource, "", false
	}
	key = preplayDiscoveryMemoKey(ctx, contentID, filter)
	if reg.preplayDiscoveryRecentlyRan(key, preplayDiscoveryRepeatWindow(trigger)) {
		// The player polls watch detail for inventory while the pre-play probe
		// is still running, and the item page and player read the same item with
		// different selected rows. Re-listing on either rewrites every candidate
		// and invalidates the probe write's catalog snapshot; the first read in
		// this window already listed the item-wide set.
		slog.DebugContext(ctx, "pre-play version discovery coalesced", "component", "apiv2", "item_id", contentID,
			"trigger", trigger, "selected_file_id", filter.SelectedFileID)
		return nil, hasVirtualSource, "", false
	}
	return claims, hasVirtualSource, key, true
}

// runDetailDiscovery performs one admitted provider listing for a detail read
// and records the outcome. ownsFlight marks the detached catalog run, which
// claimed the key's in-flight marker and must release it (and survive a panic
// with a log rather than taking the process down); the watch path waits on the
// discovery service's own coalescing and claims nothing. Either way memoKey is
// what a successful run records in the repeat window. The caller owns ctx: a
// bound for the waiting watch-detail path, a detached background context for
// the catalog path.
func (reg *Registry) runDetailDiscovery(ctx context.Context, claims *auth.Claims, profileID, contentID string, filter catalogpkg.AccessFilter, trigger, memoKey string, ownsFlight bool) (discovered bool) {
	if ownsFlight {
		defer reg.endPreplayDiscovery(memoKey)
		defer func() {
			// A panic in a detached listing must not take the process down: the
			// request that started it has long been answered.
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "pre-play version discovery panicked",
					"component", "apiv2", "item_id", contentID, "trigger", trigger, "panic", recovered)
			}
		}()
	}
	started := time.Now()
	slog.InfoContext(ctx, "pre-play version discovery started", "component", "apiv2", "item_id", contentID, "trigger", trigger)
	listed, listErr := reg.deps.VirtualCandidatesDiscovery.DiscoverVirtualCandidates(
		ctx, claims.UserID, profileID, contentID, filter,
		handlers.VirtualDiscoveryDetail,
	)
	if listErr != nil {
		slog.WarnContext(ctx, "pre-play version discovery unavailable", "component", "apiv2", "item_id", contentID,
			"trigger", trigger, "duration_ms", time.Since(started).Milliseconds())
		return false
	}
	slog.InfoContext(ctx, "pre-play version discovery completed", "component", "apiv2", "item_id", contentID,
		"trigger", trigger, "count", len(listed), "duration_ms", time.Since(started).Milliseconds())
	reg.rememberPreplayDiscovery(memoKey)
	return true
}

// startDetailVersionDiscovery starts the candidate discovery a detail-page
// read needs without making that read wait for it, and reports the
// has_virtual_source hint. The response is built from the persisted version
// list; the detached run publishes the existing versions_updated catalog event
// when its listing lands (see VirtualCandidatesRefreshService.listSource), so
// an open page still refreshes without pricing the provider round-trip into
// navigation.
//
// Only the catalog leaf detail read uses this. Watch detail still waits its
// short pre-play budget because the caller is about to start playback, and the
// start path's own candidate resolution depends on that fresh listing.
func (reg *Registry) startDetailVersionDiscovery(ctx context.Context, contentID, mediaType string, filter catalogpkg.AccessFilter, trigger string, budget time.Duration) (hasVirtualSource bool) {
	claims, hasVirtualSource, key, admitted := reg.detailDiscoveryAdmission(ctx, contentID, mediaType, filter, trigger)
	if !admitted {
		return hasVirtualSource
	}
	if !reg.beginPreplayDiscovery(key) {
		// A listing an earlier detail read started is still running. The rows it
		// writes are announced by that run's versions_updated event, so this
		// read neither waits for nor duplicates the provider work.
		slog.DebugContext(ctx, "pre-play version discovery already running", "component", "apiv2", "item_id", contentID,
			"trigger", trigger, "selected_file_id", filter.SelectedFileID)
		return hasVirtualSource
	}
	profileID := profileFrom(ctx)
	// Detach from the request: the client gets its answer now, and a
	// disconnect must not cancel the listing. The detached run keeps the
	// caller's values (its access scope and identity) but not its cancellation.
	detached := context.WithoutCancel(ctx)
	go func() {
		discoveryCtx, cancel := context.WithTimeout(detached, budget)
		defer cancel()
		reg.runDetailDiscovery(discoveryCtx, claims, profileID, contentID, filter, trigger, key, true)
	}()
	return hasVirtualSource
}

// discoverDetailVersions runs the candidate discovery a detail-page read needs
// and waits for it, bounded by budget, and reports whether it found any. Watch
// detail uses this: the caller is about to play, so it prices a short listing
// into the answer. Catalog detail uses startDetailVersionDiscovery instead and
// never waits.
func (reg *Registry) discoverDetailVersions(ctx context.Context, contentID, mediaType string, filter catalogpkg.AccessFilter, trigger string, budget time.Duration) (discovered, hasVirtualSource bool) {
	claims, hasVirtualSource, key, admitted := reg.detailDiscoveryAdmission(ctx, contentID, mediaType, filter, trigger)
	if !admitted {
		return false, hasVirtualSource
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	// The watch path deliberately does not claim the in-flight marker: it waits
	// on the discovery service's own coalescing, so a listing another request
	// already started is joined rather than skipped. key names the memo entry
	// the run records on success.
	return reg.runDetailDiscovery(discoveryCtx, claims, profileFrom(ctx), contentID, filter, trigger, key, false), true
}

// warmDetailVirtualVersions starts the pre-play probe/warm chain for the
// virtual versions a catalog leaf detail read surfaced. Android and other thin
// clients open the item page through getCatalogItem and pick a version
// client-side; they never call the versions check or a select endpoint, so
// this read is the only server-visible signal that a version is about to play.
func (reg *Registry) warmDetailVirtualVersions(ctx context.Context, detail *catalogpkg.ItemDetail, filter catalogpkg.AccessFilter) {
	if detail == nil {
		return
	}
	reg.warmVirtualVersions(ctx, detail.ContentID, detail.Type, detail.Versions, filter)
}

// warmWatchDetailVirtualVersions starts the same chain for the virtual versions
// GET /watch/{id} surfaced. Native clients open an episode straight from its
// watch detail and pick a version client-side without declaring file_id, so the
// selected-file hook (scheduleSelectedVirtualProbe) has nothing to warm and
// this read must cover the listed rows instead.
func (reg *Registry) warmWatchDetailVirtualVersions(ctx context.Context, detail *catalogpkg.WatchDetail, filter catalogpkg.AccessFilter) {
	if detail == nil {
		return
	}
	reg.warmVirtualVersions(ctx, detail.ContentID, detail.Type, detail.Versions, filter)
}

// warmVirtualVersions is the shared warm for both detail reads: the catalog
// leaf read and the watch detail read see the same projected rows and must
// schedule the same work.
//
// The work is bounded to movies and individual episodes (a series or season
// page carries no playable row), to maxDetailVirtualWarmVersions per read, and
// to one schedule per version inside detailVirtualWarmRepeatWindow. Scheduling
// is non-blocking: ScheduleSelectedFileProbe/Warm only take an admission slot
// and spawn detached work under their own budgets, and both refuse a duplicate
// live chain. A failed or durably unavailable row is skipped. This path only
// warms: it never writes a failure verdict, never stamps failed_at, and never
// fails the detail request.
func (reg *Registry) warmVirtualVersions(ctx context.Context, contentID, itemType string, versions []catalogpkg.FileVersion, filter catalogpkg.AccessFilter) {
	if reg == nil || reg.deps.VirtualSelectionWarm == nil {
		return
	}
	if itemType != themeOwnerMovie && itemType != themeOwnerEpisode {
		return
	}
	scheduler := reg.deps.VirtualSelectionWarm
	warmed := 0
	for _, version := range versions {
		if warmed >= maxDetailVirtualWarmVersions {
			break
		}
		if version.FileID <= 0 || version.Failed || (version.Available != nil && !*version.Available) {
			continue
		}
		if !isVirtualVersion(version) {
			continue
		}
		// The cap counts candidate positions, not admissions, so a repeated
		// read resolves to the same two rows instead of drifting to the next
		// pair once the first pair is memoized.
		warmed++
		if !reg.rememberDetailVirtualWarm(contentID, version.FileID) {
			continue
		}
		// An unprobed row needs the inventory probe for its track menus; a
		// probed row still needs the warm because probe evidence says nothing
		// about whether the provider has fetched the bytes.
		if version.ProbedAt == nil {
			scheduler.ScheduleSelectedFileProbe(contentID, version.FileID, filter)
		}
		scheduler.ScheduleSelectedFileWarm(contentID, version.FileID, filter)
	}
}

// isVirtualVersion reports whether a projected version is provider-backed.
// The path prefix and the container sentinel are the two markers the catalog
// row uses; either is enough.
func isVirtualVersion(version catalogpkg.FileVersion) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(version.FilePath)), "virtual://") ||
		strings.EqualFold(strings.TrimSpace(version.Container), "virtual")
}

// rememberDetailVirtualWarm records a scheduled version and reports whether it
// was new. A repeat inside detailVirtualWarmRepeatWindow returns false so the
// caller skips scheduling. The memo is node-local in-process state with no
// shared admission; it only suppresses duplicate scheduling.
func (reg *Registry) rememberDetailVirtualWarm(contentID string, fileID int) bool {
	if reg == nil || contentID == "" || fileID <= 0 {
		return false
	}
	key := contentID + "\x00" + strconv.Itoa(fileID)
	now := time.Now()
	reg.virtualWarmMu.Lock()
	defer reg.virtualWarmMu.Unlock()
	if reg.virtualWarmSeen == nil {
		reg.virtualWarmSeen = make(map[string]time.Time)
	}
	if seen, ok := reg.virtualWarmSeen[key]; ok && now.Sub(seen) < detailVirtualWarmRepeatWindow {
		return false
	}
	if len(reg.virtualWarmSeen) >= detailVirtualWarmMemoMaxEntries {
		for existing, seen := range reg.virtualWarmSeen {
			if now.Sub(seen) >= detailVirtualWarmRepeatWindow {
				delete(reg.virtualWarmSeen, existing)
			}
		}
		if len(reg.virtualWarmSeen) >= detailVirtualWarmMemoMaxEntries {
			reg.virtualWarmSeen = make(map[string]time.Time)
		}
	}
	reg.virtualWarmSeen[key] = now
	return true
}

// VirtualReleaseRequestService requests one stored indexer release on the
// provider.
type VirtualReleaseRequestService interface {
	RequestIndexerRelease(ctx context.Context, userID int, profileID, contentID string, releaseID int64, filter catalogpkg.AccessFilter) (handlers.IndexerReleaseRequestResult, error)
}

// VirtualCandidatesRefreshInput names the item whose version candidates to
// re-list.
type VirtualCandidatesRefreshInput struct {
	MediaID ID `path:"media_id" doc:"A movie or episode whose virtual candidates to re-list" example:"movie:heat-1995"`
}

// VirtualReleaseRequestInput names the item and the stored release to request.
type VirtualReleaseRequestInput struct {
	MediaID   ID `path:"media_id" doc:"A movie or episode whose indexer release to request" example:"movie:heat-1995"`
	ReleaseID ID `path:"release_id" doc:"The opaque release id read from the watch detail's indexer_releases" example:"42"`
}

// VirtualReleaseRequest is the request action's answer. It carries the opaque
// release id and the resulting state; a URL is never accepted or echoed.
type VirtualReleaseRequest struct {
	ReleaseID ID     `json:"release_id"`
	State     string `json:"state" enum:"queued,failed"`
	Message   string `json:"message,omitempty"`
}

type VirtualReleaseRequestOutput struct {
	Body VirtualReleaseRequest
}

func registerMediaCandidates(reg *Registry) {
	discover := humaOp(http.MethodPost, Prefix+"/media/{media_id}/virtual-candidates:discover", opDiscoverVirtualCandidates, "watch",
		"Discover and persist cached provider versions before playback when enabled, without probing or requesting releases.")
	discover.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{
		Operation:       discover,
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
		RetrySafety:     RetrySafetyCoalescing,
	}, reg.discoverVirtualCandidates)

	op := humaOp(http.MethodPost, Prefix+"/media/{media_id}/virtual-candidates:refresh", opRefreshVirtualCandidates, "watch",
		"Queue an asynchronous provider re-list of a virtual item's version candidates and answer the job to wait on.")
	op.DefaultStatus = http.StatusAccepted
	Register(reg, Operation{
		Operation:       op,
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
		// One active job per title: a concurrent refresh returns the existing
		// job rather than starting a second provider re-list.
		RetrySafety: RetrySafetyCoalescing,
	}, reg.refreshVirtualCandidates)

	cancel := humaOp(http.MethodPost, Prefix+"/media/{media_id}/virtual-candidates:refresh/cancel", opCancelVirtualCandidatesRefresh, "watch",
		"Cancel the caller's in-flight refresh of a virtual item's version candidates. Cancellation is non-destructive to already-persisted candidates and leaves the automatic re-listing intervals untouched.")
	cancel.DefaultStatus = http.StatusOK
	cancel.Errors = []int{http.StatusConflict, http.StatusForbidden}
	Register(reg, Operation{
		Operation:       cancel,
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
		// The job state machine makes repeated cancellation requests converge
		// on the same terminal canceled state.
		RetrySafety: RetrySafetyCoalescing,
	}, reg.cancelVirtualCandidatesRefresh)

	request := humaOp(http.MethodPost, Prefix+"/media/{media_id}/virtual-releases/{release_id}:request", opRequestVirtualRelease, "watch",
		"Request a release that exists on the indexers on the provider. The stored download URL is used server-side and is never returned.")
	request.DefaultStatus = http.StatusOK
	request.Errors = []int{http.StatusNotFound}
	Register(reg, Operation{
		Operation:       request,
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
		// The stored release row is the domain identity: a second request for
		// an already-queued release returns the same state without a second
		// enqueue.
		RetrySafety: RetrySafetyDomainIdentity,
	}, reg.requestVirtualRelease)
}

func (reg *Registry) discoverVirtualCandidates(ctx context.Context, in *VirtualCandidatesRefreshInput) (*struct{}, error) {
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	if reg.deps.VirtualCandidatesDiscovery == nil || reg.deps.Watch == nil ||
		reg.deps.PreplayDiscoveryEnabled == nil || !reg.deps.PreplayDiscoveryEnabled(ctx) {
		return nil, unavailable("pre-play version discovery")
	}
	filter, err := reg.deps.Watch.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	// This operation is a listing surface: it is callable for any item by any
	// client, so it lists and persists candidates and prepares nothing. A
	// detail-page read is what may start a preparation.
	if _, err := reg.deps.VirtualCandidatesDiscovery.DiscoverVirtualCandidates(ctx, claims.UserID, profileFrom(ctx), string(in.MediaID), filter, handlers.VirtualDiscoveryListingOnly); err != nil {
		return nil, serviceProblem(err)
	}
	return &struct{}{}, nil
}

func (reg *Registry) refreshVirtualCandidates(ctx context.Context, in *VirtualCandidatesRefreshInput) (*AdminCatalogJobAcceptedOutput, error) {
	if reg.deps.VirtualCandidatesRefresh == nil || reg.deps.Watch == nil {
		return nil, unavailable("virtual candidates refresh")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	filter, err := reg.deps.Watch.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	job, err := reg.deps.VirtualCandidatesRefresh.CreateRefreshJob(ctx, claims.UserID, profileFrom(ctx), string(in.MediaID), filter)
	if err != nil {
		return nil, serviceProblem(err)
	}
	out := &AdminCatalogJobAcceptedOutput{
		Location:   Prefix + "/admin/jobs/" + job.ID,
		RetryAfter: "5",
		Body:       reg.adminTaskJobOf(ctx, job, claims.Role == models.RoleAdmin),
	}
	return out, nil
}

func (reg *Registry) cancelVirtualCandidatesRefresh(ctx context.Context, in *VirtualCandidatesRefreshInput) (*AdminCatalogJobAcceptedOutput, error) {
	if reg.deps.VirtualCandidatesRefresh == nil || reg.deps.Watch == nil {
		return nil, unavailable("virtual candidates refresh")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	filter, err := reg.deps.Watch.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	job, err := reg.deps.VirtualCandidatesRefresh.CancelRefreshJob(ctx, claims.UserID, profileFrom(ctx), string(in.MediaID), filter)
	if err != nil {
		var apiErr *handlers.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "not_cancellable" {
			return nil, NewProblem(TypeJobNotCancelable, apiErr.Message)
		}
		return nil, serviceProblem(err)
	}
	return &AdminCatalogJobAcceptedOutput{
		Location:   Prefix + "/admin/jobs/" + job.ID,
		RetryAfter: "5",
		Body:       reg.adminTaskJobOf(ctx, job, claims.Role == models.RoleAdmin),
	}, nil
}

func (reg *Registry) requestVirtualRelease(ctx context.Context, in *VirtualReleaseRequestInput) (*VirtualReleaseRequestOutput, error) {
	if reg.deps.VirtualReleaseRequest == nil || reg.deps.Watch == nil {
		return nil, unavailable("indexer release requests")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	releaseID, err := strconv.ParseInt(string(in.ReleaseID), 10, 64)
	if err != nil || releaseID <= 0 || strconv.FormatInt(releaseID, 10) != string(in.ReleaseID) {
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: "path.release_id", Code: codeInvalid, Detail: "release_id must name a release."})
	}
	filter, err := reg.deps.Watch.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	result, err := reg.deps.VirtualReleaseRequest.RequestIndexerRelease(ctx, claims.UserID, profileFrom(ctx), string(in.MediaID), releaseID, filter)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &VirtualReleaseRequestOutput{Body: VirtualReleaseRequest{ReleaseID: ID(result.ReleaseID), State: result.State, Message: result.Message}}, nil
}
