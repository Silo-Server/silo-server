package embeddedsub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
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
}

const (
	// DefaultConcurrency is how many reads ReadPackets keeps in flight.
	// Network storage serves small random reads in parallel; more doesn't
	// shorten the slowest one.
	DefaultConcurrency = 16
	// DefaultLookback covers how long nearly every subtitle shows.
	DefaultLookback = 30 * time.Second
	maxLookback     = 10 * time.Minute
)

// Result is what ReadPackets read.
type Result struct {
	// Packets are in the order the file stores them, which is the order
	// FFmpeg demuxes them in, and for subtitles is presentation order.
	Packets []Packet
	Stats   ReadStats
}

// ReadPackets returns the packets of subtitle track number (see
// Index.Track) that show at any time in [from, to) on the container's
// timeline: packets that start before from are included while they still
// show. A packet without a known duration shows until the track's next
// packet starts. ReadPackets(ctx, path, idx, n, WholeFrom, WholeTo, opts)
// reads the whole track.
//
// It reads only the clusters' headers and the track's blocks, never the
// rest of the file, with up to opts.Concurrency reads in flight. A track
// without cues returns ErrNoCues, one whose cues are known to leave out
// packets ErrIncompleteCues, one the reader can't decode ErrUnsupported, and
// a file that changed since idx was read ErrFileChanged. When some blocks
// the index points at are unreadable it returns the packets it read along
// with a *DamagedError.
//
// When ctx ends ReadPackets returns at once; reads already issued finish in
// the background.
func ReadPackets(ctx context.Context, path string, idx *Index, number uint64, from, to time.Duration, opts ReadOptions) (*Result, error) {
	start := time.Now()
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
	f, info, err := openFile(path)
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
	r := newFileReader(ctx, f, concurrency)
	var res *Result
	switch idx.Container {
	case ContainerMatroska:
		res, err = readMatroskaPackets(ctx, r, idx, t, from, to, opts, concurrency)
	case ContainerMP4:
		res, err = readMP4Packets(ctx, r, t, from, to, concurrency)
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
func selectCues(cues []Cue, done []bool, from, to, lookback time.Duration) []int {
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
			sel = append(sel, i)
		}
	}
	return sel
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
// a duration shows until the next cue time.
func windowPackets(packets []Packet, times []time.Duration, from, to time.Duration) []Packet {
	return slices.DeleteFunc(packets, func(p Packet) bool {
		end := p.End()
		if p.Duration <= 0 {
			end = nextStart(times, p.PTS)
		}
		return !overlaps(p.PTS, end, from, to)
	})
}

// runJobs calls fn for each of n jobs, up to concurrency at once, and
// returns the results in job order. It returns as soon as ctx ends; jobs
// already running finish in the background.
func runJobs[T any](ctx context.Context, n, concurrency int, fn func(int) T) ([]T, error) {
	results := make([]T, n)
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(concurrency, n) {
		wg.Go(func() {
			for j := range next {
				if ctx.Err() == nil {
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
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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
