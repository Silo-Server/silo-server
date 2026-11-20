package scanner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/procgroup"
)

// Fork-compatible probe helpers. The fork's repair and audiobook code still
// calls these; upstream replaced the call sites with its own probe paths.
// Keep them thin and route through the upstream probe core.

// toolCommandContext runs an external media tool in its own process group so a
// deadline or a stop kills the wrapper's children too, decoding an fspath-
// encoded binary path and arguments for non-UTF-8 file names.
func toolCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return procgroup.CommandContext(ctx, fspath.Decode(name), fspath.DecodeAll(args)...)
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

// probeFileFast runs the version-candidate preprobe. It stops before a packet
// scan and before the first-frame read; a result that would need either is the
// caller's signal to escalate to the full probe.
func probeFileFast(ctx context.Context, ffprobePath string, filePath string) (*ProbeData, error) {
	return probeFileStreamsOnly(ctx, ffprobePath, filePath)
}

// impliedBitrateBps is the bitrate a file's size and duration imply. Callers
// use it as a duration-sanity signal, not as a real bitrate estimate.
func impliedBitrateBps(sizeBytes int64, durationSeconds float64) float64 {
	return float64(sizeBytes) * 8 / durationSeconds
}
