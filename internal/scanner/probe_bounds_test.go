package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeFileRejectsOversizedOutput(t *testing.T) {
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	script := "#!/bin/sh\ndd if=/dev/zero bs=1048576 count=9 2>/dev/null\n"
	if err := os.WriteFile(ffprobe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ProbeFile(context.Background(), ffprobe, "provider-controlled")
	if !errors.Is(err, errFFprobeOutputTooLarge) {
		t.Fatalf("ProbeFile error = %v, want output-size rejection", err)
	}
}

func TestBoundedProbeBufferRejectsOverflow(t *testing.T) {
	buffer := &boundedProbeBuffer{limit: 4}
	if _, err := buffer.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("5")); !errors.Is(err, errFFprobeOutputTooLarge) {
		t.Fatalf("overflow error = %v", err)
	}
}
