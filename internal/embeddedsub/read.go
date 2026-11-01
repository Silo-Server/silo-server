package embeddedsub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// ReadOptions tunes ReadPackets. The zero value uses the defaults.
type ReadOptions struct {
	// Concurrency bounds the reads in flight; 0 means DefaultConcurrency.
	Concurrency int
	// Lookback is how far before a window's start ReadPackets looks for
	// packets still showing, for a track whose cues don't all carry a
	// CueDuration; 0 means DefaultLookback. A block found to last longer
	// widens it, up to maxLookback. A track whose cues all have one uses
	// the longest of them instead.
	Lookback time.Duration
	// AllowIncomplete reads a track whose cues disagree with its
	// NUMBER_OF_FRAMES tag instead of returning ErrIncompleteCues.
	AllowIncomplete bool
	// MaxBytes bounds the packet data and blocks one call holds, plus
	// conservative estimates for cue/job and packet metadata; 0 means
	// DefaultMaxBytes. A read that would hold more returns ErrTooLarge.
	MaxBytes int64
	// Background, when set and not raised, makes this a read nobody waits
	// on: its I/O goes at the idle priority on Linux, with
	// BackgroundConcurrency reads in flight. Raising it while the read runs
	// (Priority.Raise) gives the rest of the read normal priority and
	// Concurrency. Network filesystems ignore I/O priority.
	Background *Priority
}

const (
	// DefaultConcurrency is how many reads ReadPackets keeps in flight.
	// Network storage serves small random reads in parallel; more doesn't
	// shorten the slowest one.
	DefaultConcurrency = 16
	// BackgroundConcurrency is how many reads a background read keeps in
	// flight until it is raised.
	BackgroundConcurrency = 4
	// DefaultLookback covers how long nearly every subtitle shows.
	DefaultLookback = 30 * time.Second
	maxLookback     = 10 * time.Minute
	// DefaultMaxBytes is far above a whole film's decompressed PGS track,
	// the largest subtitle tracks there are.
	DefaultMaxBytes = 256 << 20
	// WindowMaxBytes is a MaxBytes for windowed reads. A window of seconds
	// to minutes holds kilobytes of text and a few megabytes of bitmaps, so
	// a crafted file can't make each one hold what a whole read may.
	WindowMaxBytes = 16 << 20
	// Metadata weights cover slice growth, job/results, cluster maps and
	// channels, and copies of Packet structs retained while gathering output.
	cueMetadataBytes    = 1024
	packetMetadataBytes = 512
)

// budget counts the bytes a read may still hold.
type budget struct {
	left atomic.Int64
	// full is what take reports when the budget can't cover a claim; nil is
	// ErrTooLarge.
	full error
	// parent, when set, is charged with every claim as well; held is what b
	// holds of it, which release returns.
	parent *budget
	held   atomic.Int64
}

func newBudget(maxBytes int64) *budget {
	b := &budget{}
	b.left.Store(cmp0(maxBytes, DefaultMaxBytes))
	return b
}

// newBudgetWithin returns a budget of maxBytes whose claims parent covers too.
func newBudgetWithin(maxBytes int64, parent *budget) *budget {
	b := newBudget(maxBytes)
	b.parent = parent
	return b
}

// take claims n bytes, or reports why it can't and claims none: ErrTooLarge
// past b's own bound, or what a parent past its bound reports.
func (b *budget) take(n int) error {
	if b.left.Add(-int64(n)) < 0 {
		b.left.Add(int64(n))
		if b.full != nil {
			return b.full
		}
		return ErrTooLarge
	}
	if b.parent != nil {
		if err := b.parent.take(n); err != nil {
			b.left.Add(int64(n))
			return err
		}
		b.held.Add(int64(n))
	}
	return nil
}

// give returns n bytes claimed for data no longer held.
func (b *budget) give(n int) {
	b.left.Add(int64(n))
	if b.parent != nil {
		b.held.Add(-int64(n))
		b.parent.give(n)
	}
}

// release returns to the parent everything b still holds of it, once what b
// counted is no longer being built.
func (b *budget) release() {
	if b != nil && b.parent != nil {
		b.parent.give(int(b.held.Swap(0)))
	}
}

// Result is what ReadPackets read.
type Result struct {
	// Packets are in the order the file stores them, which is the order
	// FFmpeg demuxes them in, and for subtitles is presentation order.
	Packets []Packet
	Stats   ReadStats
}

const maxPacketReads = 8

var errPacketReadBusy = errors.New("embeddedsub: packet read limit reached")

type packetReadKey struct {
	source   cacheKey
	number   uint64
	from, to time.Duration
}

// packetReads counts operations until all their workers finish, including
// reads whose callers have canceled. One file version's window holds one slot.
var packetReads = struct {
	sync.Mutex
	running map[packetReadKey]struct{}
}{running: map[packetReadKey]struct{}{}}

// runPacketRead lets the caller cancel without releasing the operation's
// slot or key while any of its packet workers still hold buffers or a file.
func runPacketRead(ctx context.Context, key packetReadKey, read func() (*Result, error)) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	packetReads.Lock()
	_, running := packetReads.running[key]
	if running || len(packetReads.running) >= maxPacketReads {
		packetReads.Unlock()
		return nil, errPacketReadBusy
	}
	packetReads.running[key] = struct{}{}
	packetReads.Unlock()
	type result struct {
		res *Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		var out result
		defer func() {
			if r := recover(); r != nil {
				out = result{err: fmt.Errorf("embeddedsub: packet read panicked: %v", r)}
			}
			packetReads.Lock()
			delete(packetReads.running, key)
			packetReads.Unlock()
			done <- out
		}()
		out.res, out.err = read()
	}()
	select {
	case out := <-done:
		return out.res, out.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ReadPackets returns the packets of subtitle track number (see
// Index.Track) that show at any time in [from, to) on the container's
// timeline: packets that start before from are included while they still
// show. A packet without a known duration shows until the track's next
// packet starts. ReadPackets(ctx, path, idx, n, WholeFrom, WholeTo, opts)
// reads the whole track.
//
// It reads only the clusters' headers and the track's blocks, never the
// rest of the file, with up to opts.Concurrency reads in flight, and holds
// at most opts.MaxBytes of packet data (ErrTooLarge). A track without cues
// returns ErrNoCues, one whose cues are known to leave out packets
// ErrIncompleteCues, one the reader can't decode ErrUnsupported, and a file
// that changed since idx was read ErrFileChanged. When some blocks the index
// points at are unreadable it returns the packets it read along with a
// *DamagedError. The frames of a laced block come together or not at all.
//
// Without CueDurations, a window finds a packet that started before it only
// within the lookback (see ReadOptions.Lookback): a sign that shows for
// minutes may be missed.
//
// When ctx ends ReadPackets returns at once; reads already issued finish in
// the background and keep their slot until every worker exits. At most eight
// packet operations run process-wide, with one per file version/track/window;
// a saturated limit or an already running window fails the new call promptly.
func ReadPackets(ctx context.Context, path string, idx *Index, number uint64, from, to time.Duration, opts ReadOptions) (*Result, error) {
	if idx == nil {
		return nil, errors.New("embeddedsub: nil index")
	}
	t, ok := idx.Track(number)
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrTrackNotFound, number)
	}
	if err := t.readable(opts); err != nil {
		return nil, err
	}
	key := packetReadKey{source: cacheKey{path: path, size: idx.Size, mtime: idx.ModTime.UnixNano()}, number: number, from: from, to: to}
	return runPacketRead(ctx, key, func() (*Result, error) {
		return readPackets(ctx, path, idx, t, from, to, opts)
	})
}

// readPackets keeps the descriptor and budget alive until all jobs have exited.
// Only runPacketRead may stop waiting for this operation on cancellation.
func readPackets(ctx context.Context, path string, idx *Index, t *Track, from, to time.Duration, opts ReadOptions) (*Result, error) {
	start := time.Now()
	f, info, err := openFile(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() != idx.Size || !info.ModTime().Equal(idx.ModTime) {
		return nil, ErrFileChanged
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	var src io.ReaderAt = f
	if opts.Background.background() {
		idle := newIdleReader(f, BackgroundConcurrency, opts.Background)
		defer idle.close()
		src = idle
	}
	r := newFileReader(ctx, src, concurrency)
	if opts.Background.background() {
		r.background(opts.Background, BackgroundConcurrency)
	}
	b := newBudget(opts.MaxBytes)
	var res *Result
	switch idx.Container {
	case ContainerMatroska:
		res, err = readMatroskaPackets(ctx, r, b, idx, t, from, to, opts, concurrency)
	case ContainerMP4:
		res, err = readMP4Packets(ctx, r, b, t, from, to, concurrency)
	default:
		return nil, ErrNotSupportedContainer
	}
	if res != nil {
		read := r.stats()
		res.Stats.Reads, res.Stats.Bytes = read.Reads, read.Bytes
		res.Stats.Wall = time.Since(start)
	}
	return res, err
}

// readable reports why ReadPackets can't read the track, if it can't.
func (t *Track) readable(opts ReadOptions) error {
	switch {
	case t.Unsupported != "":
		return fmt.Errorf("%w: %s", ErrUnsupported, t.Unsupported)
	case t.Completeness == CompletenessNone:
		return ErrNoCues
	case t.Completeness == CompletenessIncomplete && !opts.AllowIncomplete:
		return fmt.Errorf("%w: %d cues, %d frames", ErrIncompleteCues, len(t.Cues), t.FrameCount)
	}
	return nil
}

// selectCues returns the indexes of the unread cues whose packets may show
// in [from, to), given that none lasts longer than lookback beyond what its
// cue says. A cue without a duration is also taken when it is the latest to
// start by from: without a duration of its own, it shows until the next.
func selectCues(cues []Cue, done []bool, from, to, lookback time.Duration, b *budget) ([]int, error) {
	whole := from == WholeFrom && to == WholeTo
	latest := WholeFrom
	if !whole {
		for _, c := range cues {
			if c.Time <= from {
				latest = max(latest, c.Time)
			}
		}
	}
	earliest := satSub(from, lookback)
	var sel []int
	for i, c := range cues {
		if !done[i] && (whole || cueWanted(c, from, to, earliest, latest)) {
			if err := b.take(cueMetadataBytes); err != nil {
				return nil, err
			}
			sel = append(sel, i)
		}
	}
	return sel, nil
}

// cueWanted reports whether a cue's packet may show in [from, to): one
// whose cue has a duration by it, one without by starting at earliest or
// later, or at latest, the last start by from.
func cueWanted(c Cue, from, to, earliest, latest time.Duration) bool {
	if c.Time >= to {
		return false
	}
	if c.Duration > 0 {
		return satAdd(c.Time, c.Duration) > from
	}
	return c.Time >= earliest || c.Time == latest
}

// windowPackets keeps the packets that show in [from, to). A packet without
// a duration shows until the next cue time. The frames of a laced block are
// kept together: FFmpeg converts each with the frames after it in view.
func windowPackets(packets []Packet, times []time.Duration, from, to time.Duration) []Packet {
	shows := func(p Packet) bool {
		end := p.End()
		if p.Duration <= 0 {
			end = nextStart(times, p.PTS)
		}
		return overlaps(p.PTS, end, from, to)
	}
	laced := map[int64]bool{}
	for _, p := range packets {
		if p.laces > 1 && shows(p) {
			laced[p.pos] = true
		}
	}
	return slices.DeleteFunc(packets, func(p Packet) bool {
		if p.laces > 1 {
			return !laced[p.pos]
		}
		return !shows(p)
	})
}

// runJobs calls fn for each of n jobs, up to concurrency at once, and
// returns the results in job order. Cancellation stops new jobs, but this
// waits for running jobs so their buffers and file stay within runPacketRead's
// limit. A job that panics fails the call rather than the process.
func runJobs[T any](ctx context.Context, n, concurrency int, fn func(int) T) ([]T, error) {
	results := make([]T, n)
	next := make(chan int)
	var (
		wg       sync.WaitGroup
		panicked atomic.Pointer[error]
	)
	for range min(concurrency, n) {
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					err := fmt.Errorf("embeddedsub: read panicked: %v", r)
					panicked.CompareAndSwap(nil, &err)
					for range next {
						// Drain, so the feeder finishes.
					}
				}
			}()
			for j := range next {
				if ctx.Err() == nil && panicked.Load() == nil {
					results[j] = fn(j)
				}
			}
		})
	}
	go func() {
		defer close(next)
		for j := range n {
			select {
			case next <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := panicked.Load(); err != nil {
		return nil, *err
	}
	return results, nil
}

// sortPackets orders packets as the file stores them.
func sortPackets(packets []Packet) {
	slices.SortFunc(packets, func(a, b Packet) int {
		if c := cmp.Compare(a.pos, b.pos); c != 0 {
			return c
		}
		return cmp.Compare(a.lace, b.lace)
	})
}
