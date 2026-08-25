package tonemap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestValidateSourceCachesPositiveVerdicts verifies completed successful validation is reused.
func TestValidateSourceCachesPositiveVerdicts(t *testing.T) {
	tests := []struct {
		name            string
		wantConversions int
	}{
		{name: "positive", wantConversions: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetSourcePreflightCache(t)
			request := sourcePreflightTestRequest(t)
			conversions := 0
			runner := sourcePreflightTestRunner(&conversions, func() string { return "ffmpeg version 1" }, nil)
			for attempt := 0; attempt < 2; attempt++ {
				err := ValidateSourceWithRunner(context.Background(), request, runner)
				if err != nil {
					t.Fatalf("ValidateSourceWithRunner() error = %v", err)
				}
			}
			if conversions != tt.wantConversions {
				t.Fatalf("conversion calls = %d, want %d before cached verdict", conversions, tt.wantConversions)
			}
		})
	}
}

// TestValidateSourceCacheInvalidatesExecutorAndSourceFacts verifies cache keys bind every frozen input.
func TestValidateSourceCacheInvalidatesExecutorAndSourceFacts(t *testing.T) {
	resetSourcePreflightCache(t)
	request := sourcePreflightTestRequest(t)
	version := "ffmpeg version 1"
	conversions := 0
	runner := sourcePreflightTestRunner(&conversions, func() string { return version }, nil)
	validate := func(label string, request SourcePreflightRequest) {
		t.Helper()
		before := conversions
		if err := ValidateSourceWithRunner(context.Background(), request, runner); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if conversions != before+3 {
			t.Fatalf("%s conversion calls = %d, want %d", label, conversions, before+3)
		}
	}

	validate("initial", request)
	request.RecipeVersion = "2"
	validate("recipe", request)
	replacementFFmpeg := request.FFmpegPath + "-replacement"
	if err := os.WriteFile(replacementFFmpeg, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	request.FFmpegPath = replacementFFmpeg
	validate("binary", request)
	version = "ffmpeg version 2"
	info, err := os.Stat(request.FFmpegPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(request.FFmpegPath, changed, changed); err != nil {
		t.Fatal(err)
	}
	validate("version", request)
	request.HardwareDevice = "/dev/dri/renderD129"
	validate("device", request)
	request.DriverFingerprint = "driver-2"
	validate("driver", request)
	request.SourceRevision.FileHash = "replacement-hash"
	validate("file revision", request)
	request.SourceRevision.ProbeUpdatedUnixNano++
	validate("rescan", request)
	request.SourceRevision.StreamSignature = "replacement-stream"
	validate("stream signature", request)
	request.SoftwareVideoDecode = true
	validate("software decode", request)
}

// TestValidateSourceDoesNotCacheOperationalFailure verifies transient failures are retried immediately.
func TestValidateSourceDoesNotCacheOperationalFailure(t *testing.T) {
	resetSourcePreflightCache(t)
	request := sourcePreflightTestRequest(t)
	conversions := 0
	runner := sourcePreflightTestRunner(&conversions, func() string { return "ffmpeg version 1" }, errors.New("temporary device failure"))
	for attempt := 0; attempt < 2; attempt++ {
		if err := ValidateSourceWithRunner(context.Background(), request, runner); !errors.Is(err, ErrSourcePreflightUnavailable) {
			t.Fatalf("temporary failure = %v, want ErrSourcePreflightUnavailable", err)
		}
	}
	if conversions != 2 {
		t.Fatalf("conversion calls = %d, want every operational failure retried", conversions)
	}
}

func TestValidateSourceClassifiesDeterministicRejection(t *testing.T) {
	resetSourcePreflightCache(t)
	request := sourcePreflightTestRequest(t)
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) == 1 && args[0] == "-version" {
			return []byte("ffmpeg version deterministic"), nil
		}
		if strings.Contains(name, "ffprobe") {
			return []byte(`{"frames":[{"color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709"}]}`), nil
		}
		return nil, nil
	}
	err := ValidateSourceWithRunner(context.Background(), request, runner)
	if !errors.Is(err, ErrSourcePreflightRejected) || errors.Is(err, ErrSourcePreflightUnavailable) {
		t.Fatalf("deterministic mismatch = %v, want ErrSourcePreflightRejected only", err)
	}
}

func TestValidateSourceWriteEvictsExpiredNegativeEntries(t *testing.T) {
	resetSourcePreflightCache(t)
	now := time.Now()
	sourcePreflightCache.Lock()
	sourcePreflightCache.entries["positive"] = sourcePreflightCacheEntry{}
	sourcePreflightCache.entries["expired-negative"] = sourcePreflightCacheEntry{
		errorMessage: "temporary failure",
		expiresAt:    now.Add(-time.Second),
	}
	sourcePreflightCache.Unlock()

	request := sourcePreflightTestRequest(t)
	conversions := 0
	if err := ValidateSourceWithRunner(context.Background(), request, sourcePreflightTestRunner(&conversions, func() string {
		return "ffmpeg version eviction"
	}, nil)); err != nil {
		t.Fatal(err)
	}

	sourcePreflightCache.Lock()
	_, positivePresent := sourcePreflightCache.entries["positive"]
	_, expiredPresent := sourcePreflightCache.entries["expired-negative"]
	sourcePreflightCache.Unlock()
	if !positivePresent {
		t.Fatal("valid positive entry was evicted")
	}
	if expiredPresent {
		t.Fatal("expired negative entry was retained after a cache write")
	}
}

func TestSourcePreflightCacheBoundsSuccessfulVerdicts(t *testing.T) {
	resetSourcePreflightCache(t)
	now := time.Now()
	for i := range maxSourcePreflightCacheEntries + 5 {
		sourcePreflightCacheStore(strconv.Itoa(i), sourcePreflightCacheEntry{}, now)
	}
	sourcePreflightCache.Lock()
	entryCount := len(sourcePreflightCache.entries)
	_, newestPresent := sourcePreflightCache.entries[strconv.Itoa(maxSourcePreflightCacheEntries+4)]
	sourcePreflightCache.Unlock()
	if entryCount != maxSourcePreflightCacheEntries {
		t.Fatalf("source preflight cache entries = %d, want %d", entryCount, maxSourcePreflightCacheEntries)
	}
	if !newestPresent {
		t.Fatal("source preflight cache evicted the newest successful verdict")
	}
}

// TestSourcePreflightTimeoutCoversAllBoundedCommands verifies the shared deadline covers the command matrix.
func TestSourcePreflightTimeoutCoversAllBoundedCommands(t *testing.T) {
	want := 10*probeCommandTimeout + sourcePreflightTimeoutSlack
	if got := SourcePreflightTimeout(100); got != want {
		t.Fatalf("SourcePreflightTimeout() = %s, want %s", got, want)
	}
	executionWant := 9*probeCommandTimeout + sourcePreflightTimeoutSlack
	if got := sourcePreflightExecutionTimeout(100); got != executionWant {
		t.Fatalf("sourcePreflightExecutionTimeout() = %s, want %s", got, executionWant)
	}
}

// TestFFmpegVersionCacheInvalidatesOnBinaryModification verifies executable changes refresh version facts.
func TestFFmpegVersionCacheInvalidatesOnBinaryModification(t *testing.T) {
	resetSourcePreflightCache(t)
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("one"), 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte("ffmpeg version test"), nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := ffmpegVersionForPreflight(context.Background(), ffmpegPath, runner); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("version calls = %d, want one for an unchanged binary", calls)
	}
	info, err := os.Stat(ffmpegPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(ffmpegPath, changed, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := ffmpegVersionForPreflight(context.Background(), ffmpegPath, runner); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("version calls = %d, want refresh after binary modification", calls)
	}
}

// TestFFmpegVersionSharedLookupSurvivesFirstCallerCancellation verifies one canceled caller cannot abort shared work.
func TestFFmpegVersionSharedLookupSurvivesFirstCallerCancellation(t *testing.T) {
	resetSourcePreflightCache(t)
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("ffmpeg"), 0o700); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	var startedOnce sync.Once
	var sharedCanceled atomic.Bool
	runner := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
			return []byte("ffmpeg version shared"), nil
		case <-ctx.Done():
			sharedCanceled.Store(true)
			return nil, ctx.Err()
		}
	}
	type versionResult struct {
		output []byte
		err    error
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first := make(chan versionResult, 1)
	go func() {
		output, err := ffmpegVersionForPreflight(firstCtx, ffmpegPath, runner)
		first <- versionResult{output: output, err: err}
	}()
	<-started
	second := make(chan versionResult, 1)
	go func() {
		output, err := ffmpegVersionForPreflight(context.Background(), ffmpegPath, runner)
		second <- versionResult{output: output, err: err}
	}()
	cancelFirst()
	select {
	case result := <-first:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("first lookup error = %v, want context canceled", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled version caller did not stop waiting")
	}
	if sharedCanceled.Load() {
		t.Fatal("caller cancellation propagated to the shared version command")
	}
	close(release)
	select {
	case result := <-second:
		if result.err != nil || string(result.output) != "ffmpeg version shared" {
			t.Fatalf("second lookup = %q, %v", result.output, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("remaining version caller did not receive the shared result")
	}
	if _, err := ffmpegVersionForPreflight(context.Background(), ffmpegPath, runner); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("version command calls = %d, want one shared and cached lookup", calls.Load())
	}
}

// TestFFmpegVersionCacheDoesNotStoreEmptyOrFailedLookups verifies unusable version results remain retryable.
func TestFFmpegVersionCacheDoesNotStoreEmptyOrFailedLookups(t *testing.T) {
	tests := []struct {
		name   string
		output []byte
		err    error
	}{
		{name: "empty"},
		{name: "failed", err: errors.New("version unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetSourcePreflightCache(t)
			ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg")
			if err := os.WriteFile(ffmpegPath, []byte("ffmpeg"), 0o700); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			runner := func(context.Context, string, ...string) ([]byte, error) {
				calls.Add(1)
				return tt.output, tt.err
			}
			for attempt := 0; attempt < 2; attempt++ {
				_, _ = ffmpegVersionForPreflight(context.Background(), ffmpegPath, runner)
			}
			if calls.Load() != 2 {
				t.Fatalf("version command calls = %d, want failed lookup retried", calls.Load())
			}
		})
	}
}

// TestSourcePreflightSharedExecutionSurvivesFirstCallerCancellation verifies shared validation outlives one request.
func TestSourcePreflightSharedExecutionSurvivesFirstCallerCancellation(t *testing.T) {
	resetSourcePreflightCache(t)
	request := sourcePreflightTestRequest(t)
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var startedOnce sync.Once
	var sharedCanceled atomic.Bool
	var preflightCommands atomic.Int32
	var conversionCommands atomic.Int32
	runner := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if len(args) == 1 && args[0] == "-version" {
			return []byte("ffmpeg version shared"), nil
		}
		preflightCommands.Add(1)
		startedOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
			sharedCanceled.Store(true)
			return nil, ctx.Err()
		}
		if strings.Contains(name, "ffprobe") {
			if !strings.Contains(joined, "stream=codec_name") {
				return []byte(`{"frames":[{"color_range":"tv","color_space":"bt2020nc","color_transfer":"smpte2084","color_primaries":"bt2020"}]}`), nil
			}
			return []byte(`{"streams":[{"codec_name":"h264","pix_fmt":"yuv420p","color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","side_data_list":[]}],"frames":[{"color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","side_data_list":[]}]}`), nil
		}
		conversionCommands.Add(1)
		return nil, nil
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- ValidateSourceWithRunner(firstCtx, request, runner) }()
	<-started
	second := make(chan error, 1)
	go func() { second <- ValidateSourceWithRunner(context.Background(), request, runner) }()
	cancelFirst()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSourcePreflightUnavailable) {
			t.Fatalf("first preflight error = %v, want unavailable + context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled preflight caller did not stop waiting")
	}
	if sharedCanceled.Load() {
		t.Fatal("caller cancellation propagated to the shared preflight command")
	}
	close(release)
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("remaining preflight caller error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("remaining preflight caller did not receive the shared result")
	}
	commandsBeforeCacheHit := preflightCommands.Load()
	if err := ValidateSourceWithRunner(context.Background(), request, runner); err != nil {
		t.Fatal(err)
	}
	if preflightCommands.Load() != commandsBeforeCacheHit {
		t.Fatalf("successful shared preflight was not cached: commands = %d, want %d", preflightCommands.Load(), commandsBeforeCacheHit)
	}
	if conversionCommands.Load() != 3 {
		t.Fatalf("conversion command calls = %d, want three shared samples", conversionCommands.Load())
	}
}

// TestValidateSourceDoesNotCacheWithoutStableRevision verifies mutable sources are always rechecked.
func TestValidateSourceDoesNotCacheWithoutStableRevision(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SourceRevision)
	}{
		{name: "missing modification time", mutate: func(revision *SourceRevision) { revision.FileModifiedUnixNano = 0 }},
		{name: "missing file hash", mutate: func(revision *SourceRevision) { revision.FileHash = "" }},
		{name: "missing probe revision", mutate: func(revision *SourceRevision) { revision.ProbeUpdatedUnixNano = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetSourcePreflightCache(t)
			request := sourcePreflightTestRequest(t)
			tt.mutate(&request.SourceRevision)
			conversions := 0
			runner := sourcePreflightTestRunner(&conversions, func() string { return "ffmpeg version 1" }, nil)
			for attempt := 0; attempt < 2; attempt++ {
				if err := ValidateSourceWithRunner(context.Background(), request, runner); err != nil {
					t.Fatal(err)
				}
			}
			if conversions != 6 {
				t.Fatalf("conversion calls = %d, want six uncached sample conversions", conversions)
			}
		})
	}
}

// TestValidateSourceChecksEverySampleOutput verifies every representative conversion is inspected.
func TestValidateSourceChecksEverySampleOutput(t *testing.T) {
	resetSourcePreflightCache(t)
	request := sourcePreflightTestRequest(t)
	conversions := 0
	outputInspections := 0
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if len(args) == 1 && args[0] == "-version" {
			return []byte("ffmpeg version 1"), nil
		}
		if strings.Contains(name, "ffprobe") {
			if !strings.Contains(joined, "stream=codec_name") {
				return []byte(`{"frames":[{"color_range":"tv","color_space":"bt2020nc","color_transfer":"smpte2084","color_primaries":"bt2020"}]}`), nil
			}
			outputInspections++
			sideData := "[]"
			if outputInspections == 2 {
				sideData = `[{"side_data_type":"Mastering display metadata"}]`
			}
			return []byte(`{"streams":[{"codec_name":"h264","pix_fmt":"yuv420p","color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","side_data_list":[]}],"frames":[{"color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","side_data_list":` + sideData + `}]}`), nil
		}
		conversions++
		return nil, nil
	}
	if err := ValidateSourceWithRunner(context.Background(), request, runner); err == nil {
		t.Fatal("preflight accepted HDR metadata in a later sample")
	}
	if conversions != 2 || outputInspections != 2 {
		t.Fatalf("conversions=%d output inspections=%d, want two of each", conversions, outputInspections)
	}
}

// TestSourcePreflightPositionsCoverBeginningMiddleAndEnd verifies representative sampling coverage.
func TestSourcePreflightPositionsCoverBeginningMiddleAndEnd(t *testing.T) {
	want := []float64{0, 50, 90}
	got := sourcePreflightPositions(100)
	if !slices.Equal(got, want) {
		t.Fatalf("sourcePreflightPositions() = %v, want %v", got, want)
	}
}

// TestSourceConversionPreflightFilterMapsQSVFramesOnce verifies the QSV interop graph has one mapping step.
func TestSourceConversionPreflightFilterMapsQSVFramesOnce(t *testing.T) {
	filter := sourceConversionPreflightFilter(SourcePreflightRequest{
		Mode: ModeHardware, Backend: BackendQSV, Kind: SourcePQ,
	})
	if got := strings.Count(filter, "hwmap=derive_device=qsv"); got != 1 {
		t.Fatalf("QSV preflight map count = %d, want 1: %s", got, filter)
	}
	if scale, mapping, metadata := strings.Index(filter, "scale_vaapi"), strings.Index(filter, "hwmap=derive_device=qsv"), strings.Index(filter, "sidedata=mode=delete"); scale < 0 || mapping <= scale || metadata <= mapping {
		t.Fatalf("QSV preflight stages are out of order: %s", filter)
	}
}

// TestSourceConversionPreflightUsesSiloQSVDriverSelection verifies QSV initialization matches runtime selection.
func TestSourceConversionPreflightUsesSiloQSVDriverSelection(t *testing.T) {
	args := sourceConversionPreflightArgs(SourcePreflightRequest{
		Mode: ModeHardware, Backend: BackendQSV, Kind: SourcePQ, HardwareDevice: "/dev/dri/renderD129",
	}, 0, "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "vaapi=va:/dev/dri/renderD129,driver=iHD,kernel_driver=i915,vendor_id=0x8086") {
		t.Fatalf("QSV preflight did not mirror runtime driver selection: %s", joined)
	}
}

func TestSourceConversionPreflightMirrorsSoftwareDecodeForVAAPI(t *testing.T) {
	args := sourceConversionPreflightArgs(SourcePreflightRequest{
		Mode: ModeHardware, Backend: BackendVAAPI, Kind: SourcePQ,
		HardwareDevice: "/dev/dri/renderD129", SoftwareVideoDecode: true,
	}, 0, "")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-hwaccel") {
		t.Fatalf("software-decode preflight requested hardware decode: %s", joined)
	}
	if !strings.Contains(joined, "format=nv12,hwupload") {
		t.Fatalf("software-decode preflight did not upload frames before VAAPI tone mapping: %s", joined)
	}
}

// TestSourceConversionPreflightOnlyRequestsSoftwareColorspaceConversion verifies hardware graphs avoid an invalid conversion.
func TestSourceConversionPreflightOnlyRequestsSoftwareColorspaceConversion(t *testing.T) {
	tests := []struct {
		name           string
		mode           Mode
		backend        string
		wantColorspace bool
	}{
		{name: "software", mode: ModeSoftware, backend: BackendSoftware, wantColorspace: true},
		{name: "QSV", mode: ModeHardware, backend: BackendQSV},
		{name: "VAAPI", mode: ModeHardware, backend: BackendVAAPI},
		{name: "NVENC", mode: ModeHardware, backend: BackendNVENC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := sourceConversionPreflightArgs(SourcePreflightRequest{
				Mode: tt.mode, Backend: tt.backend, Kind: SourcePQ,
			}, 0, "")
			if got := slices.Contains(args, "-colorspace"); got != tt.wantColorspace {
				t.Fatalf("preflight -colorspace present = %t, want %t: %s", got, tt.wantColorspace, strings.Join(args, " "))
			}
		})
	}
}

// sourcePreflightTestRequest returns a stable request fixture.
func sourcePreflightTestRequest(t *testing.T) SourcePreflightRequest {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/source.mkv"
	if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := dir + "/ffmpeg"
	if err := os.WriteFile(ffmpegPath, []byte("ffmpeg"), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return SourcePreflightRequest{
		FFmpegPath: ffmpegPath, FFprobePath: "/usr/bin/ffprobe", InputPath: path,
		DurationSeconds: 100, SourceBitDepth: 10, Mode: ModeSoftware, Backend: BackendSoftware,
		Filter: SoftwareFilterHable, Kind: SourcePQ, RecipeVersion: "1", DriverFingerprint: "driver-1",
		SourceRevision: SourceRevision{
			MediaFileID: 1, FileSize: info.Size(), FileModifiedUnixNano: normalizeRevisionTime(info.ModTime()).UnixNano(),
			FileHash: "hash", ProbeUpdatedUnixNano: 1, StreamSignature: "stream",
		},
	}
}

// sourcePreflightTestRunner returns a deterministic injectable command runner.
func sourcePreflightTestRunner(conversions *int, version func() string, conversionErr error) CommandRunner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if len(args) == 1 && args[0] == "-version" {
			return []byte(version()), nil
		}
		if strings.Contains(name, "ffprobe") {
			if !strings.Contains(joined, "stream=codec_name") {
				return []byte(`{"frames":[{"color_range":"tv","color_space":"bt2020nc","color_transfer":"smpte2084","color_primaries":"bt2020"}]}`), nil
			}
			return []byte(`{"streams":[{"codec_name":"h264","pix_fmt":"yuv420p","color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","side_data_list":[]}],"frames":[{"color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","side_data_list":[]}]}`), nil
		}
		*conversions++
		return nil, conversionErr
	}
}

// resetSourcePreflightCache clears shared preflight state between tests.
func resetSourcePreflightCache(t *testing.T) {
	t.Helper()
	sourcePreflightCache.Lock()
	sourcePreflightCache.entries = make(map[string]sourcePreflightCacheEntry)
	sourcePreflightCache.nextAccess = 0
	sourcePreflightCache.Unlock()
	ffmpegVersionCache.Lock()
	ffmpegVersionCache.entries = make(map[string][]byte)
	ffmpegVersionCache.Unlock()
}
