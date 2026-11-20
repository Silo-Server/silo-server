package scanner

import (
	"context"

	"path/filepath"

	"testing"
)

// A failed packet fallback must not discard the codec/track metadata that
// already parsed successfully; the file imports with an unknown duration and
// the repair layer retries later.
func TestProbeFileKeepsMetadataWhenPacketFallbackFails(t *testing.T) {
	t.Parallel()

	ffprobePath := filepath.Join(t.TempDir(), "ffprobe")
	script := `#!/bin/sh
case " $* " in
  *" -show_format "*)
    printf '%s\n' '{"format":{"duration":"4298357.248000","size":"1258000000"},"streams":[{"codec_type":"video","codec_name":"h264","avg_frame_rate":"30/1"}]}'
    ;;
  *)
    exit 1
    ;;
esac
`
	writeFakeTool(t, ffprobePath, script)

	probe, err := ProbeFile(context.Background(), ffprobePath, "broken.mkv")
	if err != nil {
		t.Fatalf("ProbeFile() returned error: %v", err)
	}
	if probe.Duration != 0 {
		t.Fatalf("ProbeFile() duration = %d, want 0 (unknown)", probe.Duration)
	}
	if probe.CodecVideo != "h264" {
		t.Fatalf("ProbeFile() codec = %q, want parsed metadata to survive the failed fallback", probe.CodecVideo)
	}
}
