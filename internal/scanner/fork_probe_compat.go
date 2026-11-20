package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// Fork-compatible probe helpers. The fork's repair and audiobook code still
// calls these; upstream replaced the call sites with its own probe paths.
// Keep them thin and route through the upstream probe core.

// toolCommandContext runs an external media tool in its own process group so a
// deadline or a stop kills the wrapper's children too, decoding an fspath-
// encoded binary path and arguments for non-UTF-8 file names.
func toolCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return fspath.CommandContext(ctx, fspath.Decode(name), args...)
}

type boundedProbeBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

const maxFFprobeOutputBytes = 8 << 20

var errFFprobeOutputTooLarge = fmt.Errorf("ffprobe output exceeds %d bytes", maxFFprobeOutputBytes)

func (b *boundedProbeBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.overflow = true
		remaining := b.limit - b.buffer.Len()
		if remaining > 0 {
			_, _ = b.buffer.Write(p[:remaining])
		}
		return len(p), errFFprobeOutputTooLarge
	}
	return b.buffer.Write(p)
}

func (b *boundedProbeBuffer) Bytes() []byte { return b.buffer.Bytes() }

// The fast preprobe trades the analysis window for speed: the identity fields
// and a complete stream table are usually already in the file's header, and a
// result without them escalates to the full probe (see virtualFastProbeUsable),
// so the reduction never changes a verdict.
const (
	fastProbeSizeLimit       = "32"
	fastProbeAnalyzeDuration = "0"
	fastProbeFlags           = "+discardcorrupt+fastseek+nobuffer"
	// fastProbeFFlagsOption names the fflags input option so the flag
	// stays a single literal across the builder and its tests.
	fastProbeFFlagsOption = "-fflags"
)

// buildFastProbeArgs returns the version-candidate preprobe argv. It is split
// out so tests can assert the fast flags without executing a process.
func buildFastProbeArgs(filePath string) []string {
	return []string{
		"-v", ffprobeErrorLevel,
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-show_chapters",
		probeSizeFlag, fastProbeSizeLimit,
		probeAnalyzeDurationFlag, fastProbeAnalyzeDuration,
		fastProbeFFlagsOption, fastProbeFlags,
		filePath,
	}
}

// probeFileFast runs the version-candidate preprobe. It stops before a packet
// scan and before the first-frame read; a result that would need either is the
// caller's signal to escalate to the full probe.
func probeFileFast(ctx context.Context, ffprobePath string, filePath string) (*ProbeData, error) {
	return probeFileFastWithin(ctx, ffprobePath, filePath, defaultProbeLimits)
}

func probeFileFastWithin(ctx context.Context, ffprobePath string, filePath string, limits probeLimits) (*ProbeData, error) {
	probeCtx, cancel := context.WithTimeout(ctx, limits.probe)
	defer cancel()
	output, err := runFFprobeBounded(probeCtx, ctx, ffprobePath, filePath, buildFastProbeArgs(filePath), limits)
	if err != nil {
		return nil, err
	}
	var raw ffprobeOutput
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe JSON parse failed for %s: %w", filePath, err)
	}
	if len(raw.Streams) > maxFFprobeStreams {
		return nil, fmt.Errorf("ffprobe returned %d streams (maximum %d)", len(raw.Streams), maxFFprobeStreams)
	}
	probe := convertProbeData(&raw)
	applyMatroskaSubtitleTracks(ctx, filePath, probe)
	return probe, nil
}

// impliedBitrateBps is the bitrate a file's size and duration imply. Callers
// use it as a duration-sanity signal, not as a real bitrate estimate.
func impliedBitrateBps(sizeBytes int64, durationSeconds float64) float64 {
	return float64(sizeBytes) * 8 / durationSeconds
}
