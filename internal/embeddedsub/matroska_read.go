package embeddedsub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// Read sizes for blocks. A text subtitle block is well under a kilobyte, so
// one read of blockWindow nearly always holds it; a larger one (a PGS
// display set) takes one more read for the rest.
const (
	blockWindow = 4 << 10
	// clusterHeadSize covers a cluster's header, a CRC-32 and its
	// Timestamp; clusterHeadMax bounds the search when other elements come
	// first.
	clusterHeadSize = 64
	clusterHeadMax  = 4 << 10
	// nearClusterStart is how close to its cluster's start a block must be
	// for one read to fetch both the cluster's head and the block.
	nearClusterStart = 12 << 10
	maxBlockSize     = maxPacketSize
	// scanWindow and maxClusterChildren bound walking a cluster for blocks
	// whose cues have no CueRelativePosition.
	scanWindow         = 64 << 10
	maxClusterChildren = 100_000
	// minClusterHeader is the shortest cluster header: its 4-byte ID and a
	// 1-byte size.
	minClusterHeader = 5
)

// clusterHead is what a block's timestamp and position need from its
// cluster, read once per cluster.
type clusterHead struct {
	ready chan struct{}
	// dataStart and dataEnd are file offsets; dataEnd is the segment's end
	// for an unknown-size cluster.
	dataStart, dataEnd int64
	// timestamp is the cluster's Timestamp in ticks.
	timestamp int64
	err       error
}

// matroskaRead is one ReadPackets call on a Matroska file.
type matroskaRead struct {
	r      *fileReader
	budget *budget
	idx    *Index
	t      *Track
	mu     sync.Mutex
	heads  map[int64]*clusterHead
	// scanned counts the clusters walked for cues without a relative
	// position.
	scanned int
}

// job is one block read for a cue with a relative position, or one cluster
// walk for the cues of a cluster without one.
type job struct {
	cues []int
	scan bool
}

type jobResult struct {
	packets []Packet
	damage  []error
	err     error
}

func readMatroskaPackets(ctx context.Context, r *fileReader, b *budget, idx *Index, t *Track, from, to time.Duration, opts ReadOptions, concurrency int) (*Result, error) {
	whole := from == WholeFrom && to == WholeTo
	lookback := max(t.maxCueDuration, cmp0(opts.Lookback, DefaultLookback))
	if t.allCueDurations {
		lookback = t.maxCueDuration
	}
	// One visited byte per cue, plus int64 cue times for window filtering.
	metadata := len(t.Cues)
	if !whole {
		metadata += 8 * len(t.Cues)
	}
	if err := b.take(metadata); err != nil {
		return nil, err
	}
	m := &matroskaRead{r: r, budget: b, idx: idx, t: t, heads: map[int64]*clusterHead{}}
	done := make([]bool, len(t.Cues))
	res := &Result{}
	var damage []error
	for {
		sel, err := selectCues(t.Cues, done, from, to, lookback, b)
		if err != nil {
			return nil, err
		}
		if len(sel) == 0 {
			break
		}
		for _, i := range sel {
			done[i] = true
		}
		jobs := m.jobs(sel)
		results, err := runJobs(ctx, len(jobs), concurrency, func(j int) jobResult { return m.do(jobs[j]) })
		if err != nil {
			return nil, err
		}
		// longest is how long a block shows from its start: a laced
		// block's last frame ends where the block does.
		longest := time.Duration(0)
		starts := map[int64]time.Duration{}
		for _, jr := range results {
			if jr.err != nil {
				return nil, jr.err
			}
			damage = append(damage, jr.damage...)
			for _, p := range jr.packets {
				if p.lace == 0 {
					starts[p.pos] = p.PTS
				}
				if !p.inferredDuration {
					longest = max(longest, p.End()-starts[p.pos])
				}
			}
			res.Packets = append(res.Packets, jr.packets...)
		}
		res.Stats.Cues += len(sel)
		// A block can show longer than the lookback assumed; look again
		// from where it would have had to start.
		if whole || longest <= lookback || lookback >= maxLookback {
			break
		}
		lookback = min(longest, maxLookback)
	}
	if !whole {
		res.Packets = windowPackets(res.Packets, t.sortedCueTimes(), from, to)
	}
	sortPackets(res.Packets)
	m.mu.Lock()
	res.Stats.Clusters = len(m.heads)
	res.Stats.ScannedClusters = m.scanned
	m.mu.Unlock()
	if len(damage) > 0 {
		return res, &DamagedError{Cues: len(damage), First: damage[0]}
	}
	return res, nil
}

func cmp0[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// jobs groups the selected cues: one job per cue that says where its block
// is, one per cluster for those that don't.
func (m *matroskaRead) jobs(sel []int) []job {
	var (
		jobs    []job
		byScan  = map[int64]int{}
		cueList = m.t.Cues
	)
	for _, i := range sel {
		c := cueList[i]
		if c.RelativePosition >= 0 {
			jobs = append(jobs, job{cues: []int{i}})
			continue
		}
		if j, ok := byScan[c.ClusterPosition]; ok {
			jobs[j].cues = append(jobs[j].cues, i)
			continue
		}
		byScan[c.ClusterPosition] = len(jobs)
		jobs = append(jobs, job{cues: []int{i}, scan: true})
	}
	return jobs
}

func (m *matroskaRead) do(j job) jobResult {
	var (
		packets []Packet
		err     error
	)
	if j.scan {
		var damage []error
		packets, damage, err = m.scanCluster(j.cues)
		if err != nil {
			return jobResult{err: err}
		}
		return jobResult{packets: packets, damage: damage}
	}
	packets, err = m.readCue(j.cues[0])
	if err != nil {
		if errors.Is(err, errCorrupt) {
			c := m.t.Cues[j.cues[0]]
			return jobResult{damage: []error{fmt.Errorf("cue at %v, cluster %d: %w", c.Time, c.ClusterPosition, err)}}
		}
		return jobResult{err: err}
	}
	return jobResult{packets: packets}
}

// head returns the cluster's head entry; owner reports that the caller
// created it and must load it.
func (m *matroskaRead) head(pos int64) (h *clusterHead, owner bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h := m.heads[pos]; h != nil {
		return h, false
	}
	h = &clusterHead{ready: make(chan struct{})}
	m.heads[pos] = h
	return h, true
}

// loadHead reads the cluster at pos's header and Timestamp into h, using
// buf when it already holds the cluster's first bytes. It also runs on its
// own goroutine, so a panic becomes the head's error.
func (m *matroskaRead) loadHead(h *clusterHead, pos int64, buf []byte) {
	defer close(h.ready)
	defer func() {
		if r := recover(); r != nil {
			h.err = fmt.Errorf("embeddedsub: cluster head read panicked: %v", r)
		}
	}()
	h.dataStart, h.dataEnd, h.timestamp, h.err = m.readClusterHead(pos, buf)
}

func (m *matroskaRead) readClusterHead(pos int64, buf []byte) (dataStart, dataEnd, timestamp int64, err error) {
	for _, n := range []int{clusterHeadSize, clusterHeadMax} {
		if len(buf) < n {
			if buf, err = readUpTo(m.r, pos, n); err != nil {
				return 0, 0, 0, err
			}
		}
		dataStart, dataEnd, timestamp, err = parseClusterHead(buf, pos, m.idx.segmentEnd)
		if !errors.Is(err, errNeedMore) || len(buf) < n {
			break
		}
	}
	if errors.Is(err, errNeedMore) {
		err = corrupt("cluster at %d: no timestamp in its first %d bytes", pos, len(buf))
	}
	return dataStart, dataEnd, timestamp, err
}

var errNeedMore = errors.New("need more data")

// Cluster children that may come before its Timestamp.
const (
	idClusterPosition = 0xA7
	idPrevSize        = 0xAB
	idSilentTracks    = 0x5854
)

// parseClusterHead parses the start of a cluster: its header and
// Timestamp, skipping the CRC-32, Void and other elements a muxer may put
// first.
func parseClusterHead(b []byte, pos, segEnd int64) (dataStart, dataEnd, timestamp int64, err error) {
	id, size, hl, ok := matroska.ParseElementHeader(b)
	if !ok {
		if len(b) < matroska.MaxHeaderLen {
			return 0, 0, 0, corrupt("cluster at %d is cut off", pos)
		}
		return 0, 0, 0, corrupt("no element at %d", pos)
	}
	if id != matroska.IDCluster {
		return 0, 0, 0, corrupt("cue points at element %#x at %d, not a cluster", id, pos)
	}
	dataStart, dataEnd = pos+int64(hl), segEnd
	if size != matroska.UnknownSize && dataStart+size < segEnd {
		dataEnd = dataStart + size
	}
	for p := hl; ; {
		cid, csize, chl, ok := matroska.ParseElementHeader(b[p:])
		if !ok || csize == matroska.UnknownSize || int64(p+chl)+csize > int64(len(b)) {
			if ok && csize != matroska.UnknownSize && cid != matroska.IDClusterTimestamp {
				// Skippable, but its end is past what was read.
				return 0, 0, 0, errNeedMore
			}
			if !ok && len(b)-p >= matroska.MaxHeaderLen {
				return 0, 0, 0, corrupt("malformed cluster at %d", pos)
			}
			return 0, 0, 0, errNeedMore
		}
		switch cid {
		case matroska.IDClusterTimestamp:
			ts := matroska.Uint(b[p+chl : p+chl+int(csize)])
			if ts > math.MaxInt64/2 {
				return 0, 0, 0, corrupt("cluster at %d has timestamp %d", pos, ts)
			}
			return dataStart, dataEnd, int64(ts), nil
		case matroska.IDCRC32, matroska.IDVoid, idClusterPosition, idPrevSize, idSilentTracks:
			p += chl + int(csize)
		default:
			return 0, 0, 0, corrupt("cluster at %d has no timestamp before element %#x", pos, cid)
		}
	}
}

// readCue reads the block a cue with a relative position points at. The
// block's offset depends on the cluster header's length, which isn't known
// until the cluster head is read, so the block read starts where the block
// would be with the shortest header and reads the 7 bytes a longer one
// could add; both reads go out at once.
func (m *matroskaRead) readCue(i int) ([]Packet, error) {
	c := m.t.Cues[i]
	if c.ClusterPosition >= m.idx.segmentEnd || c.RelativePosition >= m.idx.segmentEnd-c.ClusterPosition {
		return nil, corrupt("cue points past the end of the segment")
	}
	h, owner := m.head(c.ClusterPosition)
	buf, bufStart, err := m.fetchCue(c, h, owner)
	if err != nil {
		return nil, err
	}
	if h.err != nil {
		return nil, h.err
	}
	at := h.dataStart + c.RelativePosition
	if at >= h.dataEnd {
		return nil, corrupt("cue points past the end of its cluster at %d", c.ClusterPosition)
	}
	if at < bufStart || at >= bufStart+int64(len(buf)) {
		return nil, corrupt("block at %d is cut off", at)
	}
	element, held, err := m.readElement(at, buf[at-bufStart:])
	if err != nil {
		return nil, err
	}
	defer m.budget.give(held)
	return m.blockPackets(element, at, h, &c)
}

// fetchCue reads the first bytes of the cue's block, and the cluster's head
// when owner, returning the bytes and the file offset they start at.
func (m *matroskaRead) fetchCue(c Cue, h *clusterHead, owner bool) ([]byte, int64, error) {
	if owner && c.RelativePosition <= nearClusterStart {
		// One read covers the cluster's head and the block.
		buf, err := readUpTo(m.r, c.ClusterPosition, matroska.MaxHeaderLen+int(c.RelativePosition)+blockWindow)
		if err != nil {
			h.err = err
			close(h.ready)
			return nil, 0, err
		}
		m.loadHead(h, c.ClusterPosition, buf)
		return buf, c.ClusterPosition, nil
	}
	if owner {
		go m.loadHead(h, c.ClusterPosition, nil)
	}
	start := c.ClusterPosition + minClusterHeader + c.RelativePosition
	buf, err := readUpTo(m.r, start, blockWindow+matroska.MaxHeaderLen-minClusterHeader)
	<-h.ready
	return buf, start, err
}

// readElement returns the whole block element at at, given its first
// bytes, reading the rest when they don't hold it. held is what it claimed
// from the budget for the rest, to give back once the block is used.
func (m *matroskaRead) readElement(at int64, first []byte) (_ blockElement, held int, _ error) {
	id, size, hl, ok := matroska.ParseElementHeader(first)
	if !ok {
		if len(first) < matroska.MaxHeaderLen {
			return blockElement{}, 0, corrupt("block at %d is cut off", at)
		}
		return blockElement{}, 0, corrupt("no element at block position %d", at)
	}
	if id != matroska.IDSimpleBlock && id != matroska.IDBlockGroup {
		return blockElement{}, 0, corrupt("cue points at element %#x at %d, not a block", id, at)
	}
	if size < 0 || size > maxBlockSize {
		return blockElement{}, 0, corrupt("block at %d has unsupported size %d", at, size)
	}
	total := hl + int(size)
	if total > len(first) {
		if err := m.budget.take(total); err != nil {
			return blockElement{}, 0, err
		}
		rest, err := readUpTo(m.r, at+int64(len(first)), total-len(first))
		if err != nil {
			m.budget.give(total)
			return blockElement{}, 0, err
		}
		if len(first)+len(rest) < total {
			m.budget.give(total)
			return blockElement{}, 0, corrupt("block at %d is cut off", at)
		}
		first, held = append(first[:len(first):len(first)], rest...), total
	}
	return blockElement{id: id, data: first[hl:total]}, held, nil
}

type blockElement struct {
	id   uint64
	data []byte
}

// blockPackets turns a block into packets the way FFmpeg's
// matroska_parse_block does: laced frames split the block's duration, and
// each starts where the one before ends. cue, when not nil, is the block's
// cue, whose duration stands in for a block that has none.
func (m *matroskaRead) blockPackets(e blockElement, at int64, h *clusterHead, cue *Cue) ([]Packet, error) {
	blk, err := parseBlockElement(e.id, e.data)
	if err != nil {
		return nil, err
	}
	if blk.track != m.t.Number {
		return nil, corrupt("cue for track %d points at a block of track %d at %d", m.t.Number, blk.track, at)
	}
	scale := m.idx.TimestampScale
	ticks := h.timestamp + int64(blk.rel)
	laces := int64(len(blk.frames))
	// FFmpeg uses the track's default duration for a block whose duration
	// is absent or zero.
	duration := blk.duration
	if duration == 0 && m.t.DefaultDuration > 0 {
		if m.t.DefaultDuration > time.Duration(math.MaxInt64/laces) {
			return nil, corrupt("track default duration %d out of range", m.t.DefaultDuration)
		}
		duration = int64(m.t.DefaultDuration) * laces / scale
	}
	// Each frame's share, its timestamp, and the duration in nanoseconds
	// must all fit.
	whole, ok := ticksDuration(duration, scale)
	if !ok || duration > math.MaxInt64/2/laces || ticks > math.MaxInt64-duration {
		return nil, corrupt("block at %d has duration %d", at, duration)
	}
	if err := m.budget.take(len(blk.frames) * packetMetadataBytes); err != nil {
		return nil, err
	}
	packets := make([]Packet, 0, laces)
	for n, frame := range blk.frames {
		data, err := m.payload(frame)
		if err != nil {
			return nil, fmt.Errorf("block at %d: %w", at, err)
		}
		pts, ok := ticksDuration(ticks, scale)
		if !ok {
			return nil, corrupt("block at %d has timestamp %d", at, ticks)
		}
		p := Packet{PTS: pts, Data: data, pos: at, lace: n, laces: int(laces), nonKeyframe: !blk.keyframe || n > 0}
		lace := duration*int64(n+1)/laces - duration*int64(n)/laces
		switch {
		case laces > 1:
			// Each frame gets its own share, which WriteMKS writes as the
			// frame's own BlockDuration.
			p.Duration = time.Duration(lace * scale)
		case blk.hasDuration && blk.duration > 0:
			p.Duration = whole
		case cue != nil && cue.Duration > 0:
			p.Duration, p.inferredDuration = cue.Duration, true
		default:
			p.Duration, p.inferredDuration = whole, true
		}
		packets = append(packets, p)
		ticks += lace
	}
	return packets, nil
}

// payload returns a frame's data as the codec sees it, in its own memory.
func (m *matroskaRead) payload(frame []byte) ([]byte, error) {
	if m.t.Compression == nil {
		if err := m.budget.take(len(frame)); err != nil {
			return nil, err
		}
		return bytes.Clone(frame), nil
	}
	return decompressBudget(m.t.Compression, frame, m.budget)
}

// scanCluster walks the cluster of cues that have no relative position for
// the track's blocks at their times, reading element headers and skipping
// other tracks' blocks.
func (m *matroskaRead) scanCluster(cues []int) ([]Packet, []error, error) {
	pos := m.t.Cues[cues[0]].ClusterPosition
	h, owner := m.head(pos)
	if owner {
		m.loadHead(h, pos, nil)
	} else {
		<-h.ready
	}
	m.mu.Lock()
	m.scanned++
	m.mu.Unlock()
	if h.err == nil && pos >= m.idx.segmentEnd {
		h.err = corrupt("cue points past the end of the segment")
	}
	if h.err != nil {
		if errors.Is(h.err, errCorrupt) {
			return nil, []error{h.err}, nil
		}
		return nil, nil, h.err
	}
	s := clusterScan{m: m, h: h, w: scanReader{r: m.r}, want: map[time.Duration][]int{}, remaining: len(cues)}
	for _, i := range cues {
		c := m.t.Cues[i]
		s.want[c.Time] = append(s.want[c.Time], i)
	}
	at := h.dataStart
	for n := 0; at < h.dataEnd && n < maxClusterChildren && s.remaining > 0; n++ {
		next, more, err := s.step(at)
		if err != nil {
			return nil, nil, err
		}
		if !more {
			break
		}
		at = next
	}
	for _, list := range s.want {
		for _, i := range list {
			s.damage = append(s.damage, corrupt("no block for the cue at %v in the cluster at %d", m.t.Cues[i].Time, pos))
		}
	}
	return s.packets, s.damage, nil
}

// clusterScan is the state of one scanCluster walk.
type clusterScan struct {
	m *matroskaRead
	h *clusterHead
	w scanReader
	// want lists the unmatched cues by time.
	want      map[time.Duration][]int
	remaining int
	packets   []Packet
	damage    []error
}

// step handles the cluster element at at, returning where the next one
// starts; more is false when the cluster ends there.
func (s *clusterScan) step(at int64) (next int64, more bool, err error) {
	b, err := s.w.get(at, matroska.MaxHeaderLen+16)
	if err != nil {
		return 0, false, err
	}
	id, size, hl, ok := matroska.ParseElementHeader(b)
	if !ok || size == matroska.UnknownSize || id == matroska.IDCluster || id > 0xFFFFFF {
		// The cluster ends at the next top-level element.
		return 0, false, nil
	}
	if id != matroska.IDSimpleBlock && id != matroska.IDBlockGroup {
		return at + int64(hl) + size, true, nil
	}
	if track, known := peekBlockTrack(id, b[hl:]); known && track != s.m.t.Number {
		return at + int64(hl) + size, true, nil
	}
	if size > maxBlockSize {
		return 0, false, nil
	}
	total := hl + int(size)
	if err := s.m.budget.take(total); err != nil {
		return 0, false, err
	}
	defer s.m.budget.give(total)
	data, err := s.w.get(at, total)
	if err != nil {
		return 0, false, err
	}
	if len(data) < total {
		s.damage = append(s.damage, corrupt("block at %d is cut off", at))
		return 0, false, nil
	}
	if err := s.match(blockElement{id: id, data: data[hl:]}, at); err != nil {
		return 0, false, err
	}
	return at + int64(total), true, nil
}

// match adds a block the scan found when a cue wants its time. A damaged
// block is recorded as damage; any other error, such as ErrTooLarge, ends
// the read, as it does for a block a cue points at directly.
func (s *clusterScan) match(e blockElement, at int64) error {
	// A block that doesn't parse can't be matched to a cue; a cue left
	// without a block is reported as damage after the walk.
	blk, parseErr := parseBlockElement(e.id, e.data)
	if ours := parseErr == nil && blk.track == s.m.t.Number; !ours {
		return nil
	}
	t, ok := ticksDuration(s.h.timestamp+int64(blk.rel), s.m.idx.TimestampScale)
	list := s.want[t]
	if !ok || len(list) == 0 {
		return nil
	}
	cue := s.m.t.Cues[list[0]]
	if len(list) == 1 {
		delete(s.want, t)
	} else {
		s.want[t] = list[1:]
	}
	s.remaining--
	ps, err := s.m.blockPackets(e, at, s.h, &cue)
	if err != nil {
		if !errors.Is(err, errCorrupt) {
			return err
		}
		s.damage = append(s.damage, err)
		return nil
	}
	s.packets = append(s.packets, ps...)
	return nil
}

// peekBlockTrack reads a block's track number from its first bytes; known
// is false when they don't hold it.
func peekBlockTrack(id uint64, b []byte) (track uint64, known bool) {
	if id == matroska.IDBlockGroup {
		cid, _, chl, ok := matroska.ParseElementHeader(b)
		if !ok || cid != matroska.IDBlock {
			return 0, false
		}
		b = b[chl:]
	}
	track, _, ok := matroska.ReadVint(b, false)
	return track, ok
}

// scanReader serves a cluster walk from one window, reading the next when
// an element starts past it.
type scanReader struct {
	r     *fileReader
	start int64
	buf   []byte
}

// get returns the bytes at [off, off+n), or fewer at the end of the file.
func (s *scanReader) get(off int64, n int) ([]byte, error) {
	if s.buf != nil && off >= s.start && off+int64(n) <= s.start+int64(len(s.buf)) {
		return s.buf[off-s.start : off-s.start+int64(n)], nil
	}
	buf, err := readUpTo(s.r, off, max(n, scanWindow))
	if err != nil {
		return nil, err
	}
	s.start, s.buf = off, buf
	return buf[:min(n, len(buf))], nil
}
