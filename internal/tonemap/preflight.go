package tonemap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	sourcePreflightNegativeTTL     = 15 * time.Second
	sourcePreflightTimeoutSlack    = time.Second
	maxSourcePreflightCacheEntries = 4096
)

// SourcePreflightRequest freezes the source, executor, and environment facts
// required to validate an ambiguous HDR base signal before output is published.
type SourcePreflightRequest struct {
	FFmpegPath          string
	FFprobePath         string
	InputPath           string
	DurationSeconds     float64
	SourceBitDepth      int
	SoftwareVideoDecode bool
	Mode                Mode
	Backend             string
	Filter              string
	Kind                SourceKind
	RecipeVersion       string
	HardwareDevice      string
	DriverFingerprint   string
	SourceRevision      SourceRevision
}

// sourcePreflightCacheEntry stores a permanent success or a short-lived failure
// for one immutable source and executor identity.
type sourcePreflightCacheEntry struct {
	errorMessage string
	expiresAt    time.Time
	lastAccess   uint64
}

var sourcePreflightCache = struct {
	sync.Mutex
	entries    map[string]sourcePreflightCacheEntry
	nextAccess uint64
	group      singleflight.Group
}{entries: make(map[string]sourcePreflightCacheEntry)}

var ffmpegVersionCache = struct {
	sync.Mutex
	entries map[string][]byte
	group   singleflight.Group
}{entries: make(map[string][]byte)}

var (
	// ErrSourcePreflightUnavailable identifies an operational validation failure
	// that may succeed when the executor or probe is retried.
	ErrSourcePreflightUnavailable = errors.New("tone-map source preflight unavailable")
	// ErrSourcePreflightRejected identifies a completed validation whose decoded
	// source or converted output did not satisfy the frozen recipe.
	ErrSourcePreflightRejected = errors.New("tone-map source preflight rejected")
)

// ValidateSource confirms that representative decoded source frames match the
// frozen source kind and that the selected executor emits clean BT.709 output.
func ValidateSource(ctx context.Context, request SourcePreflightRequest) error {
	return ValidateSourceWithRunner(ctx, request, runCommand)
}

// ValidateSourceWithRunner performs source validation with an injectable
// command runner while preserving production caching and timeout behavior.
func ValidateSourceWithRunner(ctx context.Context, request SourcePreflightRequest, run CommandRunner) error {
	if request.Kind == "" || request.Mode == "" || strings.TrimSpace(request.InputPath) == "" {
		return fmt.Errorf("%w: incomplete tone-map source preflight", ErrSourcePreflightRejected)
	}
	if err := request.SourceRevision.ValidatePath(request.InputPath); err != nil {
		return err
	}
	if strings.TrimSpace(request.FFmpegPath) == "" {
		request.FFmpegPath = "ffmpeg"
	}
	if strings.TrimSpace(request.FFprobePath) == "" {
		request.FFprobePath = ffprobeForFFmpeg(request.FFmpegPath)
	}

	preflightCtx, cancel := context.WithTimeout(ctx, SourcePreflightTimeout(request.DurationSeconds))
	defer cancel()
	key, cacheable := sourcePreflightKey(preflightCtx, request, run)
	if err := preflightCtx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrSourcePreflightUnavailable, err)
	}
	if cacheable {
		entry, ok := sourcePreflightCacheLookup(key, time.Now())
		if ok {
			return cachedPreflightError(entry)
		}
		resultCh := sourcePreflightCache.group.DoChan(key, func() (any, error) {
			entry, ok := sourcePreflightCacheLookup(key, time.Now())
			if ok {
				return entry, nil
			}
			sharedCtx, sharedCancel := context.WithTimeout(context.Background(), sourcePreflightExecutionTimeout(request.DurationSeconds))
			defer sharedCancel()
			preflightErr := runSourcePreflight(sharedCtx, request, run)
			if err := sharedCtx.Err(); err != nil {
				return sourcePreflightCacheEntry{}, fmt.Errorf("%w: %w", ErrSourcePreflightUnavailable, err)
			}
			entry = sourcePreflightCacheEntry{}
			if preflightErr != nil {
				if !errors.Is(preflightErr, ErrSourcePreflightRejected) {
					return sourcePreflightCacheEntry{}, preflightErr
				}
				entry.errorMessage = preflightErr.Error()
				entry.expiresAt = time.Now().Add(sourcePreflightNegativeTTL)
			}
			sourcePreflightCacheStore(key, entry, time.Now())
			return entry, nil
		})
		select {
		case <-preflightCtx.Done():
			return fmt.Errorf("%w: %w", ErrSourcePreflightUnavailable, preflightCtx.Err())
		case result := <-resultCh:
			if result.Err != nil {
				return result.Err
			}
			entry, ok = result.Val.(sourcePreflightCacheEntry)
			if !ok {
				return errors.New("tone-map source preflight failed")
			}
			return cachedPreflightError(entry)
		}
	}
	return runSourcePreflight(preflightCtx, request, run)
}

// sourcePreflightCacheEntryCurrent reports whether a permanent success or
// unexpired negative verdict may be reused.
func sourcePreflightCacheEntryCurrent(entry sourcePreflightCacheEntry, now time.Time) bool {
	return entry.errorMessage == "" || now.Before(entry.expiresAt)
}

func sourcePreflightCacheLookup(key string, now time.Time) (sourcePreflightCacheEntry, bool) {
	sourcePreflightCache.Lock()
	defer sourcePreflightCache.Unlock()
	entry, ok := sourcePreflightCache.entries[key]
	if !ok || !sourcePreflightCacheEntryCurrent(entry, now) {
		return sourcePreflightCacheEntry{}, false
	}
	sourcePreflightCache.nextAccess++
	entry.lastAccess = sourcePreflightCache.nextAccess
	sourcePreflightCache.entries[key] = entry
	return entry, true
}

func sourcePreflightCacheStore(key string, entry sourcePreflightCacheEntry, now time.Time) {
	sourcePreflightCache.Lock()
	defer sourcePreflightCache.Unlock()
	for cachedKey, cached := range sourcePreflightCache.entries {
		if cached.errorMessage != "" && !now.Before(cached.expiresAt) {
			delete(sourcePreflightCache.entries, cachedKey)
		}
	}
	delete(sourcePreflightCache.entries, key)
	for len(sourcePreflightCache.entries) >= maxSourcePreflightCacheEntries {
		oldestKey := ""
		var oldestAccess uint64
		for cachedKey, cached := range sourcePreflightCache.entries {
			if oldestKey == "" || cached.lastAccess < oldestAccess {
				oldestKey = cachedKey
				oldestAccess = cached.lastAccess
			}
		}
		delete(sourcePreflightCache.entries, oldestKey)
	}
	sourcePreflightCache.nextAccess++
	entry.lastAccess = sourcePreflightCache.nextAccess
	sourcePreflightCache.entries[key] = entry
}

// SourcePreflightTimeout includes the FFmpeg identity lookup and the full
// shared validation command matrix.
func SourcePreflightTimeout(durationSeconds float64) time.Duration {
	return probeCommandTimeout + sourcePreflightExecutionTimeout(durationSeconds)
}

// sourcePreflightExecutionTimeout budgets inspection, conversion, and output
// validation for every representative source position.
func sourcePreflightExecutionTimeout(durationSeconds float64) time.Duration {
	commandCount := 3 * len(sourcePreflightPositions(durationSeconds))
	return time.Duration(commandCount)*probeCommandTimeout + sourcePreflightTimeoutSlack
}

// cachedPreflightError reconstructs the stored negative verdict without
// retaining mutable error implementations in the cache.
func cachedPreflightError(entry sourcePreflightCacheEntry) error {
	if entry.errorMessage == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrSourcePreflightRejected, entry.errorMessage)
}

// sourcePreflightKey binds a stable source revision to the exact FFmpeg binary,
// driver, device, filter, and recipe used by the executor.
func sourcePreflightKey(ctx context.Context, request SourcePreflightRequest, run CommandRunner) (string, bool) {
	if !request.SourceRevision.Stable() {
		return "", false
	}
	version, err := ffmpegVersionForPreflight(ctx, request.FFmpegPath, run)
	if err != nil || len(version) == 0 {
		return "", false
	}
	driver := strings.TrimSpace(request.DriverFingerprint)
	if driver == "" {
		driver = driverFingerprint(request.Backend, request.HardwareDevice)
	}
	executor := strings.Join([]string{
		strings.TrimSpace(request.FFmpegPath),
		hashRevisionValue(string(version)),
		strings.ToLower(strings.TrimSpace(request.Backend)),
		strings.TrimSpace(request.HardwareDevice),
		driver,
		string(request.Mode), string(request.Kind), request.Filter, request.RecipeVersion,
		strconv.FormatBool(request.SoftwareVideoDecode),
	}, "\x00")
	return request.SourceRevision.Fingerprint() + "\x00" + hashRevisionValue(executor), true
}

// ffmpegVersionForPreflight coalesces version lookups and invalidates cached
// output when the resolved binary's identity changes.
func ffmpegVersionForPreflight(ctx context.Context, ffmpegPath string, run CommandRunner) ([]byte, error) {
	resolved, cacheKey, cacheable := FFmpegBinaryIdentity(ffmpegPath)
	if !cacheable {
		return runBounded(ctx, run, ffmpegPath, "-version")
	}
	ffmpegVersionCache.Lock()
	if cached, ok := ffmpegVersionCache.entries[cacheKey]; ok {
		result := append([]byte(nil), cached...)
		ffmpegVersionCache.Unlock()
		return result, nil
	}
	ffmpegVersionCache.Unlock()
	resultCh := ffmpegVersionCache.group.DoChan(cacheKey, func() (any, error) {
		ffmpegVersionCache.Lock()
		cached, ok := ffmpegVersionCache.entries[cacheKey]
		ffmpegVersionCache.Unlock()
		if ok {
			return append([]byte(nil), cached...), nil
		}
		sharedCtx, sharedCancel := context.WithTimeout(context.Background(), probeCommandTimeout)
		defer sharedCancel()
		output, runErr := runBounded(sharedCtx, run, resolved, "-version")
		if sharedErr := sharedCtx.Err(); sharedErr != nil {
			return nil, sharedErr
		}
		if runErr != nil || len(output) == 0 {
			return output, runErr
		}
		ffmpegVersionCache.Lock()
		ffmpegVersionCache.entries[cacheKey] = append([]byte(nil), output...)
		ffmpegVersionCache.Unlock()
		return output, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		output, ok := result.Val.([]byte)
		if !ok {
			return nil, errors.New("ffmpeg version unavailable")
		}
		return append([]byte(nil), output...), nil
	}
}

// FFmpegBinaryIdentity resolves a regular FFmpeg binary and derives an identity
// that changes when the executable is replaced in place. It returns the
// resolved path, the identity, and whether the binary resolved to a regular
// file; when it did not, the path is returned unchanged and the identity is
// empty. Other per-binary caches, such as mediasample's capability inventory,
// key on the same identity.
func FFmpegBinaryIdentity(ffmpegPath string) (resolved, identity string, ok bool) {
	resolved, err := exec.LookPath(strings.TrimSpace(ffmpegPath))
	if err != nil {
		return ffmpegPath, "", false
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return ffmpegPath, "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return ffmpegPath, "", false
	}
	return resolved, strings.Join([]string{
		resolved,
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}, "\x00"), true
}

// driverFingerprint hashes the configured backend and available kernel-driver
// version facts so cached preflights do not cross driver changes.
func driverFingerprint(backend, device string) string {
	values := []string{strings.ToLower(strings.TrimSpace(backend)), strings.TrimSpace(device)}
	device = firstDevice(device)
	if strings.HasPrefix(device, "/dev/dri/") {
		name := filepath.Base(device)
		for _, path := range []string{
			filepath.Join("/sys/class/drm", name, "device", "uevent"),
			filepath.Join("/sys/class/drm", name, "device", "driver", "module", "version"),
		} {
			if data, err := os.ReadFile(path); err == nil {
				values = append(values, string(data))
			}
		}
	}
	if strings.EqualFold(backend, BackendNVENC) {
		if data, err := os.ReadFile("/proc/driver/nvidia/version"); err == nil {
			values = append(values, string(data))
		}
	}
	return hashRevisionValue(strings.Join(values, "\x00"))
}

// runSourcePreflight validates representative source frames, converts each one,
// and inspects every result before accepting the recipe.
func runSourcePreflight(ctx context.Context, request SourcePreflightRequest, run CommandRunner) error {
	start, err := sourceStartTime(ctx, request, run)
	if err != nil {
		return err
	}
	previous := 0.0
	for _, offset := range sourcePreflightPositions(request.DurationSeconds) {
		// The first sample reads from the start of the file: the opening
		// keyframe can be decoded before the first presentation time that
		// start_time reports.
		position := 0.0
		if offset > 0 {
			position = start + offset
		}
		frame, position, err := inspectSourceFrame(ctx, request, previous, position, run)
		if err != nil {
			return err
		}
		previous = position
		if !frameMatchesSourceKind(frame, request.Kind) {
			return fmt.Errorf("%w: decoded frame metadata does not match %s fallback", ErrSourcePreflightRejected, request.Kind)
		}
		file, err := os.CreateTemp("", "silo-tonemap-preflight-*.mkv")
		if err != nil {
			return fmt.Errorf("%w: create tone-map preflight output: %w", ErrSourcePreflightUnavailable, err)
		}
		outputPath := file.Name()
		if err := file.Close(); err != nil {
			_ = os.Remove(outputPath)
			return fmt.Errorf("%w: close tone-map preflight output: %w", ErrSourcePreflightUnavailable, err)
		}
		args := sourceConversionPreflightArgs(request, position, outputPath)
		if output, err := runBounded(ctx, run, request.FFmpegPath, args...); err != nil {
			_ = os.Remove(outputPath)
			return fmt.Errorf("%w: tone-map source conversion failed: %w (%s)", ErrSourcePreflightUnavailable, err, boundedCommandFailure(err, output))
		}
		if err := inspectPreflightOutput(ctx, request.FFprobePath, outputPath, run); err != nil {
			_ = os.Remove(outputPath)
			return err
		}
		_ = os.Remove(outputPath)
	}
	return nil
}

// sourceStartTime returns the timestamp the source starts at, so sample
// positions fall inside it. ffprobe's -read_intervals and ffmpeg's -ss with
// -seek_timestamp both take the stream's own timestamps, and an MPEG-TS
// rarely starts at 0. A negative or unknown start counts as 0.
func sourceStartTime(ctx context.Context, request SourcePreflightRequest, run CommandRunner) (float64, error) {
	args := []string{"-v", ffmpegErrorLogLevel, "-show_entries", "format=start_time", "-of", "json", request.InputPath}
	output, err := runBounded(ctx, run, request.FFprobePath, args...)
	if err != nil {
		return 0, fmt.Errorf("%w: read tone-map source start time: %w (%s)", ErrSourcePreflightUnavailable, err, boundedCommandFailure(err, output))
	}
	return parseSourceStartTime(output), nil
}

// parseSourceStartTime reads format.start_time from ffprobe JSON, or 0.
func parseSourceStartTime(output []byte) float64 {
	var payload struct {
		Format struct {
			StartTime string `json:"start_time"`
		} `json:"format"`
	}
	if decodeCommandJSON(output, &payload) != nil {
		return 0
	}
	start, err := strconv.ParseFloat(payload.Format.StartTime, 64)
	if err != nil || start < 0 {
		return 0
	}
	return start
}

// sourcePreflightPositions samples the beginning, middle, and near-end of a
// source while avoiding redundant positions for very short media.
func sourcePreflightPositions(duration float64) []float64 {
	positions := []float64{0}
	for _, position := range []float64{duration * 0.5, duration * 0.9} {
		if duration <= 2 || position <= 0 {
			continue
		}
		duplicate := false
		for _, existing := range positions {
			if position-existing < 1 && existing-position < 1 {
				duplicate = true
				break
			}
		}
		if !duplicate {
			positions = append(positions, position)
		}
	}
	return positions
}

// preflightFrame is the minimal decoded-frame metadata needed to confirm a base
// signal classification.
type preflightFrame struct {
	ColorRange     string           `json:"color_range"`
	ColorSpace     string           `json:"color_space"`
	ColorTransfer  string           `json:"color_transfer"`
	ColorPrimaries string           `json:"color_primaries"`
	SideData       []sideDataRecord `json:"side_data_list"`
}

// sideDataRecord is one FFprobe side-data entry, identified by its label.
type sideDataRecord struct {
	Type string `json:"side_data_type"`
}

// hasDolbyVisionMetadata reports whether the decoder attached a parsed RPU,
// which FFprobe labels "Dolby Vision Metadata".
func (frame preflightFrame) hasDolbyVisionMetadata() bool {
	return slices.ContainsFunc(frame.SideData, func(sideData sideDataRecord) bool {
		return strings.EqualFold(strings.TrimSpace(sideData.Type), "Dolby Vision Metadata")
	})
}

// sourceKeyframeSearchPackets bounds the packet scan for the first keyframe
// after a seek. It covers the longest default keyframe interval of common
// encoders (x264 and x265 use 250 frames).
const sourceKeyframeSearchPackets = 300

// inspectSourceFrame reads color metadata from one decoded frame at position
// and returns the position the frame was read from, which the conversion must
// sample too.
//
// The seek lands on a keyframe in an indexed container, so one packet decodes.
// In MPEG-TS it lands on whatever packet has that timestamp, which rarely
// decodes alone. Then a demux-only scan from the same seek finds the next
// keyframe, and the read is repeated through that packet. The packets before
// it lack their references, so the decoder drops them cheaply and the read
// costs about one frame's decode.
//
// When the scan reaches the end of the stream without a keyframe, position
// lies in the last keyframe interval and nothing after it decodes. The sample
// then moves back to that interval's keyframe, found by a scan from the
// previous sample, which lies before it.
//
// previous and position are in the stream's own timestamps.
func inspectSourceFrame(ctx context.Context, request SourcePreflightRequest, previous, position float64, run CommandRunner) (preflightFrame, float64, error) {
	frame, found, endOfStream, err := readFrameFrom(ctx, request, position, run)
	if err == nil && !found && endOfStream && previous < position {
		frame, position, found, err = readLastKeyframeBefore(ctx, request, previous, position, run)
	}
	if err != nil {
		return preflightFrame{}, 0, err
	}
	if !found {
		return preflightFrame{}, 0, fmt.Errorf("%w: tone-map source frame metadata unavailable", ErrSourcePreflightRejected)
	}
	return frame, position, nil
}

// readLastKeyframeBefore reads the stream's last keyframe, found by a scan from
// previous, and returns the position it was read from.
func readLastKeyframeBefore(ctx context.Context, request SourcePreflightRequest, previous, position float64, run CommandRunner) (preflightFrame, float64, bool, error) {
	keyframe, ok, err := lastKeyframeFrom(ctx, request, previous, run)
	if err != nil || !ok {
		return preflightFrame{}, position, false, err
	}
	// Seek to just before the keyframe in decode order, so the read starts
	// ahead of it and the scan finds it.
	position = max(0, keyframe-0.001)
	frame, found, _, err := readFrameFrom(ctx, request, position, run)
	return frame, position, found, err
}

// readFrameFrom reads the first frame that decodes from position on, through
// the next keyframe when the first packet decodes nothing. endOfStream reports
// that no keyframe follows before the stream ends.
func readFrameFrom(ctx context.Context, request SourcePreflightRequest, position float64, run CommandRunner) (frame preflightFrame, found, endOfStream bool, err error) {
	frames, err := readSourceFrames(ctx, request, position, 1, run)
	if err != nil || len(frames) > 0 {
		return firstFrame(frames), len(frames) > 0, false, err
	}
	packets, endOfStream, err := packetsThroughNextKeyframe(ctx, request, position, run)
	if err != nil || packets == 0 {
		return preflightFrame{}, false, endOfStream, err
	}
	if frames, err = readSourceFrames(ctx, request, position, packets, run); err != nil {
		return preflightFrame{}, false, false, err
	}
	return firstFrame(frames), len(frames) > 0, false, nil
}

func firstFrame(frames []preflightFrame) preflightFrame {
	if len(frames) == 0 {
		return preflightFrame{}
	}
	return frames[0]
}

// sourceProbeArgs builds an ffprobe command that reads interval of the primary
// video stream.
func sourceProbeArgs(request SourcePreflightRequest, logLevel, interval string, show ...string) []string {
	args := append([]string{"-v", logLevel, "-read_intervals", interval, "-select_streams", "v:0"}, show...)
	return append(args, "-of", "json", request.InputPath)
}

// formatPreflightPosition formats a sample position, in seconds of the
// stream's own timestamps, for ffprobe's -read_intervals and ffmpeg's -ss.
func formatPreflightPosition(position float64) string {
	return strconv.FormatFloat(position, 'f', 3, 64)
}

// readSourceFrames decodes the first packets video packets at position and
// returns the frames they produced.
func readSourceFrames(ctx context.Context, request SourcePreflightRequest, position float64, packets int, run CommandRunner) ([]preflightFrame, error) {
	interval := formatPreflightPosition(position) + "%+#" + strconv.Itoa(packets)
	args := sourceProbeArgs(request, ffmpegErrorLogLevel, interval,
		"-show_frames",
		"-show_entries", "frame=color_range,color_space,color_transfer,color_primaries:frame_side_data=side_data_type")
	output, err := runBounded(ctx, run, request.FFprobePath, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect tone-map source frame: %w (%s)", ErrSourcePreflightUnavailable, err, boundedCommandFailure(err, output))
	}
	var payload struct {
		Frames []preflightFrame `json:"frames"`
	}
	if err := decodeCommandJSON(output, &payload); err != nil {
		return nil, fmt.Errorf("%w: tone-map source frame metadata unavailable", ErrSourcePreflightRejected)
	}
	return payload.Frames, nil
}

// packetsThroughNextKeyframe returns how many packets from position reach the
// first keyframe after the first packet, which the one-packet read already
// failed to decode, or 0 when none lies within sourceKeyframeSearchPackets.
// endOfStream reports that the stream ended within that bound. The scan reads
// one packet past the bound, so a stream that ends exactly there still counts.
// It only demuxes, so it costs no decoding.
func packetsThroughNextKeyframe(ctx context.Context, request SourcePreflightRequest, position float64, run CommandRunner) (packets int, endOfStream bool, err error) {
	interval := formatPreflightPosition(position) + "%+#" + strconv.Itoa(sourceKeyframeSearchPackets+1)
	scanned, err := scanSourcePackets(ctx, request, interval, "flags", run)
	if err != nil {
		return 0, false, err
	}
	for index := 1; index < min(len(scanned), sourceKeyframeSearchPackets); index++ {
		if strings.HasPrefix(scanned[index].Flags, "K") {
			return index + 1, false, nil
		}
	}
	return 0, len(scanned) <= sourceKeyframeSearchPackets, nil
}

// sourceFinalKeyframeSearchPackets is how many packets each scan for the last
// keyframe reads. A sample in the last keyframe interval lies within
// sourceKeyframeSearchPackets of the end, and at least 10% of the duration
// lies after it, so at a steady frame rate one scan from the previous sample
// reaches the end. A variable frame rate can need more.
const sourceFinalKeyframeSearchPackets = 10 * sourceKeyframeSearchPackets

// lastKeyframeFrom returns the decode timestamp of the last keyframe the stream
// holds from position on. It only demuxes, sourceFinalKeyframeSearchPackets
// packets at a time, each scan going on from the last packet of the one
// before, until a scan ends short. The scans are bounded by packets, not by an
// end time: ffprobe ends a timed read at the first packet presented at or
// after it, which can come before a keyframe decoded earlier than it is
// presented.
func lastKeyframeFrom(ctx context.Context, request SourcePreflightRequest, position float64, run CommandRunner) (float64, bool, error) {
	keyframe, found := 0.0, false
	for {
		interval := formatPreflightPosition(position) + "%+#" + strconv.Itoa(sourceFinalKeyframeSearchPackets)
		scanned, err := scanSourcePackets(ctx, request, interval, "flags,dts_time", run)
		if err != nil {
			return 0, false, err
		}
		for _, packet := range scanned {
			if dts, ok := packet.dts(); ok && strings.HasPrefix(packet.Flags, "K") {
				keyframe, found = dts, true
			}
		}
		if len(scanned) < sourceFinalKeyframeSearchPackets {
			return keyframe, found, nil
		}
		// The scan stopped at its bound, so the stream goes on. A packet
		// without a later timestamp to resume from ends the search with what
		// it found.
		next, ok := scanned[len(scanned)-1].dts()
		if !ok || next <= position {
			return keyframe, found, nil
		}
		position = next
	}
}

// sourcePacket is one demuxed packet of the primary video stream.
type sourcePacket struct {
	Flags   string `json:"flags"`
	DTSTime string `json:"dts_time"`
}

// dts returns the packet's decode timestamp in seconds, or false when ffprobe
// reported none.
func (packet sourcePacket) dts() (float64, bool) {
	dts, err := strconv.ParseFloat(packet.DTSTime, 64)
	return dts, err == nil
}

// scanSourcePackets lists the primary video stream's packets in interval
// without decoding them.
//
// ffprobe writes each packet's JSON as it reads it, and the runner shares one
// pipe between stdout and stderr, so a parser diagnostic about a damaged
// header would land inside the JSON. The scan therefore logs nothing.
func scanSourcePackets(ctx context.Context, request SourcePreflightRequest, interval, entries string, run CommandRunner) ([]sourcePacket, error) {
	args := sourceProbeArgs(request, "quiet", interval, "-show_packets", "-show_entries", "packet="+entries)
	output, err := runBounded(ctx, run, request.FFprobePath, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: find tone-map source keyframe: %w (%s)", ErrSourcePreflightUnavailable, err, boundedCommandFailure(err, output))
	}
	var payload struct {
		Packets []sourcePacket `json:"packets"`
	}
	if err := decodeCommandJSON(output, &payload); err != nil {
		return nil, fmt.Errorf("%w: tone-map source packet metadata unavailable", ErrSourcePreflightRejected)
	}
	return payload.Packets, nil
}

// frameMatchesSourceKind requires complete decoded-frame metadata compatible
// with the frozen base-signal classification. The only Dolby Vision base that
// is preflighted is a PQ-tagged compatibility id 0 (Profile 5 needs none), so
// its frames must keep the PQ transfer and carry the RPU that maps them.
func frameMatchesSourceKind(frame preflightFrame, kind SourceKind) bool {
	if kind == SourceDolbyVision {
		return frame.hasDolbyVisionMetadata() && transferIsPQ(normalizeColorValue(frame.ColorTransfer))
	}
	complete, compatible := sourceMetadataCompatibility(kind, SourceMetadata{
		ColorRange: frame.ColorRange, ColorPrimaries: frame.ColorPrimaries,
		ColorTransfer: frame.ColorTransfer, ColorSpace: frame.ColorSpace,
	})
	return complete && compatible
}

// sourceConversionPreflightArgs builds a one-frame FFmpeg conversion that uses
// the same device, filter, and encoder family as the frozen recipe.
func sourceConversionPreflightArgs(request SourcePreflightRequest, position float64, outputPath string) []string {
	args := []string{ffmpegHideBannerArg, ffmpegLogLevelArg, ffmpegErrorLogLevel}
	device := firstDevice(request.HardwareDevice)
	if request.Mode == ModeHardware {
		switch request.Backend {
		case BackendQSV:
			if device == "" {
				device = defaultDRIRenderDevice
			}
			args = append(args, QSVInitDeviceArgs(device)...)
			args = append(args, "-init_hw_device", "opencl=ocl@va", "-filter_hw_device", "va")
			if !request.SoftwareVideoDecode {
				args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
			}
		case BackendVAAPI:
			if device == "" {
				device = defaultDRIRenderDevice
			}
			args = append(args, VAAPIInitDeviceArgs("va", device)...)
			args = append(args, VAAPIOpenCLInitArgs("va", request.Filter, request.Kind)...)
			args = append(args, "-filter_hw_device", "va")
			if !request.SoftwareVideoDecode {
				args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
			}
		case BackendNVENC:
			if device == "" {
				device = "0"
			}
			args = append(args, "-init_hw_device", "cuda=cu:"+device, "-filter_hw_device", "cu", "-hwaccel", "cuda", "-hwaccel_output_format", "cuda")
		case BackendVideoToolbox:
			if request.SoftwareVideoDecode {
				args = append(args, "-init_hw_device", "videotoolbox=vt", "-filter_hw_device", "vt")
			} else {
				args = append(args, "-hwaccel", BackendVideoToolbox, "-hwaccel_output_format", "videotoolbox_vld")
			}
		}
	}
	// position is in the stream's own timestamps, as ffprobe's -read_intervals
	// takes them. -ss alone is relative to the file's start time (about 1.4 s
	// into a typical MPEG-TS); -seek_timestamp makes it match, so the
	// conversion samples the frame the inspection read.
	args = append(args, "-seek_timestamp", "1", "-ss", formatPreflightPosition(position), "-i", request.InputPath, "-map", "0:v:0", "-frames:v", "1", "-an", "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1")
	filter := sourceConversionPreflightFilter(request)
	args = append(args, "-vf", filter, "-c:v", sourcePreflightEncoder(request), "-color_range", "tv", "-color_primaries", colorBT709, "-color_trc", colorBT709)
	// VAAPI/QSV/CUDA filters stamp the BT.709 matrix on hardware frames; an
	// explicit mux-boundary colorspace can insert an unsupported conversion.
	// VideoToolbox has downloaded an NV12 frame and needs the explicit matrix
	// because its encoder otherwise preserves the source BT.2020 matrix.
	if request.Mode == ModeSoftware || request.Backend == BackendVideoToolbox {
		args = append(args, "-colorspace", colorBT709)
	}
	if outputPath == "" {
		return append(args, "-f", "null", "-")
	}
	return append(args, "-f", "matroska", "-y", outputPath)
}

// sourceConversionPreflightFilter builds the selected executor's graph,
// including the download path needed for CUDA color conversion of SDR bases.
func sourceConversionPreflightFilter(request SourcePreflightRequest) string {
	if request.Mode == ModeSoftware {
		return SoftwareFilter(request.Kind, request.Filter)
	}
	if request.Backend == BackendVideoToolbox {
		filter := ""
		if request.SoftwareVideoDecode {
			filter = VideoToolboxUploadFilter(request.Kind, request.SourceBitDepth) + ","
		}
		return filter + VideoToolboxFilter("iw", "ih") + "," + VideoToolboxDownloadFilter(request.SourceBitDepth) + "," + HDRMetadataRemovalFilter()
	}
	if request.Backend == BackendNVENC {
		if IsSDRSource(request.Kind) {
			return "hwdownload,format=" + NVENCSoftwareFallbackPixelFormat(request.SourceBitDepth) + "," + SoftwareFilter(request.Kind, "") + ",format=nv12,hwupload_cuda"
		}
		return SourceParameters(request.Kind) + "," + CUDAFilter(request.Kind) + "," + HDRMetadataRemovalFilter()
	}
	if request.Backend == BackendQSV || request.Filter == HardwareFilterOpenCL {
		filter := IntelFilter(request.Kind, 0)
		if request.Backend == BackendQSV {
			filter += "," + QSVInteropFilter()
		}
		return intelOpenCLPreflightUpload(request) + filter + "," + HDRMetadataRemovalFilter()
	}
	filter := VAAPIFilter(request.Kind)
	if request.SoftwareVideoDecode {
		filter = SoftwareUploadFilter(request.Kind) + filter
	}
	return filter + "," + HDRMetadataRemovalFilter()
}

// intelOpenCLPreflightUpload uploads CPU-decoded frames for IntelFilter at the
// depth playback uploads them: 10-bit for HDR, which the OpenCL tone mapper
// reads, and 8-bit for SDR bases.
func intelOpenCLPreflightUpload(request SourcePreflightRequest) string {
	if !request.SoftwareVideoDecode {
		return ""
	}
	return SoftwareUploadFilter(request.Kind)
}

// sourcePreflightEncoder returns the encoder paired with the selected mode.
func sourcePreflightEncoder(request SourcePreflightRequest) string {
	if request.Mode == ModeSoftware {
		return "libx264"
	}
	return hardwareEncoder(request.Backend)
}

// inspectPreflightOutput requires H.264 yuv420p, complete limited-range BT.709
// metadata on stream and frames, and no residual HDR side data.
func inspectPreflightOutput(ctx context.Context, ffprobePath, outputPath string, run CommandRunner) error {
	args := []string{
		"-v", ffmpegErrorLogLevel, "-select_streams", "v:0",
		"-show_frames",
		"-show_entries", "stream=codec_name,pix_fmt,color_range,color_space,color_transfer,color_primaries:stream_side_data=side_data_type:frame=color_range,color_space,color_transfer,color_primaries:frame_side_data=side_data_type",
		"-of", "json", outputPath,
	}
	output, err := runBounded(ctx, run, ffprobePath, args...)
	if err != nil {
		return fmt.Errorf("%w: inspect tone-map preflight output: %w (%s)", ErrSourcePreflightUnavailable, err, boundedCommandFailure(err, output))
	}
	var payload struct {
		Streams []struct {
			CodecName      string           `json:"codec_name"`
			PixelFormat    string           `json:"pix_fmt"`
			ColorRange     string           `json:"color_range"`
			ColorSpace     string           `json:"color_space"`
			ColorTransfer  string           `json:"color_transfer"`
			ColorPrimaries string           `json:"color_primaries"`
			SideData       []sideDataRecord `json:"side_data_list"`
		} `json:"streams"`
		Frames []preflightFrame `json:"frames"`
	}
	if err := decodeCommandJSON(output, &payload); err != nil || len(payload.Streams) != 1 {
		return fmt.Errorf("%w: tone-map preflight output metadata unavailable", ErrSourcePreflightRejected)
	}
	stream := payload.Streams[0]
	if stream.CodecName != "h264" || stream.PixelFormat != "yuv420p" || !rangeIsLimited(normalizeColorValue(stream.ColorRange)) ||
		!colorIsBT709(normalizeColorValue(stream.ColorSpace)) || !colorIsBT709(normalizeColorValue(stream.ColorTransfer)) || !colorIsBT709(normalizeColorValue(stream.ColorPrimaries)) {
		return fmt.Errorf("%w: tone-map preflight output is not limited-range BT.709 H.264", ErrSourcePreflightRejected)
	}
	if len(payload.Frames) == 0 {
		return fmt.Errorf("%w: tone-map preflight output frame metadata unavailable", ErrSourcePreflightRejected)
	}
	allSideData := append([]sideDataRecord(nil), stream.SideData...)
	for _, frame := range payload.Frames {
		if complete, compatible := sourceMetadataCompatibility(SourceSDRBT709, SourceMetadata{
			ColorRange: frame.ColorRange, ColorPrimaries: frame.ColorPrimaries,
			ColorTransfer: frame.ColorTransfer, ColorSpace: frame.ColorSpace,
		}); !complete || !compatible {
			return fmt.Errorf("%w: tone-map preflight output frame is not limited-range BT.709", ErrSourcePreflightRejected)
		}
		allSideData = append(allSideData, frame.SideData...)
	}
	for _, sideData := range allSideData {
		if isHDRSideData(sideData.Type) {
			return fmt.Errorf("%w: tone-map preflight output retains HDR metadata", ErrSourcePreflightRejected)
		}
	}
	return nil
}

// isHDRSideData recognizes HDR and Dolby Vision side-data labels emitted by
// supported FFprobe versions.
func isHDRSideData(value string) bool {
	normalized := strings.ToLower(value)
	return strings.Contains(normalized, "dovi") || strings.Contains(normalized, "dolby") ||
		strings.Contains(normalized, "mastering") || strings.Contains(normalized, "content light") ||
		strings.Contains(normalized, "hdr")
}

// ffprobeForFFmpeg selects the sibling ffprobe binary when the configured path
// names FFmpeg, preserving directory and filename suffixes.
func ffprobeForFFmpeg(ffmpegPath string) string {
	dir, base := filepath.Split(ffmpegPath)
	if index := strings.Index(strings.ToLower(base), "ffmpeg"); index >= 0 {
		base = base[:index] + "ffprobe" + base[index+len("ffmpeg"):]
		return filepath.Join(dir, base)
	}
	return "ffprobe"
}

// decodeCommandJSON extracts the outer JSON object from command output that may
// contain diagnostics before or after the payload.
func decodeCommandJSON(output []byte, target any) error {
	start := bytes.IndexByte(output, '{')
	end := bytes.LastIndexByte(output, '}')
	if start < 0 || end < start {
		return errors.New("JSON payload unavailable")
	}
	return json.Unmarshal(output[start:end+1], target)
}

// boundedCommandFailure appends at most the final 512 bytes of command output
// so returned diagnostics remain useful without becoming unbounded.
func boundedCommandFailure(err error, output []byte) string {
	message := err.Error()
	if detail := strings.TrimSpace(string(output)); detail != "" {
		if len(detail) > 512 {
			detail = detail[len(detail)-512:]
		}
		message += ": " + detail
	}
	return message
}
