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
// Where a frame goes:
//
//   - Sampled lists: the cell of the sample its tag names. A sample that
//     decodes more than one frame (a keyframe within its short span, or an
//     all-intra source) keeps the first, which is the frame at or before the
//     sample time. Untagged frames are dropped.
//   - Windows (containers the list cannot seek in, and ReadThrough): each
//     sample takes the last frame at or before its time, as
//     pickSampleFrames does for statistics.
//
// A cell without a frame of its own shows the previous cell's frame, and
// cells before the first frame show the first; the grid never shifts. A run
// in which more than maxFilledShare of the cells (and more than one) had to
// be filled fails as empty, since that is a truncated or broken file rather
// than a sheet worth keeping, and a hardware attempt then falls back to
// software.
//
// Sheets are encoded as soon as no later frame can land in them: frames
// arrive in sample order, so once a frame lands two sheets further on, the
// earlier sheet is done. At most three canvases are held at a time.

// maxQueuedFrameBytes bounds the raw frames held while their log lines are
// behind. The log normally leads, so this only trips when the log parser
// has stopped keeping up at all.
const maxQueuedFrameBytes = 64 << 20

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

	logContext string
	current    *sheetFrameMeta
	logged     int64
	metas      []sheetFrameMeta

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

	open      map[int]*sheetCanvas
	furthest  int
	finalized int
	carry     []byte
	sheets    []Sheet
	decoded   int
	filled    int

	err error
}

// newSheetAssembler returns an assembler for the frames of times. A sampled
// run places frames by their sample tag; a window run by their time plus
// offset.
func newSheetAssembler(out SheetsOutput, times []float64, sampled bool, offset float64) *sheetAssembler {
	a := &sheetAssembler{
		out:        out,
		times:      times,
		frameSize:  out.frameBytes(),
		perSheet:   out.Columns * out.Rows,
		sampled:    sampled,
		offset:     offset,
		logContext: "[" + filterMetadata + "@" + sheetMetadataID + " @ ",
		open:       map[int]*sheetCanvas{},
		furthest:   -1,
	}
	if sampled {
		a.cells = make(map[int64]int, len(times))
		for i, at := range times {
			a.cells[sampleKey(at)] = i
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

// line takes one log line, keeping those of the sheets metadata filter.
func (a *sheetAssembler) line(line string) {
	message, ok := filterMessage(line, a.logContext)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return
	}
	if match := sheetFramePattern.FindStringSubmatch(message); match != nil {
		index, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || index != a.logged {
			// A counter that skips or restarts, as after a filter graph
			// reinit, no longer counts the frames on stdout.
			a.err = fmt.Errorf("ffmpeg logged frame %s after %d frames", match[1], a.logged)
			return
		}
		meta := sheetFrameMeta{index: index}
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
	pts, errPTS := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
	duration, errDuration := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
	// framecrc prints AV_NOPTS_VALUE as the minimum int64, rather than NOPTS.
	if errPTS == nil && pts != math.MinInt64 && errDuration == nil && duration >= 0 {
		end := a.offset + (float64(pts)+float64(duration))*a.packetTimeBase
		if finite(end) {
			a.packetEnd = max(a.packetEnd, end)
			a.hasPacketTime = true
		}
	}
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
		if meta.sample >= 0 && ok && a.setCell(cell, frame) {
			a.decoded++
		}
		a.recycle(frame)
		return
	}
	if !meta.hasTime {
		a.recycle(frame)
		return
	}
	at := a.offset + meta.seconds
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

// setCell copies frame into cell unless the cell already holds one, and
// reports whether it did. Frames for a sheet already encoded are dropped.
func (a *sheetAssembler) setCell(cell int, frame []byte) bool {
	sheet := cell / a.perSheet
	if sheet < a.finalized {
		return false
	}
	canvas := a.canvas(sheet)
	slot := cell % a.perSheet
	if canvas.set[slot] {
		return false
	}
	canvas.write(slot, frame)
	canvas.set[slot] = true
	if sheet > a.furthest {
		a.furthest = sheet
		a.finalizeThrough(sheet - 1)
	}
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

// finish checks the run's frames against its log, assigns what the window
// placement still holds, and encodes the remaining sheets.
func (a *sheetAssembler) finish() ([]Sheet, SheetFrames, *AttemptError) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fail := func(reason Reason, err error) ([]Sheet, SheetFrames, *AttemptError) {
		return nil, SheetFrames{}, &AttemptError{Reason: reason, Err: err}
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
	}
	if a.decoded == 0 {
		return fail(ReasonEmpty, errors.New("ffmpeg decoded no frame for any sample"))
	}
	sheets := (len(a.times) + a.perSheet - 1) / a.perSheet
	a.finalizeThrough(sheets)
	if a.err != nil {
		return fail(ReasonOutput, a.err)
	}
	if limit := max(1, int(maxFilledShare*float64(len(a.times)))); a.filled > limit {
		return fail(ReasonEmpty, fmt.Errorf("ffmpeg decoded frames for only %d of %d samples", len(a.times)-a.filled, len(a.times)))
	}
	return a.sheets, SheetFrames{Decoded: len(a.times) - a.filled, Filled: a.filled}, nil
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
