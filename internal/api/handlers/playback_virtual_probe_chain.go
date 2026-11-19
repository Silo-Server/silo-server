package handlers

import (
	"context"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

// The pre-play probe chain closes the gap between discovery persistence and
// the first play attempt. Discovery (the v2 catalog leaf detail path) lists a
// virtual item's provider candidates and persists them, but the persisted
// rows carry declared metadata only: ffprobe evidence — the exact stream
// table — lands the first time the version actually plays. A client that
// opens the detail page and immediately presses Play therefore pays the
// probe latency on its first start even though discovery just ran. Android
// has no background refetch of its own, so that cost always lands on the
// first open there.
//
// The chain re-reads the target playback version and requires that its bytes
// are known exactly (a persisted candidate whose URI appears in the
// just-persisted listing, or — for an explicit selection — its own persisted
// result id; a neutral group row may resolve to a sibling candidate, so it is
// never probed speculatively), then probes it through the same background
// evidence path the resolve uses (queued, coalescing write; never the
// foreground direct fallback). Two reads schedule it: discovery after
// persistence targets the resolved default, and a watch-detail read that names
// an explicit still-unprobed version targets that row, so a non-default pick
// no longer shows unknown audio/subtitle menus until its first play. Nothing
// here gates playback: the play path is untouched and never waits for the
// chain. A chain may finish its probe before a play attempt resolves the same
// row — that attempt then reuses the recorded evidence — or play may win
// first; the queued evidence write follows the ordinary persistence rules
// either way. When play wins, the two probes are not duplicated: every probe
// goes through the shared VirtualProbeCache, whose single-flight makes the
// second caller join the first one's enumeration. A playback start therefore
// never cancels a chain: canceling one threw away work the detail page had
// already paid for, then re-ran it. Every chain is optional best-effort work:
// any failure, including an unexpected panic, may never affect the server
// process serving it.

// virtualDefaultProbeChainBudget bounds one admitted chain's whole life: the
// target-version re-read, the candidate match, and the ffprobe enumeration
// behind the probe call all run under this deadline. probeVirtualSourceAndPersist
// applies its own inner budget on top, so the total cost of a chain is bounded
// twice: once here, by the chain owner, and once inside the probe.
var virtualDefaultProbeChainBudget = 90 * time.Second

// virtualSelectionProbeBudget bounds one explicit-selection probe chain: the
// target read, the provider URL resolution, and up to
// virtualSelectionProbeAttempts bounded enumerations. The item page's audio and
// subtitle menus wait on this answer, so it is the number this server documents
// to clients as the selection's bound, and a client's provisional-inventory
// window must not be shorter than it (web/src/player/utils/inventoryProvenance.ts
// carries the matching constant). It is the selection's own bound, not a slice of
// the discovery chain's 90 s allowance: a probe the viewer asked for must answer
// while they are still looking, or answer an honest failure.
var virtualSelectionProbeBudget = 20 * time.Second

// virtualSelectionProbeAttemptBudget bounds one enumeration inside a selection
// chain. The scanner's background probe timeout is a full minute, which is longer
// than the viewer will wait; a selection attempt gets the time a cold provider
// actually needs and one retry inside the chain bound if it fails.
//
// The measured cold enumeration — a provider relay that has to open the release
// before ffprobe can read it — took 6-10 s in production. The previous 5 s
// attempt budget cut every one of those before it could enumerate, so a selection
// that would have verified answered "probe failed" and the item page kept
// declared metadata until a post-commit probe delivered the inventory ~40 s after
// Play. 12 s covers the measured range with margin and still leaves the second
// attempt room inside the chain bound.
var virtualSelectionProbeAttemptBudget = 12 * time.Second

// virtualSelectionProbeAttempts is how many enumerations one selection probe
// earns. Background chains keep one attempt so a dead provider is not hammered;
// a viewer's fresh pick earns a second because a single cold-relay failure is
// common and the answer is due now. The second attempt is capped by the chain's
// remaining budget, so the pair can never exceed virtualSelectionProbeBudget.
const virtualSelectionProbeAttempts = 2

// virtualSelectionProbeRetryDelay separates the attempts of a selection probe.
// Short on purpose: it is a second chance for a cold relay, not an outage backoff.
var virtualSelectionProbeRetryDelay = 300 * time.Millisecond

// virtualSelectionWarmBudget bounds one explicit-selection warm-up. The work is
// a single forced provider listing — the same call playback start makes in
// FillVirtualReleaseCache — not the provider-side fetch itself, which the
// provider continues after the listing returns. Two minutes is generous for a
// slow provider listing and still bounds a wedged call.
var virtualSelectionWarmBudget = 2 * time.Minute

// virtualDefaultProbeChainConcurrency bounds how many discovery-triggered chains
// run at once. Two keeps a household-wide discovery burst from saturating the
// detached gate while still admitting a genuinely concurrent browse.
const virtualDefaultProbeChainConcurrency = 2

// virtualSelectionProbeChainConcurrency is the reserved lane for a probe the
// viewer asked for by selecting a version. It is deliberately separate from the
// discovery budget: a browse burst can hold every background slot, and the one
// thing that must never queue behind it is a person waiting on the menu they just
// opened. Selection chains never consume the background slots and background
// chains never consume this one, so neither can starve the other.
const virtualSelectionProbeChainConcurrency = 1

// Probe-chain triggers are log attributes, not dispatch keys. Discovery
// schedules the resolved default after persistence; a watch-detail read with
// an explicit version selection schedules that row. Refresh runs never
// schedule either way (see VirtualCandidatesRefreshService.listSource).
const (
	virtualProbeChainTriggerDiscovery = "virtual-candidates:discover"
	virtualProbeChainTriggerSelection = "watch-detail:select"
)

// virtualSelectionWarmTriggerDefault is the log trigger for a warm-up started
// by a detail read that resolved the item's default version. The work is the
// same as an explicit selection's warm-up; only the trigger label differs.
const virtualSelectionWarmTriggerDefault = "detail:default"

// VirtualPreprobeStatus reports what a synchronous single-file inventory
// preprobe established. The zero value means no probe ran, so the caller keeps
// the row's declared metadata.
type VirtualPreprobeStatus string

const (
	// VirtualPreprobeDeclined means the probe did not run: the row was not a
	// probe target, the failure damper refused it, the handler is not wired for
	// it, or no detached slot was free.
	VirtualPreprobeDeclined VirtualPreprobeStatus = ""
	// VirtualPreprobeVerified means the probe enumerated the row's bytes and
	// its durable evidence write committed, so a re-read carries the tracks.
	VirtualPreprobeVerified VirtualPreprobeStatus = "verified"
	// VirtualPreprobePending means a probe covers this row but left no
	// committed evidence yet (it ran and failed, timed out, or its evidence was
	// withheld, or another chain is still enumerating the same row), so the row
	// still answers with declared metadata.
	VirtualPreprobePending VirtualPreprobeStatus = "pending"
)

// Reasons a discovered or selected item ends up not probed. They are log
// values only — nothing branches on them — so an operator can tell a chain
// that was never admitted (schedule_refused) from one that ran out of budget
// before it reached the probe (budget_expired) and one whose probe failed
// (probe_failed). The remaining values name ordinary skips.
const (
	virtualProbeChainReasonScheduleRefused       = "schedule_refused"
	virtualProbeChainReasonBudgetExpired         = "budget_expired"
	virtualProbeChainReasonDefaultUnavailable    = "default_unavailable"
	virtualProbeChainReasonProbeFailed           = "probe_failed"
	virtualProbeChainReasonProbeIncomplete       = "probe_incomplete"
	virtualProbeChainReasonNoDefaultVersion      = "no_default_version"
	virtualProbeChainReasonSelectionMissing      = "selection_unavailable"
	virtualProbeChainReasonNotVirtual            = "not_virtual"
	virtualProbeChainReasonAlreadyProbed         = "already_probed"
	virtualProbeChainReasonStampWithoutInventory = "stamp_without_inventory"
	virtualProbeChainReasonNoExactCandidate      = "no_exact_candidate"
	virtualProbeChainReasonRecentlyFailed        = "recently_failed"
	virtualProbeChainReasonGateBusy              = "gate_busy"
)

// logVirtualProbeChainSkip records why one discovered or selected item was
// not probed. The first play still probes the row, so these lines are
// observability, not alerts: expected no-ops stay at debug, while a refused,
// expired, or failed chain leaves one info line naming the content, the
// trigger, and the reason.
func logVirtualProbeChainSkip(ctx context.Context, level slog.Level, trigger, contentID, reason, detail string, attrs ...any) {
	if ctx == nil {
		ctx = context.Background()
	}
	args := []any{
		virtualEvidenceLogKeyComponent, virtualEvidenceLogValueAPI,
		contentIDKey, contentID,
		virtualEvidenceLogKeyReason, reason,
		"trigger", trigger,
	}
	if detail != "" {
		args = append(args, "detail", detail)
	}
	args = append(args, attrs...)
	slog.Log(ctx, level, "virtual pre-play probe chain skipped", args...)
}

// VirtualDefaultProbeTarget re-reads the item's target playback version: the
// default (top-sorted) one after discovery persistence, or the file named by
// filter.SelectedFileID when a read carries an explicit selection. The chain
// must probe the row a viewer will actually open, and it must re-read those
// rows after persistence instead of trusting the discovery response, because
// discovery may have replaced rows between the request and the persist
// landing.
//
// The read carries the access filter of the request that scheduled the chain:
// "the default" is the default of the versions that viewer may see, and a
// version outside their visibility is not a version they can open.
type VirtualDefaultProbeTarget interface {
	DefaultVirtualProbeTarget(ctx context.Context, contentID string, filter catalog.AccessFilter) (*models.MediaFile, error)
}

// VirtualDefaultProbeTargetFunc allows wiring the default-version lookup as a
// plain function.
type VirtualDefaultProbeTargetFunc func(ctx context.Context, contentID string, filter catalog.AccessFilter) (*models.MediaFile, error)

// DefaultVirtualProbeTarget re-reads the item's default playback version.
func (f VirtualDefaultProbeTargetFunc) DefaultVirtualProbeTarget(ctx context.Context, contentID string, filter catalog.AccessFilter) (*models.MediaFile, error) {
	return f(ctx, contentID, filter)
}

// VirtualProbeVersionLister is the catalog version read behind the production
// default-probe target: the same top-sorted, access-filtered version list the
// browse picker uses. *catalog.DetailService implements it.
type VirtualProbeVersionLister interface {
	GetItemVersions(ctx context.Context, contentID string, filter catalog.AccessFilter) ([]catalog.FileVersion, error)
}

// NewVirtualDefaultProbeTarget builds the production default-probe target from
// the catalog detail service and the file repository. It resolves the default
// under the access filter of the discovery request that scheduled the chain
// (see VirtualDefaultProbeTarget), so versions the viewer cannot see are never
// read and never probed. A nil reader disables the target, and with it the
// chain.
func NewVirtualDefaultProbeTarget(versions VirtualProbeVersionLister, files FilePathResolver) VirtualDefaultProbeTarget {
	if versions == nil || files == nil {
		return nil
	}
	return VirtualDefaultProbeTargetFunc(func(ctx context.Context, contentID string, filter catalog.AccessFilter) (*models.MediaFile, error) {
		list, err := versions.GetItemVersions(ctx, contentID, filter)
		if err != nil || len(list) == 0 {
			return nil, err
		}
		selected := list[0].FileID
		if filter.SelectedFileID != 0 {
			selected = 0
			for _, version := range list {
				if version.FileID == filter.SelectedFileID {
					selected = version.FileID
					break
				}
			}
			if selected == 0 {
				return nil, nil
			}
		}
		row, err := files.GetByID(ctx, selected)
		if err != nil || row == nil {
			return nil, err
		}
		// A selected catalog entry authorizes the association named by the
		// listing, not an unrelated row that happens to carry the same id.
		if row.ID != selected || (row.EpisodeID != "" && row.EpisodeID != contentID) ||
			(row.EpisodeID == "" && row.ContentID != contentID) {
			return nil, nil
		}
		if _, none := filter.LibraryScope([]int{row.MediaFolderID}); none {
			return nil, nil
		}
		if filter.ScopeFilesToLibrary && filter.PresentationLibraryID != nil && row.MediaFolderID != *filter.PresentationLibraryID {
			return nil, nil
		}
		return row, nil
	})
}

// defaultProbeChainAdmission deduplicates and bounds pre-play probe chains.
// The map holds one entry per live chain, keyed by the content id the viewer
// opened, and admitted counts live chains against
// virtualDefaultProbeChainConcurrency.
//
// acquire is the whole admission operation: it checks the item key and the
// concurrency bound under one lock and performs no I/O, waits for nothing,
// and spawns nothing. A rejected caller returns immediately; the database
// reads and the goroutine belong to admitted work only. The zero value is
// ready to use.
type defaultProbeChainAdmission struct {
	mu       sync.Mutex
	active   map[string]struct{}
	done     map[string]chan struct{}
	admitted int
	priority int
}

// defaultProbeChainRefusal names why admission rejected a chain. The empty
// value means the chain was admitted.
type defaultProbeChainRefusal string

const (
	defaultProbeChainRefusalNone     defaultProbeChainRefusal = ""
	defaultProbeChainRefusalLive     defaultProbeChainRefusal = "chain_already_live"
	defaultProbeChainRefusalCapacity defaultProbeChainRefusal = "concurrency_full"
)

// acquire admits one chain for contentID. refusal is empty when admitted;
// otherwise it names the reason the callback was dropped. A live chain for the
// item already covers it (duplicate callbacks coalesce into that chain), and
// when the caller's own lane is full the callback is dropped — the play path
// and any later discovery run still probe normally. The returned done channel
// is the live chain's completion signal, non-nil only for the
// defaultProbeChainRefusalLive refusal: a selection probe joins the chain that
// already covers its row instead of being refused.
//
// priority admits into the selection lane (virtualSelectionProbeChainConcurrency)
// rather than the discovery lane, so a selection chain is never refused because
// background discovery holds every slot, and vice versa. The returned func
// releases the slot and must be called exactly once.
func (a *defaultProbeChainAdmission) acquire(contentID string, priority bool) (func(), defaultProbeChainRefusal, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active == nil {
		a.active = make(map[string]struct{})
	}
	if a.done == nil {
		a.done = make(map[string]chan struct{})
	}
	if _, live := a.active[contentID]; live {
		return nil, defaultProbeChainRefusalLive, a.done[contentID]
	}
	if priority {
		if a.priority >= virtualSelectionProbeChainConcurrency {
			return nil, defaultProbeChainRefusalCapacity, nil
		}
		a.priority++
	} else {
		if a.admitted >= virtualDefaultProbeChainConcurrency {
			return nil, defaultProbeChainRefusalCapacity, nil
		}
		a.admitted++
	}
	a.active[contentID] = struct{}{}
	done := make(chan struct{})
	a.done[contentID] = done
	return func() { a.release(contentID, priority) }, defaultProbeChainRefusalNone, nil
}

// isLive reports whether a chain for key is currently admitted. It performs no
// I/O and spawns nothing; callers use it to avoid duplicating work another
// admitted chain already covers (for example a selection warm-up that would
// re-open the bytes a live pre-play probe is already touching).
func (a *defaultProbeChainAdmission) isLive(key string) bool {
	if a == nil || key == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, live := a.active[key]
	return live
}

func (a *defaultProbeChainAdmission) release(contentID string, priority bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, live := a.active[contentID]; !live {
		return
	}
	delete(a.active, contentID)
	if done, ok := a.done[contentID]; ok {
		delete(a.done, contentID)
		close(done)
	}
	if priority {
		if a.priority > 0 {
			a.priority--
		}
		return
	}
	a.admitted--
}

// ScheduleDefaultFileProbe is the subscription point the discovery services
// call after a successful detached persistence (see
// VirtualCandidatesRefreshService.ScheduleDefaultFileProbe). It is
// non-blocking by contract: admission is a single atomic check, and
// everything else — including the database re-read — happens inside the
// admitted chain, never on the discovery caller. filter is the access filter
// of the request that triggered discovery; the chain resolves its default
// under that same filter (see VirtualDefaultProbeTarget).
func (h *PlaybackHandler) ScheduleDefaultFileProbe(contentID string, source *models.MediaFile, streams []VirtualPlaybackStream, filter catalog.AccessFilter) {
	h.scheduleFileProbe(contentID, source, streams, filter, virtualProbeChainTriggerDiscovery, false)
}

// ScheduleSelectedFileProbe is the pre-play probe entry point for an explicit
// version selection. A watch-detail read (v1, v2, or a candidates
// discover/detail read) that names a version calls it once the detail carries
// a virtual row whose inventory is still missing: without it the picker shows
// declared metadata only and the audio/subtitle menus stay unknown until the
// first play.
//
// It shares the discovery chain's admission (one live chain per content and
// selected file, the same process-wide slots, drop-if-live), its budget, its
// failure damper, its durable evidence write, and the versions_updated publish
// that write already triggers. The only difference is the target: the row
// named by fileID under filter (see VirtualDefaultProbeTarget), never the
// resolved default. A bare listing or polling read schedules nothing because
// it carries no explicit selection, and the discovery default path below is
// unchanged.
func (h *PlaybackHandler) ScheduleSelectedFileProbe(contentID string, fileID int, filter catalog.AccessFilter) {
	if fileID <= 0 {
		logVirtualProbeChainSkip(context.Background(), slog.LevelInfo, virtualProbeChainTriggerSelection, contentID,
			virtualProbeChainReasonScheduleRefused, "invalid_input")
		return
	}
	filter.SelectedFileID = fileID
	h.scheduleFileProbe(contentID, nil, nil, filter, virtualProbeChainTriggerSelection, true)
}

// ScheduleSelectedFileWarm is the explicit-selection warm-up entry point: it
// starts the provider-side fetch for the selected row's bytes as soon as the
// viewer picks that version, instead of waiting for the play press to do it.
// The row may already be probed — that is the common case and exactly why this
// is separate from the probe chain: ffprobe evidence says what the bytes are,
// not that the provider has fetched them, and a cold AltMount release still
// blocks the first play for the length of the NZB download.
//
// It reuses the playback start's own fill call (VirtualReleaseCacheWarmup is the
// same VirtualReleaseCacheFillFunc the stream handler fills a pinned release
// with), so there is no second download path. The wired fill already skips a
// release the provider reports cached or downloaded, and a provider it cannot
// classify, so an already-cached selection costs nothing. Fire-and-forget and
// bounded: the caller only reserves an admission slot; the target re-read, the
// listing, and the goroutine all happen detached under virtualSelectionWarmBudget
// and the shared detached-work gate.
//
// Deduplication matches the probe chain: one live warm-up per (item, selected
// file), and a live pre-play probe for the same row covers the fetch because
// opening the provider URL is itself what starts it. Nothing here writes the
// catalog — no inventory, no probe stamp, no failed_at verdict — it only makes
// the bytes available earlier, so a failure is a debug line and never a verdict.
//
// Metered connections: a watch-detail read carries no server-visible network
// hint (the client's metered flag exists only on a playback plan request), and
// the codebase's only Wi-Fi gate, downloads.wifi_only, is a per-profile
// client-download preference, not a server-side prefetch switch. The default is
// therefore to warm every explicit selection and let the cached-release check
// skip the redundant ones. A future toggle belongs with the other
// virtual_library.* settings next to cache_ttl_minutes and would be read where
// the fill is wired, not here; no settings key is introduced for it now.
func (h *PlaybackHandler) ScheduleSelectedFileWarm(contentID string, fileID int, filter catalog.AccessFilter) {
	h.scheduleFileWarm(contentID, fileID, filter, virtualProbeChainTriggerSelection)
}

// ScheduleDefaultFileWarm warms the default row a detail read resolved. A
// catalog_detail or watch_detail read lists candidates, persists them, and
// resolves the top-sorted version; without this warm-up that version's provider
// fetch waits for the play press even though the read already named it. The
// caller passes the resolved default file id, so admission, slots, the
// short damper check, and the no-state-write rule are exactly the explicit
// selection path's: a live selection warm for the same row already owns the
// work, and a default warm already running makes a later selection a no-op.
//
// It is scheduled only from a detail-page discovery, never from a listing-only
// request or a refresh job, and never by a browse/list/search row read (those
// do not run detail discovery at all).
func (h *PlaybackHandler) ScheduleDefaultFileWarm(contentID string, fileID int, filter catalog.AccessFilter) {
	h.scheduleFileWarm(contentID, fileID, filter, virtualSelectionWarmTriggerDefault)
}

// scheduleFileWarm is the one admission and hand-off path for both warm
// triggers. The trigger is a log attribute only; it never changes the
// admission key, the fill, or the target.
func (h *PlaybackHandler) scheduleFileWarm(contentID string, fileID int, filter catalog.AccessFilter, trigger string) {
	if h == nil || contentID == "" || fileID <= 0 {
		return
	}
	if h.VirtualReleaseCacheWarmup == nil || h.DefaultProbeTarget == nil {
		return
	}
	filter.SelectedFileID = fileID
	// A live probe chain for this exact row is already opening the provider URL,
	// which starts the same fetch; a second listing would only duplicate it.
	if h.defaultProbeChains.isLive(contentID + "\x00" + strconv.Itoa(fileID)) {
		logVirtualSelectionWarmSkip(contentID, trigger, "probe_live")
		return
	}
	ctx, cancel := h.virtualDetachedContext(context.Background(), virtualSelectionWarmBudget)
	// The warm-up is a viewer action too (the row was just selected), so it runs
	// on the selection lane: a discovery burst holding every background slot must
	// not delay the provider fetch the pick started.
	release, refusal, _ := h.selectedWarmChains.acquire("warm\x00"+contentID+"\x00"+strconv.Itoa(fileID), trigger == virtualProbeChainTriggerSelection)
	if refusal != defaultProbeChainRefusalNone {
		cancel()
		logVirtualSelectionWarmSkip(contentID, trigger, string(refusal))
		return
	}
	filterSnapshot := virtualProbeAccessFilterSnapshot(filter)
	h.warmChainWG.Add(1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(context.WithoutCancel(ctx), "virtual selection warm-up panicked",
					"component", "api", "content_id", contentID, "stack", string(debug.Stack()))
			}
			release()
			cancel()
			h.warmChainWG.Done()
		}()
		h.runSelectedFileWarm(ctx, contentID, filterSnapshot, trigger)
	}()
}

// logVirtualSelectionWarmSkip records why a selection or default warm-up did
// not start. Expected skips stay at debug: the play path fills the same release
// when the time comes, so a skipped warm-up is a latency miss, not a failure.
func logVirtualSelectionWarmSkip(contentID, trigger, reason string) {
	slog.DebugContext(context.Background(), "virtual selection warm-up skipped",
		"component", "api",
		contentIDKey, contentID,
		virtualEvidenceLogKeyReason, reason,
		"trigger", trigger)
}

// runSelectedFileWarm is the body of one admitted warm-up. It re-reads the
// selected row under the triggering request's access filter (the same
// authorization the probe chain uses), requires the exact row and a virtual
// path, then hands the row's virtual URI to the playback fill call. It never
// writes catalog state and never changes a verdict.
func (h *PlaybackHandler) runSelectedFileWarm(ctx context.Context, contentID string, filter catalog.AccessFilter, trigger string) {
	if err := ctx.Err(); err != nil {
		return
	}
	row, err := h.DefaultProbeTarget.DefaultVirtualProbeTarget(ctx, contentID, filter)
	if err != nil || row == nil {
		return
	}
	if filter.SelectedFileID > 0 && row.ID != filter.SelectedFileID {
		return
	}
	if !isVirtualPlaybackFile(row) {
		return
	}
	gate := h.detachedGate()
	if !gate.tryAcquire() {
		logVirtualSelectionWarmSkip(contentID, trigger, virtualProbeChainReasonGateBusy)
		return
	}
	defer gate.release()
	if err := ctx.Err(); err != nil {
		return
	}
	if err := h.VirtualReleaseCacheWarmup(ctx, row.FilePath, row.VirtualOwnerInstallationID, nil); err != nil {
		slog.DebugContext(ctx, "virtual selection warm-up could not start the provider fetch",
			"component", "api", "content_id", contentID, "file_id", row.ID,
			virtualEvidenceLogKeyError, logredact.SanitizeURLError(err))
	}
}

// scheduleFileProbe is the one admission and hand-off path: it validates the
// trigger's inputs, admits the chain under the content+selection key, snapshots
// everything the detached body reads, and spawns it. explicitSelection selects
// between the discovery listing hand-off (default target) and a selection
// entry point that names its row through the access filter.
func (h *PlaybackHandler) scheduleFileProbe(contentID string, source *models.MediaFile, streams []VirtualPlaybackStream, filter catalog.AccessFilter, trigger string, explicitSelection bool) {
	if h == nil {
		logVirtualProbeChainSkip(context.Background(), slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonScheduleRefused, "nil_handler")
		return
	}
	if contentID == "" {
		logVirtualProbeChainSkip(context.Background(), slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonScheduleRefused, "invalid_input")
		return
	}
	if explicitSelection {
		if filter.SelectedFileID <= 0 {
			logVirtualProbeChainSkip(context.Background(), slog.LevelInfo, trigger, contentID,
				virtualProbeChainReasonScheduleRefused, "invalid_input")
			return
		}
	} else if source == nil || len(streams) == 0 {
		logVirtualProbeChainSkip(context.Background(), slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonScheduleRefused, "invalid_input")
		return
	}
	if h.DefaultProbeTarget == nil {
		logVirtualProbeChainSkip(context.Background(), slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonScheduleRefused, "target_unwired")
		return
	}
	if h.VirtualPlaybackSourceProber == nil && h.VirtualPlaybackSourceProberWithHeaders == nil {
		logVirtualProbeChainSkip(context.Background(), slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonScheduleRefused, "prober_unwired")
		return
	}
	// One chain per item and selected version. Discovery persists one source
	// group at a time; keying admission by source as well let five groups for
	// the same title each schedule a chain, exhausting the process-wide slots
	// with duplicates. The chain re-reads the selected row and probes that row's
	// own stored URL, so any discovery group can safely start it. A selection
	// read uses the same key, so it coalesces with a discovery chain already
	// probing the same row instead of opening a second probe.
	admissionKey := contentID + "\x00" + strconv.Itoa(filter.SelectedFileID)
	// A selection probe runs on the selection lane and under the selection's own
	// budget: a discovery burst can hold both background slots, and the whole
	// discovery chain allowance is far longer than a menu will wait.
	chainBudget := virtualDefaultProbeChainBudget
	if explicitSelection {
		chainBudget = min(chainBudget, virtualSelectionProbeBudget)
	}
	ctx, cancel := h.virtualDetachedContext(context.Background(), chainBudget)
	release, refusal, liveChain := h.defaultProbeChains.acquire(admissionKey, explicitSelection)
	var join <-chan struct{}
	switch {
	case refusal == defaultProbeChainRefusalNone:
	case explicitSelection && refusal == defaultProbeChainRefusalLive && liveChain != nil:
		// A live chain for this exact row is already enumerating the bytes the
		// viewer selected. Join it instead of refusing: when its evidence lands,
		// the follow-up below answers already_probed; when it failed, the viewer's
		// own probe gets its attempt. The wait is bounded by this chain's budget.
		join = liveChain
	default:
		cancel()
		// A chain for this item is already live, or the caller's lane is full.
		// Either way the callback is dropped: the live chain re-reads the same
		// target, and a dropped probe still happens on the first play. No
		// queue, no retry, no delay for the caller.
		logVirtualProbeChainSkip(context.Background(), slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonScheduleRefused, string(refusal))
		return
	}
	// Snapshot the exact inputs so a later mutation of the caller's structures
	// cannot change what is probed: the row is copied by value, the filter's
	// reference members are cloned, and the listing entries are deep-copied
	// for the mutable members the probe reads. A selection chain carries no
	// listing hand-off.
	var probeSource *models.MediaFile
	if source != nil {
		snapshot := *source
		probeSource = &snapshot
	}
	filterSnapshot := virtualProbeAccessFilterSnapshot(filter)
	var streamsSnapshot []VirtualPlaybackStream
	if len(streams) > 0 {
		streamsSnapshot = make([]VirtualPlaybackStream, len(streams))
		for i := range streams {
			streamsSnapshot[i] = virtualProbeStreamSnapshot(streams[i])
		}
	}
	// The chain detaches from the requesting caller: its context carries the
	// chain budget and ends with the service (virtualDetachedContext cancels
	// on service shutdown), never with the browse request that listed the
	// candidates or selected a version. The single cleanup defer runs in a
	// fixed order — recover log, admission release, context cancel, completion
	// signal — so a panic anywhere in the chain body is an observation, not a
	// process death unrelated playback would share, and Done() reports the
	// slot truly free: a Wait() caller must be able to re-schedule the same
	// item without racing the previous chain's teardown.
	h.probeChainWG.Add(1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(context.WithoutCancel(ctx), "virtual pre-play probe chain panicked",
					"component", "api", "content_id", contentID, "stack", string(debug.Stack()))
			}
			if release != nil {
				release()
			}
			cancel()
			h.probeChainWG.Done()
		}()
		if join != nil {
			// Join the live chain that already covers this row's bytes, then take
			// the selection lane for the follow-up. A chain that still holds the
			// row is the answer; a busy lane means the same.
			select {
			case <-join:
			case <-ctx.Done():
				logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
					virtualProbeChainReasonBudgetExpired, "join", virtualEvidenceLogKeyError, ctx.Err())
				return
			}
			var joinedRefusal defaultProbeChainRefusal
			release, joinedRefusal, _ = h.defaultProbeChains.acquire(admissionKey, explicitSelection)
			if joinedRefusal != defaultProbeChainRefusalNone {
				logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
					virtualProbeChainReasonScheduleRefused, "joined_"+string(joinedRefusal))
				return
			}
		}
		// The detached chain reports through its logs and its catalog write.
		_, _ = h.runFileProbeChain(ctx, contentID, probeSource, streamsSnapshot, filterSnapshot, trigger, explicitSelection)
	}()
}

// virtualProbeAccessFilterSnapshot deep-copies the reference members of an
// access filter so the detached chain reads exactly the constraints of the
// request that scheduled it, even if that request's structures are reused
// afterwards. Scalar members copy with the value.
func virtualProbeAccessFilterSnapshot(filter catalog.AccessFilter) catalog.AccessFilter {
	clone := filter
	clone.AllowedLibraryIDs = slices.Clone(filter.AllowedLibraryIDs)
	clone.AllowedContentIDs = slices.Clone(filter.AllowedContentIDs)
	clone.DisabledLibraryIDs = slices.Clone(filter.DisabledLibraryIDs)
	clone.ExcludedMediaTypes = slices.Clone(filter.ExcludedMediaTypes)
	clone.MetadataLanguageOverrides = maps.Clone(filter.MetadataLanguageOverrides)
	if filter.PresentationLibraryID != nil {
		presentationLibrary := *filter.PresentationLibraryID
		clone.PresentationLibraryID = &presentationLibrary
	}
	return clone
}

// virtualProbeStreamSnapshot deep-copies the mutable members a probe reads
// from one listing entry. Shallow struct copies share the request-header map
// and the declared track slices with the discovery caller, so a later caller
// mutation could change or race a running probe; every member the chain
// consumes is cloned.
func virtualProbeStreamSnapshot(base VirtualPlaybackStream) VirtualPlaybackStream {
	clone := base
	clone.RequestHeaders = maps.Clone(base.RequestHeaders)
	clone.AudioLanguages = slices.Clone(base.AudioLanguages)
	clone.SubtitleLanguages = slices.Clone(base.SubtitleLanguages)
	return clone
}

// resolveFileProbeTarget re-reads and authorizes the row one admitted chain may
// probe, logging the reason and returning ok=false for every refusal. The
// target is never the request-time row: discovery may have replaced the rows
// between the listing and the persistence of this chain's inputs, so it is
// whatever the catalog serves now for this item, under the triggering request's
// access filter. A selection chain carries its file id in that filter, so the
// same read resolves the row the viewer picked (see VirtualDefaultProbeTarget).
func (h *PlaybackHandler) resolveFileProbeTarget(ctx context.Context, contentID string, filter catalog.AccessFilter, trigger string, explicitSelection bool) (*models.MediaFile, bool) {
	if err := ctx.Err(); err != nil {
		logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonBudgetExpired, "chain_start", virtualEvidenceLogKeyError, err)
		return nil, false
	}
	row, err := h.DefaultProbeTarget.DefaultVirtualProbeTarget(ctx, contentID, filter)
	if err != nil {
		if ctx.Err() != nil {
			logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
				virtualProbeChainReasonBudgetExpired, "target_read", virtualEvidenceLogKeyError, ctx.Err())
			return nil, false
		}
		logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonDefaultUnavailable, "target_read", virtualEvidenceLogKeyError, err)
		return nil, false
	}
	if ctx.Err() != nil {
		logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonBudgetExpired, "target_read", virtualEvidenceLogKeyError, ctx.Err())
		return nil, false
	}
	if row == nil {
		reason := virtualProbeChainReasonNoDefaultVersion
		if explicitSelection {
			reason = virtualProbeChainReasonSelectionMissing
		}
		logVirtualProbeChainSkip(ctx, slog.LevelDebug, trigger, contentID,
			reason, "")
		return nil, false
	}
	// A selection chain must probe the row it names. A target that resolves
	// anything else never authorizes speculative bytes on the viewer's behalf.
	if explicitSelection && row.ID != filter.SelectedFileID {
		logVirtualProbeChainSkip(ctx, slog.LevelDebug, trigger, contentID,
			virtualProbeChainReasonNoExactCandidate, "selection_mismatch", virtualEvidenceLogKeyFileID, row.ID)
		return nil, false
	}
	if !isVirtualPlaybackFile(row) {
		logVirtualProbeChainSkip(ctx, slog.LevelDebug, trigger, contentID,
			virtualProbeChainReasonNotVirtual, "")
		return nil, false
	}
	return row, true
}

// virtualRowCarriesProbeInventory reports whether a row's stored probe evidence
// actually describes tracks. A probe stamp with no video, audio or subtitle
// inventory is not evidence a probe enumerated anything: it is the state an
// unadoptable or invalidated evidence write leaves behind. Re-probing such a row
// is the repair path, so the chain gate must not treat the stamp alone as
// "already probed" (see runFileProbeChain).
func virtualRowCarriesProbeInventory(row *models.MediaFile) bool {
	if row == nil {
		return false
	}
	return len(row.VideoTracks) > 0 || len(row.AudioTracks) > 0 || len(row.SubtitleTracks) > 0
}

// runFileProbeChain is the body of one admitted chain. It probes either the
// item's default version (discovery trigger) or one explicitly selected
// version (selection trigger); the authorization read, the virtual guard, the
// probe stamp, the failure damper, the detached gate, the durable evidence
// write, and the versions_updated publish are the same for both. Every step is
// bounded by the chain context; a failure or an unsafe target leaves one
// structured skip line naming the reason (see logVirtualProbeChainSkip).
//
// It reports the probe's outcome (empty when no probe ran) and whether that
// outcome's evidence write committed. The detached scheduler ignores both; the
// synchronous preprobe uses them to tell an enumerated row from one that still
// answers with declared metadata.
func (h *PlaybackHandler) runFileProbeChain(ctx context.Context, contentID string, source *models.MediaFile, streams []VirtualPlaybackStream, filter catalog.AccessFilter, trigger string, explicitSelection bool) (string, bool) {
	row, ok := h.resolveFileProbeTarget(ctx, contentID, filter, trigger, explicitSelection)
	if !ok {
		return "", false
	}
	// Stamp skip and damper check happen inside admitted work — the admission
	// callback only reserves the slot, by design. A selection whose row already
	// carries probed inventory stops here without touching the provider. The
	// stamp alone is not inventory: a build that wrote evidence it could not
	// adopt (or invalidated foreign evidence) could leave a row stamped with
	// empty track arrays, and treating that as "already probed" froze it forever
	// — production's played 4K row kept an empty menu through 40 skipped chains.
	if row.ProbeUpdatedAt != nil && virtualRowCarriesProbeInventory(row) {
		logVirtualProbeChainSkip(ctx, slog.LevelDebug, trigger, contentID,
			virtualProbeChainReasonAlreadyProbed, "", virtualEvidenceLogKeyFileID, row.ID)
		return "", false
	}
	if row.ProbeUpdatedAt != nil {
		// Re-probe, but say why: this row is the repair path for an earlier
		// evidence write that landed no inventory.
		logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonStampWithoutInventory, "reprobing", virtualEvidenceLogKeyFileID, row.ID)
	}
	oid := row.VirtualOwnerInstallationID
	var probeCand *VirtualPlaybackStream
	if explicitSelection {
		// The selection read authorized this exact row; its own stored result
		// names the bytes to enumerate.
		probeCand = virtualPersistedSelectedProbeCandidate(row)
	} else {
		probeCand = virtualDefaultProbeCandidate(row, source, streams, filter.SelectedFileID != 0)
	}
	if probeCand == nil {
		// Without an exact byte match there is nothing safe to enumerate: the
		// target may be the neutral group row, and a neutral row may resolve to
		// a sibling candidate. The play path's own probe handles those; a chain
		// must never guess.
		logVirtualProbeChainSkip(ctx, slog.LevelDebug, trigger, contentID,
			virtualProbeChainReasonNoExactCandidate, "", virtualEvidenceLogKeyFileID, row.ID)
		return "", false
	}
	if virtualProbeBackedOff(virtualProbeFailureKey(probeCand.URI, oid)) {
		if explicitSelection {
			// A fresh selection is the viewer asking now. The damper exists so
			// background re-probes stop hammering a failing provider; it must not
			// refuse the pick the viewer just made, or the menu they opened can never
			// learn the truth about the version they chose.
			slog.DebugContext(ctx, "virtual probe damper bypassed for a fresh selection",
				"component", "api", contentIDKey, contentID, "trigger", trigger,
				virtualEvidenceLogKeyFileID, row.ID,
				"last_failure", virtualProbeFailures.reason(virtualProbeFailureKey(probeCand.URI, oid)))
		} else {
			logVirtualProbeChainSkip(ctx, slog.LevelDebug, trigger, contentID,
				virtualProbeChainReasonRecentlyFailed, "", virtualEvidenceLogKeyFileID, row.ID)
			return "", false
		}
	}
	expectedRuntimeMinutes := h.virtualExpectedRuntimeMinutes(ctx, row)
	stickyKey := bestResultCacheKey(contentID, virtualPlaybackNeutralKey(row.FilePath), oid)
	probeURL, probeHeaders, hasProbeURL := h.virtualChainProbeURL(ctx, row, *probeCand, filter)
	if !hasProbeURL {
		logVirtualProbeChainSkip(ctx, slog.LevelDebug, trigger, contentID,
			virtualProbeChainReasonNoExactCandidate, "no_provider_url", virtualEvidenceLogKeyFileID, row.ID)
		return "", false
	}
	probeCand.RequestHeaders = probeHeaders
	// A selection probe earns one retry inside the selection's own bound: a single
	// provider-side enumeration failure is common (a cold relay, an ffprobe that
	// raced a still-opening read) and the viewer is waiting on this exact answer.
	// Background chains keep one attempt so a dead provider is not hammered.
	attempts := 1
	if explicitSelection {
		attempts = virtualSelectionProbeAttempts
	}
	outcome := ""
	carried := virtualCommittedProbeInventory{}
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
			case <-time.After(virtualSelectionProbeRetryDelay):
			}
			if ctx.Err() != nil {
				break
			}
		}
		attemptCtx := ctx
		cancelAttempt := func() {}
		if explicitSelection {
			attemptCtx, cancelAttempt = context.WithTimeout(ctx, virtualSelectionProbeAttemptBudget)
		}
		var (
			attemptOutcome    string
			attemptCarried    virtualCommittedProbeInventory
			enumerationFailed bool
			attemptExpired    bool
			gateBusy          bool
		)
		// One attempt runs in its own closure so a panic inside the prober still
		// releases the shared gate and the attempt's deadline: the chain's outer
		// recover reports the panic, but only this defer gives the slot back.
		func() {
			defer cancelAttempt()
			// The enumeration runs behind the shared detached gate, the same bounded
			// admission every background virtual probe uses. No slot, no probe.
			gate := h.detachedGate()
			if gate == nil || !gate.tryAcquire() {
				gateBusy = true
				return
			}
			defer gate.release()
			attemptCand := *probeCand
			attemptTransient := cloneVirtualProbeTransient(*row)
			// Zero the duration so the probe measures the empirical duration instead
			// of inheriting the catalog value.
			attemptTransient.Duration = 0
			failuresBefore := virtualProbeFailures.count(virtualProbeFailureKey(attemptCand.URI, oid))
			// The chain probes the selected/default row's own candidate bytes and uses
			// the durable evidence path: an already-loaded detail/version page can only
			// be invalidated after the tracks are actually committed. No session binding
			// exists here, so the fence is nil and the identity tiers are zero — the same
			// posture the unstamped-row fast path takes, where the row owns its candidate.
			attemptOutcome, attemptCarried = h.probeVirtualSourceAndPersistWith(
				attemptCtx, stickyKey, row, probeURL, attemptTransient, attemptCand,
				resolver.PersistedIdentityTiers{}, expectedRuntimeMinutes, oid, true, nil,
			)
			// Retry only an enumeration that actually failed inside its own allowance:
			// a recorded probe failure (the damper carries its reason) or an attempt that
			// ran out of its short attempt budget while the chain still has time. A
			// refused evidence write and an incomplete enumeration are not provider
			// failures, and re-probing them would only repeat the same answer.
			enumerationFailed = virtualProbeFailures.count(virtualProbeFailureKey(attemptCand.URI, oid)) > failuresBefore
			attemptExpired = attemptCtx.Err() != nil && ctx.Err() == nil
		}()
		if gateBusy {
			logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
				virtualProbeChainReasonGateBusy, "", virtualEvidenceLogKeyFileID, row.ID)
			return "", false
		}
		outcome, carried = attemptOutcome, attemptCarried
		if outcome == probeOutcomeVerified || ctx.Err() != nil {
			break
		}
		if attempt < attempts && !enumerationFailed && !attemptExpired {
			break
		}
		if attempt < attempts {
			slog.DebugContext(ctx, "virtual selection probe failed; retrying inside the selection bound",
				"component", "api", contentIDKey, contentID, "trigger", trigger,
				virtualEvidenceLogKeyFileID, row.ID, "attempt", attempt, "attempts", attempts,
				"reason", virtualProbeFailures.reason(virtualProbeFailureKey(probeCand.URI, oid)))
		}
	}
	// A verified outcome with an unwritten carrier means the row keeps its
	// previous evidence: the serve recorded a pin's first served-size
	// observation, so there is no catalog change for a detail page to refresh
	// (see virtualProbeEvidenceWithholdsIdentity).
	if outcome == probeOutcomeVerified && carried.written && h.EventsHub != nil {
		publishCtx, publishCancel := context.WithTimeout(context.WithoutCancel(ctx), virtualCandidatePublishBudget)
		publishVirtualVersionsUpdatedEvent(publishCtx, nil, h.EventsHub, contentID)
		publishCancel()
	}
	switch outcome {
	case probeOutcomeFailed:
		if ctx.Err() != nil {
			logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
				virtualProbeChainReasonBudgetExpired, "probe", virtualEvidenceLogKeyError, ctx.Err())
			return outcome, false
		}
		// The reason travels with the skip line (and with the damper mark a later
		// background probe reads) so the answer is a named failure, never a silent
		// "unavailable".
		logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonProbeFailed, "probe",
			virtualEvidenceLogKeyFileID, row.ID,
			"attempts", attempts,
			"reason", virtualProbeFailures.reason(virtualProbeFailureKey(probeCand.URI, oid)))
	case probeOutcomePartial:
		logVirtualProbeChainSkip(ctx, slog.LevelInfo, trigger, contentID,
			virtualProbeChainReasonProbeIncomplete, "probe", virtualEvidenceLogKeyFileID, row.ID,
			"attempts", attempts)
	}
	return outcome, carried.written
}

// PreprobeSelectedFile runs one explicitly selected virtual row's inventory
// probe synchronously for a caller that is going to answer with the row's
// tracks. It is the versions check's single-file inventory mode: the same
// target read, exact-candidate match, damper, detached gate and durable
// evidence write the detached selection chain uses, inline instead of spawned.
//
// The caller owns the deadline: the request context bounds the whole call, so a
// slow provider ends as VirtualPreprobePending with the row's declared metadata
// intact and never as an error. Admission is shared with the detached chains
// (one live chain per content and selected file), so a row already being probed
// elsewhere reports pending instead of opening a second ffprobe on the same
// relay bytes.
func (h *PlaybackHandler) PreprobeSelectedFile(ctx context.Context, contentID string, fileID int, filter catalog.AccessFilter) VirtualPreprobeStatus {
	if h == nil || contentID == "" || fileID <= 0 {
		return VirtualPreprobeDeclined
	}
	// A chain already enumerating this exact row owns the work; it answers
	// pending so the caller keeps declared metadata and lets that chain land.
	filter.SelectedFileID = fileID
	key := contentID + "\x00" + strconv.Itoa(fileID)
	if h.defaultProbeChains.isLive(key) {
		return VirtualPreprobePending
	}
	// The same wiring the detached scheduler requires: without either seam the
	// probe could only fail, and a misconfigured server must answer declared
	// metadata rather than a spurious pending.
	if h.DefaultProbeTarget == nil || (h.VirtualPlaybackSourceProber == nil && h.VirtualPlaybackSourceProberWithHeaders == nil) {
		return VirtualPreprobeDeclined
	}
	release, refusal, _ := h.defaultProbeChains.acquire(key, true)
	if refusal != defaultProbeChainRefusalNone {
		return VirtualPreprobeDeclined
	}
	defer release()
	outcome, written := h.runFileProbeChain(ctx, contentID, nil, nil, filter, virtualProbeChainTriggerSelection, true)
	switch {
	case outcome == probeOutcomeVerified && written:
		return VirtualPreprobeVerified
	case outcome == "":
		if ctx.Err() != nil {
			// The caller's deadline ended the wait before the chain could run.
			// Nothing committed, but a probe was attempted for this row.
			return VirtualPreprobePending
		}
		// Nothing ran: the row was not a probe target or the damper refused it.
		return VirtualPreprobeDeclined
	default:
		return VirtualPreprobePending
	}
}

// virtualChainStoredURLMaxAge bounds how long a pre-play probe may trust a
// row's persisted ResolvedURL that carries no expiry of its own.
//
// A nil expiry is an unknown lifetime, not an unlimited one: the provider
// declared no lifetime for the token, so the row can only be trusted while it
// is demonstrably recent. The listing that mints such a token is cached by the
// resolver for virtual_library.cache_ttl_minutes (ten minutes by default), and
// a provider such as AltMount issues short-lived release tokens that it
// re-mints on every listing — once one lapses, the provider falls back to
// title-based recovery server-side. Three listing windows is the cap: long
// enough that browsing then playing reuses the token the last listing minted,
// short enough that under the default cache the re-resolve reaches the provider
// instead of the listing that produced the stale token.
//
// The clock is row.UpdatedAt: the row's only persisted freshness evidence.
// A provider re-list and a stored-URL refresh both advance it, and those are
// the writes that carry a newly minted token. Other metadata writes advance it
// too, so the bound approximates the token's age instead of proving it; what it
// rules out is the case this exists for — a row minted long ago whose URL
// carries no expiry and was therefore trusted forever.
var virtualChainStoredURLMaxAge = 30 * time.Minute

// virtualChainStoredURLTrusted reports whether the pre-play probe may open the
// row's persisted ResolvedURL now. The stored URL is stale — and the chain must
// resolve afresh exactly as if the row carried no URL — when its own expiry has
// passed, when the matched listing entry says the provider's copy of this
// row's URL has already lapsed, or when the row carries no expiry and is older
// than virtualChainStoredURLMaxAge (no timestamp at all is not evidence of
// freshness).
func virtualChainStoredURLTrusted(row *models.MediaFile, now time.Time, listingExpired bool) bool {
	if row == nil || strings.TrimSpace(row.ResolvedURL) == "" || listingExpired {
		return false
	}
	if row.ResolvedURLExpiresAt != nil {
		return row.ResolvedURLExpiresAt.After(now)
	}
	if row.UpdatedAt.IsZero() {
		return false
	}
	return !now.After(row.UpdatedAt.Add(virtualChainStoredURLMaxAge))
}

// virtualChainProbeURL resolves the HTTP source the pre-play probe must open.
// The catalog row and the discovery listing both name the candidate by its
// durable virtual URI; that URI is not a provider transport and must never be
// handed to ffprobe. The fresh listing's resolved URL is preferred, then the
// row's stored URL while it is still trusted (see
// virtualChainStoredURLTrusted), then a fresh resolve under the same access
// filter that authorized discovery. A fresh resolve's URL is persisted back
// onto the row through the existing stored-URL refresh write, so the next
// chain, probe, or play reuses the newly minted token instead of minting
// another.
func (h *PlaybackHandler) virtualChainProbeURL(ctx context.Context, row *models.MediaFile, cand VirtualPlaybackStream, filter catalog.AccessFilter) (string, map[string]string, bool) {
	if row == nil {
		return "", nil, false
	}
	now := time.Now()
	rowURL := strings.TrimSpace(row.ResolvedURL)
	// The matched listing entry is the provider's own view of this row's URL.
	// When it has already lapsed, a token minted from the same listing cannot
	// be trusted either, so the stored URL is stale too.
	candExpired := cand.ProviderExpiresAt != nil && !cand.ProviderExpiresAt.After(now)
	// A persisted candidate copied from a selected row carries that row's signed
	// URL too. It must obey the same staleness fence as the row fallback below;
	// otherwise a vanished profile variant with an expired or ancient URL is
	// probed instead of being resolved freshly, and the probe failure damages
	// the candidate. A candidate minted by the fresh listing (a different URL)
	// keeps its first preference.
	if url := strings.TrimSpace(cand.ProviderURL); url != "" &&
		(cand.ProviderExpiresAt == nil || cand.ProviderExpiresAt.After(now)) {
		if url != rowURL || virtualChainStoredURLTrusted(row, now, candExpired) {
			return url, maps.Clone(cand.RequestHeaders), true
		}
	}
	if rowURL != "" && virtualChainStoredURLTrusted(row, now, candExpired) {
		return rowURL, maps.Clone(row.ProviderRequestHeaders), true
	}
	if h.VirtualMediaDetailedResolver == nil {
		return "", nil, false
	}
	resolved, err := h.VirtualMediaDetailedResolver.ResolveVirtualMediaDetailed(
		ctx, row.FilePath, row.VirtualOwnerInstallationID, filter.UserID, filter.ProfileID,
		false, nil, "",
	)
	if err != nil || strings.TrimSpace(resolved.URL) == "" {
		return "", nil, false
	}
	// Record the freshly minted token through the Phase-1 stored-URL write, so
	// a long-lived stale row stops paying a provider round-trip on every
	// browse-then-play. The write is the ordinary CAS-fenced refresh: it
	// refuses a resolution for a substituted sibling and never touches probe
	// evidence. Best-effort: the URL is already in hand.
	_, _ = refreshStoredVirtualResolution(ctx, row, resolved, h.VirtualFileMetadataSaver, h.VirtualFileSaver)
	return resolved.URL, maps.Clone(resolved.RequestHeaders), true
}

// virtualDefaultProbeCandidate resolves the exact bytes the chain may probe.
// An exact URI match against the listing the discovery run just persisted is
// preferred. A selected version may also use its own persisted row when the
// fresh listing dropped that profile variant: the target read authorized the
// row, and a neutral source covers every profile variant beneath its base path.
// A profile-specific source covers only its own group. The neutral group row
// itself is never admissible — it names no specific release and may resolve to
// a sibling candidate, so the play path's own probe handles it.
func virtualDefaultProbeCandidate(row *models.MediaFile, source *models.MediaFile, streams []VirtualPlaybackStream, allowPersistedSelected bool) *VirtualPlaybackStream {
	if row == nil || source == nil {
		return nil
	}
	target := strings.TrimSpace(row.FilePath)
	if target == "" {
		return nil
	}
	if source.FilePath == target {
		return nil
	}
	for i := range streams {
		if streams[i].URI == target && streams[i].OwnerInstallationID == row.VirtualOwnerInstallationID {
			return &streams[i]
		}
	}
	if !allowPersistedSelected {
		return nil
	}
	// A selected row can be a persisted candidate from an earlier provider
	// listing. The target read authorized its content, episode, owner, and
	// library; its own concrete result id names the bytes to probe. The
	// discovery source only supplies the scheduling hand-off, so a chain started
	// by a sibling source may still probe this row's stored URL. A neutral row
	// (no result id) is never probed speculatively; the default path keeps the
	// exact-listing requirement above.
	return virtualPersistedSelectedProbeCandidate(row)
}

// virtualPersistedSelectedProbeCandidate resolves the bytes of one persisted,
// authorized selected row. Both entry points that may probe a selection use
// it: a discovery listing that no longer names the selected URI, and a
// watch-detail selection that has no discovery listing at all. A neutral group
// row (no result id) is never probed speculatively — it may resolve to a
// sibling candidate, and the play path's own probe handles it.
func virtualPersistedSelectedProbeCandidate(row *models.MediaFile) *VirtualPlaybackStream {
	if row == nil {
		return nil
	}
	if virtualResultCandidateID(strings.TrimSpace(row.FilePath)) == "" {
		return nil
	}
	return virtualPlaybackStreamFromFile(row)
}

func virtualPlaybackStreamFromFile(row *models.MediaFile) *VirtualPlaybackStream {
	if row == nil {
		return nil
	}
	stream := &VirtualPlaybackStream{
		URI:                 row.FilePath,
		ProviderURL:         row.ResolvedURL,
		ProviderExpiresAt:   row.ResolvedURLExpiresAt,
		OwnerInstallationID: row.VirtualOwnerInstallationID,
		Resolution:          row.Resolution,
		CodecVideo:          row.CodecVideo,
		CodecAudio:          row.CodecAudio,
		Container:           row.Container,
		Bitrate:             row.Bitrate,
		FileSize:            row.FileSize,
		RequestHeaders:      maps.Clone(row.ProviderRequestHeaders),
	}
	for _, track := range row.AudioTracks {
		if track.Language != "" {
			stream.AudioLanguages = append(stream.AudioLanguages, track.Language)
		}
	}
	for _, track := range row.SubtitleTracks {
		if track.Language != "" {
			stream.SubtitleLanguages = append(stream.SubtitleLanguages, track.Language)
		}
	}
	return stream
}
