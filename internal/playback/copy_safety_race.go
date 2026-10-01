package playback

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// copySafetyScanTimeout bounds one asynchronous multi-PPS scan. The scan reads
// the opening seconds of the file, which on cold remote storage is dominated by
// the read rather than the demux; a minute is generous for that and still
// guarantees the goroutine cannot outlive the session it was started for by
// much. It is deliberately not the request timeout: nothing about this work
// belongs to the HTTP request that triggered it.
const copySafetyScanTimeout = time.Minute

// copySafetyScanConcurrency caps how many copy-safety scans run at once across
// the whole replica. Per-file dedupe collapses the repeat requests for one
// popular file, but nothing bounds the number of *distinct* unknown files a
// burst of watch-page loads can name, and each one costs an ffmpeg process
// reading the opening seconds off remote storage. Excess races block their
// goroutine on the semaphore rather than being dropped: the scan is cheap to
// defer and must still happen, goroutines are cheap, ffmpeg is not.
const copySafetyScanConcurrency = 4

// copySafetyRecheckTimeout bounds the verdict re-read that follows a failed
// scan. It cannot inherit the scan's context: a scan that failed because its
// deadline expired leaves that context already dead, which is exactly the case
// the re-read exists for.
const copySafetyRecheckTimeout = 15 * time.Second

// CopySafetyScanner is the scanner-side half of the race: it decides whether a
// file still needs the H.264 multi-PPS scan, runs it, and reports what this
// process already knows without scanning. *scanner.PlaybackProbeEnsurer
// implements it.
type CopySafetyScanner interface {
	NeedsCopySafetyScan(file *models.MediaFile) bool
	// ScanCopySafety resolves an unknown verdict. Its second result reports
	// that the verdict was superseded — computed from a generation of the file
	// the row no longer holds — which is neither an error nor something any
	// caller may act on.
	ScanCopySafety(ctx context.Context, file *models.MediaFile) (multi bool, stale bool, err error)
	// KnownCopySafetyVerdict answers from the process memo or the persisted
	// row, never from ffmpeg, and retries a write that never landed.
	//
	// It is required rather than probed for: an unsafe verdict whose write
	// failed lives only in the scanning process's memo, and the row — the only
	// other place to look — cannot distinguish that from "never scanned". A
	// racer that could not ask this question would leave the sessions on the
	// condemned route with nothing left to withdraw them.
	KnownCopySafetyVerdict(ctx context.Context, file *models.MediaFile) (multi bool, known bool)
}

// CopySafetyFileLoader loads the media file a race was requested for.
type CopySafetyFileLoader interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
}

// CopySafetyRace runs the H.264 copy-safety scan out of band, after a plan that
// stream-copies video has already been handed to a client (or a watch page has
// been rendered for a file no session exists for yet).
//
// This is the asynchronous half of optimistic remuxing: an unknown verdict no
// longer blocks a play, so it has to be resolved behind the play instead. A
// multi-PPS verdict is both persisted — every later plan for the file excludes
// the copy route deterministically — and pushed at whatever sessions are live
// on a copy route by the time it lands.
type CopySafetyRace struct {
	scanner  CopySafetyScanner
	files    CopySafetyFileLoader
	notifier *CopySafetyNotifier
	// inFlight keeps one goroutine per file. The scanner's own singleflight
	// already collapses concurrent scans, but every start, replan and watch-page
	// load for a popular file would otherwise stack a goroutine that does
	// nothing but wait on it.
	inFlight sync.Map // file ID -> *copySafetyRaceState
	// slots is the replica-wide scan semaphore. A goroutine holds its per-file
	// inFlight entry while it waits for a slot, so queueing never lets a second
	// goroutine for the same file through.
	slots   chan struct{}
	timeout time.Duration
}

// NewCopySafetyRace returns a racer, or nil when it has nothing to scan with. A
// nil racer is safe to call.
func NewCopySafetyRace(scanner CopySafetyScanner, files CopySafetyFileLoader, notifier *CopySafetyNotifier) *CopySafetyRace {
	if scanner == nil || files == nil {
		return nil
	}
	return &CopySafetyRace{
		scanner:  scanner,
		files:    files,
		notifier: notifier,
		slots:    make(chan struct{}, copySafetyScanConcurrency),
		timeout:  copySafetyScanTimeout,
	}
}

// copySafetyRaceState is the per-file entry in inFlight. It exists so a request
// that arrives while a scan is running is remembered rather than dropped.
//
// Dropping it was wrong in one specific, reachable way: the sessions a pass
// notifies are the ones that exist when it runs, and the notifier excludes the
// sessions it disposed of from its own late sweep. A replan that commits a
// replacement stream-copy while the scan is in flight therefore produces a
// session no pass will ever look at — its race request was swallowed by the
// dedupe, and the plan it is running was never considered by the pass that
// preceded it.
type copySafetyRaceState struct {
	mu sync.Mutex
	// recheck records that somebody asked for this file while the owner was
	// mid-pass, so the owner owes one more pass before it lets go.
	recheck bool
	// done marks the owner as retired and its map entry as already removed;
	// a request that sees it must store a fresh state instead.
	done bool
}

// RaceScan resolves the copy-safety verdict for fileID in the background. It
// returns immediately, and does nothing when the verdict is already known or
// the file is not H.264.
//
// A request that lands while a scan for the same file is running does not start
// a second scan — and is not dropped either: it is folded into one follow-up
// pass the running goroutine makes before it retires. That pass normally costs
// no ffmpeg at all (the verdict it would scan for is by then memoized or
// persisted, so it takes the known-verdict path) and exists to re-examine the
// sessions that appeared while the scan ran.
//
// The caller's request context is deliberately not used: the scan outlives the
// request that noticed the verdict was missing, and the whole point is that no
// client ever waits on it.
func (r *CopySafetyRace) RaceScan(fileID int) {
	if r == nil || fileID <= 0 {
		return
	}
	for {
		actual, running := r.inFlight.LoadOrStore(fileID, &copySafetyRaceState{})
		state, _ := actual.(*copySafetyRaceState)
		if state == nil {
			return
		}
		if !running {
			go r.runRace(fileID, state)
			return
		}
		state.mu.Lock()
		if !state.done {
			state.recheck = true
			state.mu.Unlock()
			return
		}
		state.mu.Unlock()
		// The owner retired between the load and the lock. It deletes its map
		// entry before marking itself done, both under this lock, so observing
		// done proves the entry is gone and the next LoadOrStore installs a
		// fresh state: this loop runs at most twice.
	}
}

// runRace owns one file's races until nothing is left asking for it.
func (r *CopySafetyRace) runRace(fileID int, state *copySafetyRaceState) {
	// The slot is taken before the scan's own deadline starts: time spent
	// queueing behind other files is not time the scan was given to run.
	r.acquireSlot()
	defer r.releaseSlot()
	for {
		r.scan(fileID)

		state.mu.Lock()
		if state.recheck {
			state.recheck = false
			state.mu.Unlock()
			continue
		}
		// Retire under the lock, deleting first: a request that already holds
		// this state can then tell, from done alone, that it has to install a
		// new one.
		r.inFlight.Delete(fileID)
		state.done = true
		state.mu.Unlock()
		return
	}
}

func (r *CopySafetyRace) acquireSlot() {
	if r.slots == nil {
		return
	}
	r.slots <- struct{}{}
}

func (r *CopySafetyRace) releaseSlot() {
	if r.slots == nil {
		return
	}
	<-r.slots
}

func (r *CopySafetyRace) scan(fileID int) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = copySafetyScanTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	file, err := r.files.GetByID(ctx, fileID)
	if err != nil || file == nil {
		if err != nil {
			slog.WarnContext(ctx, "video copy-safety race could not load the file",
				"component", "playback", "file_id", fileID, "error", err)
		}
		return
	}
	if !r.scanner.NeedsCopySafetyScan(file) {
		// Nothing left to scan, but that is not the same as nothing to do. The
		// verdict may have been reached by another replica between this race
		// being requested and the file being loaded: that replica notified its
		// own sessions and has no way to reach ours, so an unsafe verdict has to
		// be applied locally even though no scan runs here. It may equally have
		// been reached by *this* process and failed to persist, which is why the
		// question goes to the scanner (memo first, then row) rather than to the
		// row alone — the row cannot tell an unpersisted verdict from an unknown
		// one. A known-safe verdict is silent, as always.
		//
		// This closes the window for sessions this replica raced against another
		// replica's write. It is not distributed invalidation: a verdict that
		// lands after every replica has stopped racing still reaches only the
		// replica that reached it. Pushing invalidations across replicas —
		// Redis-backed, like the other cross-replica playback signals — is
		// follow-up work.
		r.notifyKnownUnsafe(ctx, file)
		return
	}

	multi, stale, err := r.scanner.ScanCopySafety(ctx, file)
	if err != nil {
		// An inconclusive scan is not evidence of anything. Nothing is persisted
		// (the scanner only records a verdict it reached), live sessions keep
		// playing the route they were given, and a later request retries. The
		// old behavior — treating a failed scan as copy-unsafe — belonged to a
		// world where the scan ran before playback started.
		slog.WarnContext(ctx, "video copy-safety scan failed",
			"component", "playback", "file_id", fileID, "error", err)
		// Inconclusive here does not mean inconclusive everywhere. Another
		// replica may have persisted an unsafe verdict while this scan was
		// failing, and that verdict now suppresses every later local scan of the
		// file (NeedsCopySafetyScan reads the row) — so the sessions this replica
		// owns would wait for an unrelated future race to withdraw their route.
		// Re-reading the row is the one thing that can still resolve them.
		r.recheckPersistedVerdict(ctx, fileID)
		return
	}
	if stale {
		// The scan read a generation of the file the row has since moved past —
		// it was rewritten in place while ffmpeg was working. The verdict is
		// about bytes nobody is serving, so it neither persists nor withdraws
		// anything: notifying on it would tear down sessions playing the
		// replacement over evidence from the file it replaced. The replacement's
		// own verdict is unknown again, and the next start, replan or revival
		// for it asks for a fresh race.
		slog.InfoContext(ctx, "discarding a video copy-safety verdict for a superseded generation of the file",
			"component", "playback", "file_id", fileID)
		return
	}
	if !multi {
		return
	}

	slog.InfoContext(ctx, "video copy-safety scan disqualified the stream-copy route",
		"component", "playback", "file_id", fileID)
	r.notifier.VideoCopyUnsafe(ctx, fileID)
}

// recheckPersistedVerdict re-reads the row after a local scan failed and applies
// an unsafe verdict another replica reached in the meantime. A read that fails,
// or a row with no valid verdict on it, changes nothing: inconclusive stays
// inconclusive.
func (r *CopySafetyRace) recheckPersistedVerdict(ctx context.Context, fileID int) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copySafetyRecheckTimeout)
	defer cancel()

	file, err := r.files.GetByID(ctx, fileID)
	if err != nil || file == nil {
		if err != nil {
			slog.WarnContext(ctx, "video copy-safety verdict re-read after a failed scan could not load the file",
				"component", "playback", "file_id", fileID, "error", err)
		}
		return
	}
	r.notifyKnownUnsafe(ctx, file)
}

// notifyKnownUnsafe pushes the withdrawal for a file this replica can already
// call copy-unsafe without scanning — from the persisted row, or from a verdict
// this process reached whose write never landed. A known-safe or unresolved
// file is silent, as always.
func (r *CopySafetyRace) notifyKnownUnsafe(ctx context.Context, file *models.MediaFile) {
	if r == nil || file == nil {
		return
	}
	multi, known := r.scanner.KnownCopySafetyVerdict(ctx, file)
	if !known || !multi {
		return
	}
	slog.InfoContext(ctx, "applying a copy-unsafe verdict that needed no scan",
		"component", "playback", "file_id", file.ID)
	r.notifier.VideoCopyUnsafe(ctx, file.ID)
}

// VideoCopyUnsafeKnown reports whether this replica can already say the file
// cannot be video stream-copied, without running ffmpeg and without waiting on
// anything. It is what the serve paths gate a revived stream-copy on: the
// persisted row alone would miss a verdict this process reached but failed to
// write, which is exactly the case where nothing else is left to catch it.
func (r *CopySafetyRace) VideoCopyUnsafeKnown(ctx context.Context, file *models.MediaFile) bool {
	if r == nil || file == nil {
		return false
	}
	multi, known := r.scanner.KnownCopySafetyVerdict(ctx, file)
	return known && multi
}

// RaceScanForPlan starts a race only when the plan actually stream-copies video
// for this file. Callers on the playback start and replan paths use it so the
// route test lives in one place.
func (r *CopySafetyRace) RaceScanForPlan(fileID int, plan *PlanV3) {
	if r == nil || plan == nil {
		return
	}
	switch plan.Delivery {
	case DeliveryRemuxHLSV3, DeliveryRemuxProgressiveV3:
	default:
		return
	}
	r.RaceScan(fileID)
}
