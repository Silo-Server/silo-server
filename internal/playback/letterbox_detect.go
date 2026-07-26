package playback

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Letterbox measures the hard-coded black bars baked into a video frame.
//
// Some sources — Disney+/Netflix WEB-DLs in particular — encode a 2.39:1 image
// inside a 1920x1080 frame, so the container reports 16:9 and every client sees
// a full-height picture. Anything anchored to the bottom of that frame, notably
// subtitles, lands inside the black bar instead of over the image. Nothing in
// the file's metadata reveals this: only the pixels do.
//
// Fractions are of the full frame height, so a client can inset its overlay
// without knowing the frame's dimensions.
type Letterbox struct {
	TopFraction    float64
	BottomFraction float64
}

// Detected reports whether either bar is large enough to act on.
func (l Letterbox) Detected() bool {
	return l.TopFraction >= minLetterboxFraction || l.BottomFraction >= minLetterboxFraction
}

// intersect keeps the SMALLER bar on each edge.
//
// One frame cannot tell a bar from the content: a fade to black, a night
// exterior, or titles over black all look exactly like a letterbox. A real
// baked-in bar is present in every frame, so the minimum across samples
// converges on the truth while a transient dark frame is discarded. This
// mirrors LetterboxInsets.intersect on the Android client.
func (l Letterbox) intersect(other Letterbox) Letterbox {
	return Letterbox{
		TopFraction:    min(l.TopFraction, other.TopFraction),
		BottomFraction: min(l.BottomFraction, other.BottomFraction),
	}
}

const (
	// Below this a "bar" is rounding noise in cropdetect's 2px rounding, or a
	// genuinely thin mastering artifact not worth moving subtitles for.
	minLetterboxFraction = 0.02
	// Above this the measurement is more likely wrong than right — a 25% bar on
	// each edge would leave half a frame — and acting on it would push
	// subtitles into the middle of the picture. Refusing beats guessing.
	maxLetterboxFraction = 0.25
	// cropdetect needs a handful of frames to settle; more per sample costs
	// decode time for very little extra confidence.
	framesPerSample = 12
	// Samples are spread across the runtime so an opening title card or a
	// closing fade cannot dominate the result.
	sampleCount = 4
	// A single sample that hangs must not hold a caller forever.
	sampleTimeout = 20 * time.Second
)

// DetectLetterbox samples [sampleCount] points across the runtime and returns
// the intersection of what cropdetect reports.
//
// A failed or nonsensical measurement returns a zero Letterbox rather than an
// error the caller has to interpret: no measurement and "no bars" lead to the
// same behavior, which is today's behavior.
func DetectLetterbox(ctx context.Context, inputPath string, durationSeconds float64, frameHeight int, ffmpegPath string) Letterbox {
	if strings.TrimSpace(inputPath) == "" || durationSeconds <= 0 || frameHeight <= 0 {
		return Letterbox{}
	}

	var result *Letterbox
	for _, position := range samplePositions(durationSeconds) {
		sample, ok := detectLetterboxAt(ctx, inputPath, position, frameHeight, ffmpegPath)
		if !ok {
			continue
		}
		if result == nil {
			sample := sample
			result = &sample
			continue
		}
		intersected := result.intersect(sample)
		result = &intersected
	}
	if result == nil {
		return Letterbox{}
	}
	return sanitizeLetterbox(*result)
}

// samplePositions spreads samples over the middle of the runtime, avoiding the
// opening and closing minutes where black frames are most likely.
func samplePositions(durationSeconds float64) []float64 {
	positions := make([]float64, 0, sampleCount)
	for i := 1; i <= sampleCount; i++ {
		fraction := float64(i) / float64(sampleCount+1)
		positions = append(positions, durationSeconds*fraction)
	}
	return positions
}

func detectLetterboxAt(ctx context.Context, inputPath string, seekSeconds float64, frameHeight int, ffmpegPath string) (Letterbox, bool) {
	sampleCtx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()

	// -ss before -i keeps the seek cheap (keyframe accurate is plenty here).
	// cropdetect writes its findings to stderr; the null muxer keeps the decode
	// from producing output nobody reads.
	cmd := exec.CommandContext(
		sampleCtx,
		letterboxFFmpegBinary(ffmpegPath),
		"-hide_banner",
		"-nostats",
		"-ss", strconv.FormatFloat(seekSeconds, 'f', 3, 64),
		"-i", inputPath,
		"-vf", "cropdetect=limit=24:round=2:reset=0",
		"-frames:v", strconv.Itoa(framesPerSample),
		"-an", "-sn",
		"-f", "null", "-",
	)
	output, err := cmd.CombinedOutput()
	if err != nil && len(output) == 0 {
		return Letterbox{}, false
	}
	return parseCropDetect(string(output), frameHeight)
}

func letterboxFFmpegBinary(ffmpegPath string) string {
	if trimmed := strings.TrimSpace(ffmpegPath); trimmed != "" {
		return trimmed
	}
	return "ffmpeg"
}

// parseCropDetect reads cropdetect's "crop=w:h:x:y" lines and converts the last
// one into fractions of frameHeight.
//
// The last line is the one that has seen every sampled frame, since reset=0
// makes cropdetect accumulate rather than restart per frame. frameHeight is
// passed in rather than inferred: the kept rectangle alone cannot reveal the
// frame, and assuming the bars are symmetric would silently mismeasure the
// sources that are not — which are exactly the ones worth getting right.
func parseCropDetect(ffmpegOutput string, frameHeight int) (Letterbox, bool) {
	if frameHeight <= 0 {
		return Letterbox{}, false
	}
	var (
		cropWidth, cropHeight, cropY int
		found                        bool
	)
	scanner := bufio.NewScanner(strings.NewReader(ffmpegOutput))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		index := strings.LastIndex(line, "crop=")
		if index < 0 {
			continue
		}
		fields := strings.Split(strings.TrimSpace(line[index+len("crop="):]), ":")
		if len(fields) != 4 {
			continue
		}
		values := make([]int, 0, 4)
		valid := true
		for _, field := range fields {
			value, err := strconv.Atoi(strings.TrimSpace(field))
			if err != nil || value < 0 {
				valid = false
				break
			}
			values = append(values, value)
		}
		if !valid {
			continue
		}
		cropWidth, cropHeight, cropY = values[0], values[1], values[3]
		found = true
	}
	if !found || cropWidth <= 0 || cropHeight <= 0 {
		return Letterbox{}, false
	}

	// cropdetect reports the KEPT rectangle: y is the top bar, and whatever is
	// left below it is the bottom bar. A crop taller than the frame means the
	// two disagree about the source, so trust neither.
	if cropHeight+cropY > frameHeight {
		return Letterbox{}, false
	}
	top := float64(cropY) / float64(frameHeight)
	bottom := float64(frameHeight-cropHeight-cropY) / float64(frameHeight)
	return Letterbox{TopFraction: top, BottomFraction: bottom}, true
}

// sanitizeLetterbox drops noise and refuses absurd measurements.
func sanitizeLetterbox(value Letterbox) Letterbox {
	clean := Letterbox{}
	if value.TopFraction >= minLetterboxFraction && value.TopFraction <= maxLetterboxFraction {
		clean.TopFraction = value.TopFraction
	}
	if value.BottomFraction >= minLetterboxFraction && value.BottomFraction <= maxLetterboxFraction {
		clean.BottomFraction = value.BottomFraction
	}
	return clean
}

// LetterboxCache measures each source at most once and never blocks a caller.
//
// Detection costs a few seeks and a dozen decoded frames — cheap once, far too
// slow to sit in the path of a playback-start response. Callers therefore get
// whatever is already known (nothing, on the first play of a file) and a
// measurement is warmed in the background for next time.
//
// In-memory and therefore cold after a restart: the first play of each file
// plans without geometry, which is the behavior that existed before this
// measurement did. Persisting it belongs on the media file row; see the note
// on LetterboxCacheKey.
type LetterboxCache struct {
	ffmpegPath func() string

	mu       sync.Mutex
	measured map[string]Letterbox
	// Insertion order, so the map can be bounded without holding a library's
	// worth of entries forever on a long-lived server.
	order    []string
	inflight map[string]struct{}
}

// maxLetterboxEntries bounds the cache. Well past any plausible working set of
// recently played files, and small enough that the map cannot grow into a leak
// on a server that has been up for months.
const maxLetterboxEntries = 4096

// LetterboxCacheKey identifies a measurement.
//
// Path alone is not enough: replacing a file in place — an upgrade to a better
// release, a re-encode — keeps the path and would otherwise serve the old
// file's bars forever, putting subtitles in the wrong place with no way for a
// user to clear it. Size changes on any real replacement, so the pair
// invalidates itself.
func LetterboxCacheKey(inputPath string, fileSize int64) string {
	return inputPath + "|" + strconv.FormatInt(fileSize, 10)
}

func NewLetterboxCache(ffmpegPath func() string) *LetterboxCache {
	return &LetterboxCache{
		ffmpegPath: ffmpegPath,
		measured:   make(map[string]Letterbox),
		inflight:   make(map[string]struct{}),
	}
}

// Lookup returns the known measurement for a source, if there is one.
func (c *LetterboxCache) Lookup(key string) (Letterbox, bool) {
	if c == nil {
		return Letterbox{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.measured[key]
	return value, ok
}

// Warm measures the source in the background unless it is already known or a
// measurement is already running for it. Safe to call on every playback start.
func (c *LetterboxCache) Warm(key, inputPath string, durationSeconds float64, frameHeight int) {
	if c == nil || strings.TrimSpace(inputPath) == "" || durationSeconds <= 0 || frameHeight <= 0 {
		return
	}
	c.mu.Lock()
	if _, done := c.measured[key]; done {
		c.mu.Unlock()
		return
	}
	if _, running := c.inflight[key]; running {
		c.mu.Unlock()
		return
	}
	c.inflight[key] = struct{}{}
	c.mu.Unlock()

	go func() {
		// Detached from the request that triggered it: the answer is for the
		// NEXT play, so a client disconnecting must not cancel the measurement.
		ctx, cancel := context.WithTimeout(context.Background(), sampleTimeout*sampleCount)
		defer cancel()
		ffmpegPath := ""
		if c.ffmpegPath != nil {
			ffmpegPath = c.ffmpegPath()
		}
		started := time.Now()
		value := DetectLetterbox(ctx, inputPath, durationSeconds, frameHeight, ffmpegPath)
		slog.Info("letterbox measured",
			"component", "playback",
			"input", inputPath,
			"frame_height", frameHeight,
			"top_fraction", value.TopFraction,
			"bottom_fraction", value.BottomFraction,
			"detected", value.Detected(),
			"took_ms", time.Since(started).Milliseconds(),
		)

		c.mu.Lock()
		defer c.mu.Unlock()
		// Store even a zero result: "measured, no bars" must not be retried on
		// every single playback of a normal 16:9 file.
		if _, exists := c.measured[key]; !exists {
			c.order = append(c.order, key)
		}
		c.measured[key] = value
		delete(c.inflight, key)
		c.evictLocked()
	}()
}

// evictLocked drops the oldest measurements once the cache is over its bound.
// Oldest-first is the right order here: the cost of a wrong guess is one
// re-measurement of a file nobody has played in a long time.
func (c *LetterboxCache) evictLocked() {
	for len(c.order) > maxLetterboxEntries {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.measured, oldest)
	}
}

func (l Letterbox) String() string {
	return fmt.Sprintf("top=%.4f bottom=%.4f", l.TopFraction, l.BottomFraction)
}
