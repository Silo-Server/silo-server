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
	// row, never from ffmpeg, and retries a write that never landed. The race
	// calls it for that retry: replans on every replica read the verdict from
	// the row, so a verdict only this process's memo holds helps nobody.
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
// A play never waits on the scan. Its verdict is persisted for the download
// policy, which never remuxes a copy-unsafe source because a download cannot
// recover, and for web clients treated as strict decoders, which get the
// encode on the first plan. For every other client the planner treats such a
// source like any other, and the verdict never withdraws a copy that is
// playing: decoders that apply in-band parameter sets play it fine, and the
// ones that do not report the failure that starts the replan.
type CopySafetyRace struct {
	scanner CopySafetyScanner
	files   CopySafetyFileLoader
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
func NewCopySafetyRace(scanner CopySafetyScanner, files CopySafetyFileLoader) *CopySafetyRace {
	if scanner == nil || files == nil {
		return nil
	}
	return &CopySafetyRace{
		scanner: scanner,
		files:   files,
		slots:   make(chan struct{}, copySafetyScanConcurrency),
		timeout: copySafetyScanTimeout,
	}
}

// copySafetyRaceState is the per-file entry in inFlight. It exists so a request
// that arrives while a scan is running is remembered rather than dropped.
//
// Dropping it loses a verdict when the file is rewritten in place mid-scan: the
// running scan reads the old generation and comes back stale without persisting
// anything, and the request for the replacement was swallowed by the dedupe, so
// the replacement stays unscanned until some later play asks again.
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
// a second scan, and is not dropped either: it is folded into one follow-up
// pass the running goroutine makes before it retires. That pass normally costs
// no ffmpeg at all, because the verdict is by then persisted and the pass takes
// the known-verdict path; it scans again only when the first scan left the
// verdict unknown (failed, or read a superseded generation of the file).
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
		// Nothing left to scan. A verdict this process reached but failed to
		// write is retried here, so the row the replans read catches up.
		r.scanner.KnownCopySafetyVerdict(ctx, file)
		return
	}

	multi, stale, err := r.scanner.ScanCopySafety(ctx, file)
	switch {
	case err != nil:
		// An inconclusive scan is not evidence of anything. Nothing is
		// persisted, and a later request retries.
		slog.WarnContext(ctx, "video copy-safety scan failed",
			"component", "playback", "file_id", fileID, "error", err)
	case stale:
		// The scan read a generation of the file the row has since moved past.
		// The replacement's verdict is unknown again. A request for it that
		// arrived during this scan gets the follow-up pass; otherwise the next
		// start, replan or watch-page load asks for a fresh race.
		slog.InfoContext(ctx, "discarding a video copy-safety verdict for a superseded generation of the file",
			"component", "playback", "file_id", fileID)
	case multi:
		slog.InfoContext(ctx, "video copy-safety scan found conflicting parameter sets; downloads of this source are not remuxed",
			"component", "playback", "file_id", fileID)
	}
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
