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
// A play never waits on the scan. Its verdict is persisted for later plans:
// a copy-unsafe source gets one stream copy per recovery, and a replan after
// that copy failed encodes it instead of trying another copy delivery. The
// verdict never withdraws a copy that is playing: decoders that apply in-band
// parameter sets play it fine, and the ones that do not report the failure
// that starts the replan.
type CopySafetyRace struct {
	scanner CopySafetyScanner
	files   CopySafetyFileLoader
	// inFlight keeps one goroutine per file. The scanner's own singleflight
	// already collapses concurrent scans, but every start, replan and watch-page
	// load for a popular file would otherwise stack a goroutine that does
	// nothing but wait on it.
	inFlight sync.Map // file ID -> struct{}
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

// RaceScan resolves the copy-safety verdict for fileID in the background. It
// returns immediately, and does nothing when the verdict is already known or
// the file is not H.264. A request that lands while a scan for the same file is
// running is dropped: that scan already answers it.
//
// The caller's request context is deliberately not used: the scan outlives the
// request that noticed the verdict was missing, and the whole point is that no
// client ever waits on it.
func (r *CopySafetyRace) RaceScan(fileID int) {
	if r == nil || fileID <= 0 {
		return
	}
	if _, running := r.inFlight.LoadOrStore(fileID, struct{}{}); running {
		return
	}
	go func() {
		defer r.inFlight.Delete(fileID)
		// The slot is taken before the scan's own deadline starts: time spent
		// queueing behind other files is not time the scan was given to run.
		r.acquireSlot()
		defer r.releaseSlot()
		r.scan(fileID)
	}()
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
		// The replacement's verdict is unknown again, and the next start,
		// replan or watch-page load for it asks for a fresh race.
		slog.InfoContext(ctx, "discarding a video copy-safety verdict for a superseded generation of the file",
			"component", "playback", "file_id", fileID)
	case multi:
		slog.InfoContext(ctx, "video copy-safety scan found conflicting parameter sets; a failed copy of this source now falls back to an encode",
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
