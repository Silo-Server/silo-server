package mediasample

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// A Sheets run writes one raw frame per decoded frame to stdout and logs
// each frame's metadata=print lines to stderr: a frame line with the frame
// counter and time, then a "sample" entry, which is the sample time a
// sampled list tagged the frame's packet with, or -1 for an untagged frame
// (see buildSheetsGraph). The counter starts at zero and counts every frame
// the filter passes on, so the Nth raw frame is the frame logged as N.
//
// ffmpeg logs a frame before it writes the frame, but stdout and stderr are
// read by different goroutines, so either may run ahead. sheetAssembler
// queues whichever half arrives first and places a frame once both halves
// are in. Neither reader ever waits for the other: a stdout write that
// waited for a log line would stall ffmpeg's exit, and a stand-in Exec that
// writes all of stdout before its log would deadlock.
//
// A filter graph reinit, as a hardware decoder that renews its surfaces
// causes mid-run, restarts the counter at zero. The old graph has passed on
// every frame it logged, so the frames still line up and the count goes on.
//
// Where a frame goes:
//
//   - Sampled lists: the cell of the sample its tag names. A sample that
//     decodes more than one frame (a keyframe within its short span, or an
//     all-intra source) keeps the first, which is the frame at or before the
//     sample time. Untagged frames are dropped. Each frame's time also
//     places it back in the input (sourceSeconds), which a repair reads. A
//     sample a keyframe list left out shows the cell before (shareFrames).
//   - Windows (containers the list cannot seek in, and ReadThrough): each
//     sample takes the last frame at or before its time, as
//     pickSampleFrames does for statistics.
//   - Repairs (see sheet_repair.go): a list over some samples whose frames
//     are collected as candidates, each sample keeping the frame nearest its
//     time, and replace what the first run placed.
//
// A cell without a frame of its own shows the previous cell's frame, and
// cells before the first frame show the first; the grid never shifts. A run
// in which more than maxFilledShare of the cells (and more than one) had to
// be filled fails as empty, since that is a truncated or broken file rather
// than a sheet worth keeping, and a hardware attempt then falls back to
// software.
//
// Sheets are encoded once the run and its repairs are done, since a repair
// may replace any cell; a run's sheets are bounded by its caller's chunking.

// maxQueuedFrameBytes bounds the raw frames held while their log lines are
// behind. The log normally leads, so this only trips when the log parser
// has stopped keeping up at all.
const maxQueuedFrameBytes = 64 << 20

// keyframeMatchSeconds absorbs the rounding between a keyframe packet's
// time and its decoded frame's, which keyframes seconds apart never reach.
const keyframeMatchSeconds = 0.01

// maxDroppedKeyframeShare replaces maxFilledShare for a window whose
// decoder dropped keyframes over skipped pictures (see droppedKeyframe):
// such a file is whole, and a sample showing the keyframe a few seconds
// before its own is a usable preview. A list keeps maxFilledShare, so too
// many drops still send it to the window.
const maxDroppedKeyframeShare = 0.15

// sheetFramePattern matches the frame line of the sheets metadata filter:
// "frame:12   pts:1550   pts_time:1.55". pts_time is "NOPTS" for a frame
// without a time.
var sheetFramePattern = regexp.MustCompile(`^frame:\s*(\d+)\s+pts:\s*\S+\s+pts_time:\s*(\S+)`)

// sheetFrameMeta is one frame's metadata=print lines.
type sheetFrameMeta struct {
	index   int64
	seconds float64
	hasTime bool
	sample  float64
}

// sheetAssembler joins a Sheets run's raw frames with its log and tiles
// them into sheets. Its zero value is not usable; see newSheetAssembler.
type sheetAssembler struct {
	mu sync.Mutex

	out       SheetsOutput
	times     []float64
	frameSize int
	perSheet  int
	// sampled places frames by their sample tag, which cells maps to a
	// cell; otherwise frames are placed by time, offset by the window start.
	sampled bool
	cells   map[int64]int
	offset  float64
	// A sampled list's entries each last span seconds, and plan says where
	// each starts and which entry each sample's frame comes from. header
	// reads the list input's start time, by which ffmpeg shifted every
	// frame's time; with these, sourceSeconds finds a frame's time in the
	// input. sources holds that time for each cell's frame, NaN when it is
	// unknown.
	span    float64
	plan    listPlan
	header  inputHeaderParser
	sources []float64
	// onDisorder, when set, is called once a frame comes from an earlier
	// list entry than the frame before it, lastEntry's (see keyframeSheets).
	onDisorder func()
	lastEntry  int
	// candidates, when set, collects each sample's nearest frame for a
	// repair instead of tiling.
	candidates []sheetCandidate

	logContext string
	current    *sheetFrameMeta
	// counter is the frame number the filter logs next; logged counts
	// every frame logged, across graph reinits.
	counter int64
	logged  int64
	metas   []sheetFrameMeta

	partial     []byte
	queued      [][]byte
	queuedBytes int
	spare       [][]byte

	// Window placement: the next sample to assign and the last frame seen.
	nextSample int
	prev       []byte
	// The stream-copy timing file reports how far the input was actually
	// read, including non-keyframes after the last decoded keyframe.
	packetTimeBase float64
	packetEnd      float64
	hasPacketTime  bool
	// dtsTiming lets a packet without a PTS count by its DTS; see
	// packetLine.
	dtsTiming bool
	// A window whose decoder dropped keyframes (see droppedKeyframe) gives
	// the samples after each dropped keyframe an earlier one. The times of
	// the frames decoded and of the keyframe packets read find those
	// samples, which then count as filled. A keyframe packet without a time
	// leaves them unknown (untimedKey).
	droppedKeyframes bool
	untimedKey       bool
	frameTimes       []float64
	keyTimes         []float64

	open      map[int]*sheetCanvas
	finalized int
	carry     []byte
	sheets    []Sheet
	decoded   int
	filled    int
	// refined counts the cells a repair replaced, each once.
	refined      int
	refinedCells map[int]bool

	err error
}

// newSheetAssembler returns an assembler for the frames of times. A sampled
// run places frames by their sample tag, its list entries lasting span
// seconds and starting at each sample's time unless the caller sets another
// plan; a window run places them by their time plus offset.
func newSheetAssembler(out SheetsOutput, times []float64, sampled bool, offset, span float64) *sheetAssembler {
	a := &sheetAssembler{
		out:        out,
		times:      times,
		frameSize:  out.frameBytes(),
		perSheet:   out.Columns * out.Rows,
		sampled:    sampled,
		offset:     offset,
		span:       span,
		logContext: "[" + filterMetadata + "@" + sheetMetadataID + " @ ",
		open:       map[int]*sheetCanvas{},
	}
	if sampled {
		a.cells = make(map[int64]int, len(times))
		a.refinedCells = map[int]bool{}
		a.sources = make([]float64, len(times))
		a.plan = samplePlan(times, 0)
		for i, at := range times {
			a.cells[sampleKey(at)] = i
			a.sources[i] = math.NaN()
		}
	}
	return a
}

// sampleKey is a sample time in whole microseconds, the precision sample
// tags are written with, so a parsed tag finds its sample exactly.
func sampleKey(seconds float64) int64 {
	return int64(math.Round(seconds * 1e6))
}

// Write takes raw frames from ffmpeg's stdout. It never fails or blocks on
// the log, so ffmpeg is never held up; a join error is kept for finish.
func (a *sheetAssembler) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(p)
	if a.err != nil {
		return n, nil
	}
	for len(p) > 0 {
		if a.partial == nil {
			a.partial = a.frameBuffer()
		}
		take := min(a.frameSize-len(a.partial), len(p))
		a.partial = append(a.partial, p[:take]...)
		p = p[take:]
		if len(a.partial) == a.frameSize {
			a.queued = append(a.queued, a.partial)
			a.queuedBytes += a.frameSize
			a.partial = nil
		}
	}
	a.drain()
	if a.err == nil && a.queuedBytes > maxQueuedFrameBytes {
		a.err = errors.New("ffmpeg's log fell too far behind its frames")
	}
	return n, nil
}

// line takes one log line, keeping the input header and the lines of the
// sheets metadata filter.
func (a *sheetAssembler) line(line string) {
	if droppedKeyframe(line) {
		a.mu.Lock()
		a.droppedKeyframes = true
		a.mu.Unlock()
		return
	}
	message, ok := filterMessage(line, a.logContext)
	if !ok {
		if a.sampled {
			a.mu.Lock()
			a.header.line(line)
			a.mu.Unlock()
		}
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return
	}
	if match := sheetFramePattern.FindStringSubmatch(message); match != nil {
		index, err := strconv.ParseInt(match[1], 10, 64)
		// A counter that skips, or restarts anywhere but at zero, no longer
		// counts the frames on stdout.
		if err != nil || (index != a.counter && index != 0) {
			a.err = fmt.Errorf("ffmpeg logged frame %s after %d frames", match[1], a.logged)
			return
		}
		a.counter = index + 1
		meta := sheetFrameMeta{index: a.logged}
		if seconds, err := strconv.ParseFloat(match[2], 64); err == nil && finite(seconds) {
			meta.seconds, meta.hasTime = seconds, true
		}
		a.current = &meta
		return
	}
	if a.current == nil {
		return
	}
	key, raw, ok := strings.Cut(strings.TrimSpace(message), "=")
	if !ok || key != sampleMetadataKey {
		return
	}
	sample, err := strconv.ParseFloat(raw, 64)
	if err != nil || !finite(sample) {
		sample = -1
	}
	a.current.sample = sample
	a.metas = append(a.metas, *a.current)
	a.current = nil
	a.logged++
	a.drain()
}

// readPacketTiming reads the stream-copy timing file after ffmpeg exits, so
// packet writes cannot interleave with the frame log on stderr.
func (a *sheetAssembler) readPacketTiming(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxStderrLine)
	for scanner.Scan() {
		a.packetLine(scanner.Text())
		if a.err != nil {
			return a.err
		}
	}
	return scanner.Err()
}

// packetLine reads the framecrc stream-copy output: "#tb 0: 1/1000",
// followed by "0, DTS, PTS, duration, size, checksum[, flags]". PTS and
// duration count in that time base and are relative to the window start.
// With dtsTiming, a packet without a PTS falls back to its DTS:
// packed-bitstream XviD in AVI carries no PTS on any packet, and a DTS never
// runs ahead of the PTS it stands in for, so the fallback cannot stretch a
// reordered stream's extent. Without a PTS the decoder stamps each frame
// with the DTS of the packet that released it, so the fallback is set only
// for a decoder in low-delay mode, which releases each keyframe as it
// arrives. A reordering decoder, such as H.264 with B-frames, releases a
// keyframes-only read's keyframes a GOP or more late; its DTS-only windows
// keep failing rather than publish every cell at an earlier keyframe.
func (a *sheetAssembler) packetLine(line string) {
	if raw, ok := strings.CutPrefix(line, "#tb 0: "); ok {
		numerator, denominator, ok := strings.Cut(raw, "/")
		n, errN := strconv.ParseFloat(strings.TrimSpace(numerator), 64)
		d, errD := strconv.ParseFloat(strings.TrimSpace(denominator), 64)
		if !ok || errN != nil || errD != nil || !(n > 0) || !(d > 0) || !finite(n) || !finite(d) {
			a.err = errors.New("ffmpeg reported an invalid packet time base")
		} else {
			a.packetTimeBase = n / d
		}
		return
	}
	if !strings.HasPrefix(line, "0,") {
		return
	}
	fields := strings.SplitN(line, ",", 6)
	if len(fields) != 6 || a.packetTimeBase <= 0 {
		a.err = errors.New("ffmpeg reported packet timing without a time base")
		return
	}
	pts, hasPTS := packetTimestamp(fields[2])
	if !hasPTS && a.dtsTiming {
		pts, hasPTS = packetTimestamp(fields[1])
	}
	duration, errDuration := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
	if hasPTS && errDuration == nil && duration >= 0 {
		end := a.offset + (float64(pts)+float64(duration))*a.packetTimeBase
		if finite(end) {
			a.packetEnd = max(a.packetEnd, end)
			a.hasPacketTime = true
		}
	}
	if packetIsKey(fields[5]) {
		if at := a.offset + float64(pts)*a.packetTimeBase; hasPTS && finite(at) {
			a.keyTimes = append(a.keyTimes, at)
		} else {
			a.untimedKey = true
		}
	}
}

// packetIsKey reads the flags framecrc appends after the checksum. It
// prints them only when they differ from a plain keyframe's.
func packetIsKey(rest string) bool {
	_, raw, ok := strings.Cut(rest, "F=0x")
	if !ok {
		return true
	}
	// Side data, when the packet has any, follows the flags.
	raw, _, _ = strings.Cut(raw, ",")
	flags, err := strconv.ParseUint(strings.TrimSpace(raw), 16, 64)
	return err == nil && flags&1 != 0
}

// staleSamples counts the samples that hold a frame but not their own
// keyframe, the last keyframe packet at or before the sample. A sample
// without any frame is counted when its sheet is finalized.
func (a *sheetAssembler) staleSamples() int {
	keys := slices.Clone(a.keyTimes)
	slices.Sort(keys)
	frames := slices.Clone(a.frameTimes)
	slices.Sort(frames)
	stale, next := 0, 0
	for cell, at := range a.times {
		for next < len(keys) && keys[next] <= at+sampleMatchSeconds {
			next++
		}
		if next == 0 || !a.placed(cell) {
			continue
		}
		key := keys[next-1]
		i, _ := slices.BinarySearch(frames, key-keyframeMatchSeconds)
		if i == len(frames) || frames[i] > key+keyframeMatchSeconds {
			stale++
		}
	}
	return stale
}

// packetTimestamp parses a framecrc DTS or PTS field. framecrc prints
// AV_NOPTS_VALUE as the minimum int64, rather than NOPTS.
func packetTimestamp(field string) (int64, bool) {
	ts, err := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
	return ts, err == nil && ts != math.MinInt64
}

// drain places every frame whose log lines are in.
func (a *sheetAssembler) drain() {
	for a.err == nil && len(a.queued) > 0 && len(a.metas) > 0 {
		frame, meta := a.queued[0], a.metas[0]
		a.queued, a.metas = a.queued[1:], a.metas[1:]
		a.queuedBytes -= a.frameSize
		a.place(meta, frame)
	}
}

// place puts a frame in its cell or cells.
func (a *sheetAssembler) place(meta sheetFrameMeta, frame []byte) {
	if a.sampled {
		cell, ok := a.cells[sampleKey(meta.sample)]
		if meta.sample < 0 || !ok {
			a.recycle(frame)
			return
		}
		if a.onDisorder != nil {
			if entry := a.plan.entry[cell]; entry < a.lastEntry {
				a.onDisorder()
				a.onDisorder = nil
			} else {
				a.lastEntry = entry
			}
		}
		source := a.sourceSeconds(meta, cell)
		if a.candidates != nil {
			a.offer(cell, source, frame)
			return
		}
		if a.setCell(cell, frame) {
			a.decoded++
			a.sources[cell] = source
		}
		a.recycle(frame)
		return
	}
	if !meta.hasTime {
		a.recycle(frame)
		return
	}
	at := a.offset + meta.seconds
	a.frameTimes = append(a.frameTimes, at)
	a.assignWindow(at - sampleMatchSeconds)
	a.recycle(a.prev)
	a.prev = frame
}

// assignWindow gives every sample before limit the last frame seen, if any.
func (a *sheetAssembler) assignWindow(limit float64) {
	for a.nextSample < len(a.times) && a.times[a.nextSample] < limit {
		if a.prev != nil && a.setCell(a.nextSample, a.prev) {
			a.decoded++
		}
		a.nextSample++
	}
}

// sourceSeconds is the input time, counted from the start of the file as
// sample times are, of a sampled list's frame for cell, NaN when the frame
// has no time. The entry cell's frame comes from starts span seconds into
// the list for each entry before it, and at its inpoint in the container
// (see buildConcatListSpan), and ffmpeg shifted every frame by the list
// input's start time.
func (a *sheetAssembler) sourceSeconds(meta sheetFrameMeta, cell int) float64 {
	entry := a.plan.entry[cell]
	if !meta.hasTime || entry < 0 {
		return math.NaN()
	}
	return meta.seconds + a.header.info.StartSeconds - float64(entry)*a.span + a.plan.entries[entry].inpoint - a.plan.inputStart
}

// shareFrames gives each sample the list left out (see keyframePlan) the
// frame and source time of the cell before it, which decoding the keyframe
// they share again would have given it. A sample after a cell without a
// frame stays without one, for the repair to find.
func (a *sheetAssembler) shareFrames() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for cell, entry := range a.plan.entry {
		if entry >= 0 || cell == 0 || !a.placed(cell-1) {
			continue
		}
		frame := a.canvas((cell - 1) / a.perSheet).read((cell - 1) % a.perSheet)
		if a.setCell(cell, frame) {
			a.decoded++
			a.sources[cell] = a.sources[cell-1]
		}
	}
}

// setCell copies frame into cell unless the cell already holds one, and
// reports whether it did.
func (a *sheetAssembler) setCell(cell int, frame []byte) bool {
	canvas := a.canvas(cell / a.perSheet)
	slot := cell % a.perSheet
	if canvas.set[slot] {
		return false
	}
	canvas.write(slot, frame)
	canvas.set[slot] = true
	return true
}

func (a *sheetAssembler) canvas(sheet int) *sheetCanvas {
	canvas := a.open[sheet]
	if canvas == nil {
		canvas = newSheetCanvas(a.out, a.perSheet)
		a.open[sheet] = canvas
	}
	return canvas
}

// finalizeThrough encodes every sheet before limit.
func (a *sheetAssembler) finalizeThrough(limit int) {
	for a.err == nil && a.finalized < limit {
		a.finalize(a.finalized)
		a.finalized++
	}
}

// finalize fills sheet's empty cells and encodes it. An empty cell shows the
// cell before it, and cells before the first picture show the first: the
// previous sheet's last cell, or else this sheet's first filled cell.
func (a *sheetAssembler) finalize(sheet int) {
	canvas := a.canvas(sheet)
	delete(a.open, sheet)
	count := min(a.perSheet, len(a.times)-sheet*a.perSheet)
	lead := a.carry
	if lead == nil {
		if first := slices.Index(canvas.set[:count], true); first >= 0 {
			lead = canvas.read(first)
		}
	}
	previous := -1
	for slot := range count {
		if !canvas.set[slot] {
			a.filled++
			switch {
			case previous >= 0:
				canvas.write(slot, canvas.read(previous))
			case lead != nil:
				canvas.write(slot, lead)
			}
			canvas.set[slot] = true
		}
		previous = slot
	}
	if count > 0 {
		a.carry = canvas.read(count - 1)
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas.img, &jpeg.Options{Quality: a.out.Quality}); err != nil {
		a.err = fmt.Errorf("encode sheet %d: %w", sheet, err)
		return
	}
	a.sheets = append(a.sheets, Sheet{Index: sheet, Thumbnails: count, JPEG: encoded.Bytes()})
}

// close checks the run's frames against its log once ffmpeg has exited and
// assigns what the window placement still holds.
func (a *sheetAssembler) close() *AttemptError {
	a.mu.Lock()
	defer a.mu.Unlock()
	fail := func(reason Reason, err error) *AttemptError {
		return &AttemptError{Reason: reason, Err: err}
	}
	switch {
	case a.err != nil:
		return fail(ReasonOutput, a.err)
	case len(a.partial) > 0:
		return fail(ReasonOutput, fmt.Errorf("ffmpeg wrote %d bytes after its last whole frame", len(a.partial)))
	case len(a.queued) > 0 || len(a.metas) > 0 || a.current != nil:
		return fail(ReasonOutput, fmt.Errorf("ffmpeg wrote %d frames but logged %d", a.logged-int64(len(a.metas))+int64(len(a.queued)), a.logged))
	}
	if !a.sampled {
		if !a.hasPacketTime {
			return fail(ReasonOutput, errors.New("ffmpeg reported no usable packet timing for the sampled window"))
		}
		a.assignWindow(a.packetEnd + sampleMatchSeconds)
		if a.droppedKeyframes {
			a.filled += a.staleSamples()
		}
	}
	return nil
}

// finish encodes the sheets of a closed run and its repairs.
func (a *sheetAssembler) finish() ([]Sheet, SheetFrames, *AttemptError) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fail := func(reason Reason, err error) ([]Sheet, SheetFrames, *AttemptError) {
		return nil, SheetFrames{}, &AttemptError{Reason: reason, Err: err}
	}
	if a.decoded == 0 {
		return fail(ReasonEmpty, errors.New("ffmpeg decoded no frame for any sample"))
	}
	if a.droppedKeyframes && a.untimedKey && !a.sampled {
		return fail(ReasonEmpty, errors.New("ffmpeg dropped keyframes in a window with an untimed keyframe packet"))
	}
	sheets := (len(a.times) + a.perSheet - 1) / a.perSheet
	a.finalizeThrough(sheets)
	if a.err != nil {
		return fail(ReasonOutput, a.err)
	}
	share := maxFilledShare
	if a.droppedKeyframes && !a.sampled {
		share = maxDroppedKeyframeShare
	}
	if limit := max(1, int(share*float64(len(a.times)))); a.filled > limit {
		return fail(ReasonEmpty, fmt.Errorf("ffmpeg decoded frames for only %d of %d samples", len(a.times)-a.filled, len(a.times)))
	}
	return a.sheets, SheetFrames{Decoded: len(a.times) - a.filled, Filled: a.filled, Refined: a.refined}, nil
}

func (a *sheetAssembler) frameBuffer() []byte {
	if n := len(a.spare); n > 0 {
		buf := a.spare[n-1]
		a.spare = a.spare[:n-1]
		return buf[:0]
	}
	return make([]byte, 0, a.frameSize)
}

func (a *sheetAssembler) recycle(frame []byte) {
	if frame != nil {
		a.spare = append(a.spare, frame)
	}
}

// sheetCanvas is one sheet being filled: a 4:2:0 picture of Columns by Rows
// cells, black where no cell was written.
type sheetCanvas struct {
	img  *image.YCbCr
	w, h int
	cols int
	set  []bool
}

func newSheetCanvas(out SheetsOutput, cells int) *sheetCanvas {
	img := image.NewYCbCr(image.Rect(0, 0, out.Columns*out.TileWidth, out.Rows*out.TileHeight), image.YCbCrSubsampleRatio420)
	// Full-range black: luma 0, neutral chroma. Zero chroma would be green.
	for i := range img.Cb {
		img.Cb[i] = 128
		img.Cr[i] = 128
	}
	return &sheetCanvas{img: img, w: out.TileWidth, h: out.TileHeight, cols: out.Columns, set: make([]bool, cells)}
}

// planes calls copyRows for the luma and both chroma planes of slot: the
// plane, its stride, the cell's first byte in it, and the width, height, and
// offset of the plane within a raw frame.
func (c *sheetCanvas) planes(slot int, copyRows func(plane []byte, stride, origin, w, h, frameOffset int)) {
	x, y := (slot%c.cols)*c.w, (slot/c.cols)*c.h
	cw, ch := c.w/2, c.h/2
	copyRows(c.img.Y, c.img.YStride, y*c.img.YStride+x, c.w, c.h, 0)
	copyRows(c.img.Cb, c.img.CStride, (y/2)*c.img.CStride+x/2, cw, ch, c.w*c.h)
	copyRows(c.img.Cr, c.img.CStride, (y/2)*c.img.CStride+x/2, cw, ch, c.w*c.h+cw*ch)
}

// write copies a raw frame into slot.
func (c *sheetCanvas) write(slot int, frame []byte) {
	c.planes(slot, func(plane []byte, stride, origin, w, h, frameOffset int) {
		for row := range h {
			copy(plane[origin+row*stride:origin+row*stride+w], frame[frameOffset+row*w:frameOffset+(row+1)*w])
		}
	})
}

// read returns slot as a raw frame.
func (c *sheetCanvas) read(slot int) []byte {
	frame := make([]byte, c.w*c.h*3/2)
	c.planes(slot, func(plane []byte, stride, origin, w, h, frameOffset int) {
		for row := range h {
			copy(frame[frameOffset+row*w:frameOffset+(row+1)*w], plane[origin+row*stride:origin+row*stride+w])
		}
	})
	return frame
}
