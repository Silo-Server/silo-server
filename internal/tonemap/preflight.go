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
	resolved, cacheKey, cacheable := ffmpegBinaryCacheKey(ffmpegPath)
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

// ffmpegBinaryCacheKey resolves a regular FFmpeg binary and derives an identity
// that changes when the executable is replaced in place.
func ffmpegBinaryCacheKey(ffmpegPath string) (string, string, bool) {
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
	positions := sourcePreflightPositions(request.DurationSeconds)
	for _, position := range positions {
		frame, err := inspectSourceFrame(ctx, request, position, run)
		if err != nil {
			return err
		}
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
	ColorRange     string `json:"color_range"`
	ColorSpace     string `json:"color_space"`
	ColorTransfer  string `json:"color_transfer"`
	ColorPrimaries string `json:"color_primaries"`
}

// inspectSourceFrame reads color metadata from one decoded frame at position.
func inspectSourceFrame(ctx context.Context, request SourcePreflightRequest, position float64, run CommandRunner) (preflightFrame, error) {
	interval := strconv.FormatFloat(position, 'f', 3, 64) + "%+#1"
	args := []string{
		"-v", ffmpegErrorLogLevel, "-read_intervals", interval, "-select_streams", "v:0",
		"-show_frames",
		"-show_entries", "frame=color_range,color_space,color_transfer,color_primaries",
		"-of", "json", request.InputPath,
	}
	output, err := runBounded(ctx, run, request.FFprobePath, args...)
	if err != nil {
		return preflightFrame{}, fmt.Errorf("%w: inspect tone-map source frame: %w (%s)", ErrSourcePreflightUnavailable, err, boundedCommandFailure(err, output))
	}
	var payload struct {
		Frames []preflightFrame `json:"frames"`
	}
	if err := decodeCommandJSON(output, &payload); err != nil || len(payload.Frames) == 0 {
		return preflightFrame{}, fmt.Errorf("%w: tone-map source frame metadata unavailable", ErrSourcePreflightRejected)
	}
	return payload.Frames[0], nil
}

// frameMatchesSourceKind requires complete decoded-frame metadata compatible
// with the frozen base-signal classification.
func frameMatchesSourceKind(frame preflightFrame, kind SourceKind) bool {
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
	args = append(args, "-ss", strconv.FormatFloat(position, 'f', 3, 64), "-i", request.InputPath, "-map", "0:v:0", "-frames:v", "1", "-an", "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1")
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
		return SourceParameters(request.Kind) + "," + CUDAFilter() + "," + HDRMetadataRemovalFilter()
	}
	filter := VAAPIFilter(request.Kind)
	if request.SoftwareVideoDecode && (request.Backend == BackendQSV || request.Backend == BackendVAAPI) {
		filter = "format=nv12,hwupload," + filter
	}
	if request.Backend == BackendQSV {
		filter = QSVFilter(request.Kind) + "," + QSVInteropFilter()
		if request.SoftwareVideoDecode {
			format := "nv12"
			if !IsSDRSource(request.Kind) {
				format = "p010le"
			}
			filter = "format=" + format + ",hwupload," + filter
		}
	}
	return filter + "," + HDRMetadataRemovalFilter()
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
	type sideDataRecord struct {
		Type string `json:"side_data_type"`
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
		Frames []struct {
			ColorRange     string           `json:"color_range"`
			ColorSpace     string           `json:"color_space"`
			ColorTransfer  string           `json:"color_transfer"`
			ColorPrimaries string           `json:"color_primaries"`
			SideData       []sideDataRecord `json:"side_data_list"`
		} `json:"frames"`
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
