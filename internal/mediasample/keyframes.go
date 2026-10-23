package mediasample

import (
	"context"
	"io"
	"math"
	"os"
	"sort"
	"syscall"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

// A list entry that starts at its sample's time makes the concat demuxer seek
// to the keyframe before it and read every packet of every stream, video and
// all audio tracks, from that keyframe up to the sample, packets the decoder
// then skips. With keyframes several seconds apart that is most of the file.
// A Sheets list over a Matroska input instead starts each entry at the
// sample's keyframe, read from the file's Cues: the index the demuxer's seek
// uses, so the entry lands on the same keyframe and reads only it and the
// entry's span. Every frame then lies at or after its entry's inpoint, so
// its time places it back in the input exactly (see sourceSeconds).
//
// A sample whose keyframe is the previous sample's gets no entry: it would
// only decode the frame the cell before shows, and the HEVC decoder drops a
// keyframe sent twice in a row as a duplicate picture order count. Once the
// list's frames are in, such a sample shows the previous cell's frame, with
// its source time (see shareFrames), so the repair finds it repeating that
// cell and decodes its own moment.
//
// Reading fewer packets changes what an HEVC or H.264 decoder knows when an
// open-GOP keyframe arrives after a jump: it derives the keyframe's picture
// order count from the last picture whose header it parsed, skipped ones
// included. Across a gap longer than half the count's cycle (128 frames for
// x265's default) the derived counts run backwards, and the decoder passes on
// keyframes out of list order with rewritten times, or drops them as
// duplicates. Sample-time entries leave shorter gaps and fare better, so a
// keyframe list is abandoned, ffmpeg stopped, as soon as a frame comes from
// an earlier entry than the one before it or the decoder drops a keyframe
// (droppedKeyframe, which also covers H.264 keyframes whose reference
// marking names skipped pictures), and the samples are read again from their
// own times (see keyframeSheets). Closed-GOP keyframes start their counts afresh and never
// trip it.
//
// Samples before the first keyframe, and every sample of an input without
// usable Cues (other containers, a Matroska file without Cues or whose Cues
// leave out keyframes, or a read that fails or takes longer than
// keyframeLookupTimeout), start at their own time as before. MP4 inputs keep
// sample-time entries even with an index: an entry that starts exactly at an
// MP4 keyframe's time often seeks past it. Repairs always list sample times;
// they read from the keyframe on by design.

// keyframeLookupTimeout bounds the wait for a Sheets input's Cues. Reading
// them takes milliseconds; a read still blocked after this, as on stalled
// storage, is left behind and the run lists sample times.
const keyframeLookupTimeout = 10 * time.Second

// keyframeLookups bounds the lookups in flight across the process, including
// those left behind blocked, and so the memory parsing Cues takes, a few
// times their size up to keyframes.ReadMatroska's limit on them. A
// lookup takes milliseconds, so a few slots serve any number of trickplay
// workers. A run that finds every slot taken lists sample times without
// reading the Cues.
var keyframeLookups = make(chan struct{}, 4)

// lookupKeyframes returns the video keyframe times of the Matroska file at
// path, stopping between reads once ctx ends; tests replace it.
var lookupKeyframes = matroskaKeyframes

// sheetKeyframes returns the video keyframe times, on the container's
// timeline, of a Sheets list request's input, or nil when it has none to
// start entries at. Run calls it once, before any attempt and its timeout
// start, and stops waiting when ctx ends or keyframeLookupTimeout passes.
func sheetKeyframes(ctx context.Context, req Request) []float64 {
	if req.Sheets == nil || req.Samples == nil || req.Samples.ReadThrough || ctx.Err() != nil {
		return nil
	}
	select {
	case keyframeLookups <- struct{}{}:
	default:
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, keyframeLookupTimeout)
	defer cancel()
	lookup := lookupKeyframes
	found := make(chan []float64, 1)
	go func() {
		defer func() { <-keyframeLookups }()
		found <- lookup(ctx, req.Input)
	}()
	select {
	case keyframes := <-found:
		// A lookup can finish just as the wait ends; the run has moved on.
		if ctx.Err() != nil {
			return nil
		}
		return keyframes
	case <-ctx.Done():
		return nil
	}
}

// matroskaKeyframes reads the keyframe times of the Matroska file at path
// from its Cues and a few clusters' block headers (keyframes.ReadMatroska),
// or returns nil. The file opens without blocking, so a FIFO in its place
// cannot hold the lookup, and only a regular file is read.
func matroskaKeyframes(ctx context.Context, path string) []float64 {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	index, err := keyframes.ReadMatroska(contextReaderAt{ctx: ctx, r: file}, info.Size())
	if err != nil {
		return nil
	}
	return index.Keyframes
}

// contextReaderAt stops reading once ctx ends.
type contextReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (c contextReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.ReadAt(p, off)
}

// listPlan is the list a Sheets run reads: its entries, the input start time
// their inpoints are offset by, and for each sample the index of the entry
// its frame comes from, or -1 for a sample that shows the previous sample's
// frame.
type listPlan struct {
	inputStart float64
	entries    []listEntry
	entry      []int
}

// samplePlan starts every sample's entry at its own time.
func samplePlan(seconds []float64, inputStart float64) listPlan {
	plan := listPlan{inputStart: inputStart, entries: sampleEntries(seconds, inputStart), entry: make([]int, len(seconds))}
	for i := range plan.entry {
		plan.entry[i] = i
	}
	return plan
}

// keyframePlan starts each sample's entry at the last of keyframes at or
// before the sample's inpoint, and leaves out a sample whose keyframe is the
// previous sample's. A sample before the first keyframe starts at its own
// time.
func keyframePlan(seconds []float64, inputStart float64, keyframes []float64) listPlan {
	plan := listPlan{inputStart: inputStart, entry: make([]int, len(seconds))}
	previous := -1
	for i, at := range seconds {
		inpoint := math.Max(0, at+inputStart)
		// The list writes inpoints in whole microseconds, so the seek lands
		// on a keyframe up to that rounding after the exact time.
		k := sort.SearchFloat64s(keyframes, math.Round(inpoint*1e6)/1e6+1e-9) - 1
		if k >= 0 && k == previous {
			plan.entry[i] = -1
			continue
		}
		if k >= 0 {
			inpoint = math.Max(0, ceilMicroseconds(keyframes[k]))
		}
		previous = k
		plan.entry[i] = len(plan.entries)
		plan.entries = append(plan.entries, listEntry{sample: at, inpoint: inpoint})
	}
	return plan
}

// ceilMicroseconds rounds a keyframe time up to the microseconds an inpoint
// is written with, so the inpoint never falls before the keyframe and seeks
// to the one before it. A time already whole in microseconds, as a Matroska
// file's default millisecond timestamps are, stays as it is.
func ceilMicroseconds(seconds float64) float64 {
	return math.Ceil(seconds*1e6-1e-3) / 1e6
}
