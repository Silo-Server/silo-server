package playback

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
	"golang.org/x/sync/singleflight"
)

const (
	hardwareEncoderWarmupTimeout     = 3 * time.Second
	hardwareEncoderWarmupNegativeTTL = 15 * time.Second
	defaultRenderDevicePath          = "/dev/dri/renderD128"
	ffmpegHideBannerArg              = "-hide_banner"
	ffmpegLogLevelArg                = "-loglevel"
	ffmpegErrorLogLevel              = "error"
)

type hardwareEncoderWarmupCacheEntry struct {
	err       error
	expiresAt time.Time
}

type hardwareEncoderWarmupState struct {
	sync.Mutex
	entries map[string]hardwareEncoderWarmupCacheEntry
	group   singleflight.Group
}

func newHardwareEncoderWarmupState() *hardwareEncoderWarmupState {
	return &hardwareEncoderWarmupState{entries: make(map[string]hardwareEncoderWarmupCacheEntry)}
}

var hardwareEncoderWarmupCache = newHardwareEncoderWarmupState()

type hardwareEncoderWarmupRunner func(context.Context, string, ...string) ([]byte, error)

// WarmHardwareEncoder performs one bounded, best-effort single-frame encode on
// each configured hardware device. It primes driver and encoder state behind
// process startup so the first viewer does not pay that one-time cost.
func WarmHardwareEncoder(ctx context.Context, ffmpegPath, configuredHWAccel, configuredHWDevice string) error {
	return warmHardwareEncoderCached(ctx, ffmpegPath, configuredHWAccel, configuredHWDevice,
		hardwareEncoderWarmupCache, ResolveHWAccelWithFFmpegContext, runHardwareEncoderWarmup)
}

func warmHardwareEncoderCached(
	ctx context.Context,
	ffmpegPath, configuredHWAccel, configuredHWDevice string,
	state *hardwareEncoderWarmupState,
	resolve func(ctx context.Context, hwAccel, ffmpegPath, hwDevice string) string,
	run hardwareEncoderWarmupRunner,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if state == nil || resolve == nil || run == nil {
		return nil
	}
	ffmpegPath = ResolveFFmpegPath(ffmpegPath)
	backend := resolve(ctx, configuredHWAccel, ffmpegPath, configuredHWDevice)
	if backend == "" || backend == HWAccelNone {
		return nil
	}
	devices := hardwareEncoderWarmupDevices(backend, configuredHWDevice)
	cacheKey := ffmpegIdentityKey(ffmpegPath) + "\x00" + backend + "\x00" + strings.Join(devices, ",")
	now := time.Now()
	state.Lock()
	entry, ok := state.entries[cacheKey]
	state.Unlock()
	if ok && (entry.err == nil || now.Before(entry.expiresAt)) {
		return entry.err
	}

	value, err, _ := state.group.Do(cacheKey, func() (any, error) {
		state.Lock()
		cached, found := state.entries[cacheKey]
		state.Unlock()
		if found && (cached.err == nil || time.Now().Before(cached.expiresAt)) {
			return nil, cached.err
		}
		// Once a caller starts the shared flight, its disconnect or shutdown race
		// must not poison the negative cache for other viewers. Each command keeps
		// its own short deadline below.
		warmErr := warmHardwareEncoderWithRunner(context.WithoutCancel(ctx), hardwareEncoderWarmupTimeout, ffmpegPath, backend, devices, run)
		fresh := hardwareEncoderWarmupCacheEntry{err: warmErr}
		if warmErr != nil {
			fresh.expiresAt = time.Now().Add(hardwareEncoderWarmupNegativeTTL)
		}
		state.Lock()
		state.entries[cacheKey] = fresh
		state.Unlock()
		return nil, warmErr
	})
	_ = value
	return err
}

func warmHardwareEncoderWithRunner(ctx context.Context, timeout time.Duration, ffmpegPath, backend string, devices []string, run hardwareEncoderWarmupRunner) error {
	if backend == "" || backend == HWAccelNone || run == nil {
		return nil
	}
	if len(devices) == 0 {
		devices = []string{""}
	}
	var result error
	for _, device := range devices {
		args := hardwareSmokeEncodeArgs(backend, device)
		if len(args) == 0 {
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		output, err := run(attemptCtx, ffmpegPath, args...)
		cancel()
		if err != nil {
			result = errors.Join(result, fmt.Errorf("warm %s encoder on %q: %s", backend, device, FormatFFmpegProbeFailure(err, output)))
		}
	}
	return result
}

func runHardwareEncoderWarmup(ctx context.Context, ffmpegPath string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
}

func hardwareEncoderWarmupDevices(backend, configured string) []string {
	if configuredDevices := ParseHWDeviceSet(configured).List(); len(configuredDevices) > 0 {
		if backend == transcodeHWNVENC {
			return []string{configuredDevices[0]}
		}
		return append([]string(nil), configuredDevices...)
	}
	if backend == transcodeHWNVENC {
		return []string{""}
	}
	renderDevices := listRenderDevices(defaultDRIDir)
	if backend == transcodeHWQSV {
		intelDevices := renderDevices[:0]
		for _, device := range renderDevices {
			if isIntelDevice(device) {
				intelDevices = append(intelDevices, device)
			}
		}
		if len(intelDevices) > 0 {
			return intelDevices
		}
	}
	if len(renderDevices) > 0 {
		return renderDevices
	}
	return []string{defaultRenderDevicePath}
}

// hardwareSmokeEncodeArgs builds the one-frame synthetic encode shared by
// encoder warmup and the auto-detection capability probes: the same
// initialization chain a real transcode uses, driven by testsrc2 so no media
// file is required.
func hardwareSmokeEncodeArgs(backend, device string) []string {
	base := []string{ffmpegHideBannerArg, ffmpegLogLevelArg, ffmpegErrorLogLevel}
	switch backend {
	case transcodeHWQSV:
		if strings.TrimSpace(device) == "" {
			device = defaultRenderDevicePath
		}
		base = append(base, tonemap.QSVInitDeviceArgs(device)...)
		base = append(base, "-filter_hw_device", "qs")
	case transcodeHWVAAPI:
		if strings.TrimSpace(device) == "" {
			device = defaultRenderDevicePath
		}
		base = append(base, tonemap.VAAPIInitDeviceArgs(vaapiHWDeviceAlias, device)...)
		base = append(base, "-filter_hw_device", vaapiHWDeviceAlias)
	case transcodeHWNVENC:
		if strings.TrimSpace(device) != "" {
			base = append(base, "-init_hw_device", "cuda=cu:"+device, "-filter_hw_device", "cu")
		}
	default:
		return nil
	}
	base = append(base, "-f", "lavfi", "-i", smokeEncodeSource)
	switch backend {
	case transcodeHWQSV:
		base = append(base, "-vf", "format=nv12,hwupload=extra_hw_frames=64", "-frames:v", "1", "-an", "-c:v", "h264_qsv")
	case transcodeHWVAAPI:
		base = append(base, "-vf", "format=nv12,hwupload", "-frames:v", "1", "-an", "-c:v", "h264_vaapi")
	case transcodeHWNVENC:
		if strings.TrimSpace(device) != "" {
			base = append(base, "-vf", "hwupload_cuda")
		}
		base = append(base, "-frames:v", "1", "-an", "-c:v", "h264_nvenc")
	}
	return append(base, "-f", "null", "-")
}
