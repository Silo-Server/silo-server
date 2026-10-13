package playback

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/executionlease"
	"github.com/Silo-Server/silo-server/internal/workerinput"
)

func TestWorkerCopyFragmentProbeUsesConfiguredProbeAndRetainsAdmission(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "configured-probe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '2.5\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"init.mp4", "seg_00000.m4s"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := workerinput.New(workerinput.Options{LocalRoot: dir, FFprobe: probe, Origins: []string{"https://core.example"}, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	retains, releases := 0, 0
	ctx := executionlease.WithRetainer(workerinput.WithContext(t.Context(), adapter), func() (func(), error) {
		retains++
		return func() { releases++ }, nil
	})
	opts := TranscodeOpts{FFmpegPath: filepath.Join(dir, "missing", "ffmpeg")}
	origin, err := probeCopyFragmentOrigin(ctx, opts, dir, 0)
	if err != nil || origin != 2.5 || retains != 1 || releases != 1 {
		t.Fatalf("probe origin=%g error=%v admission=%d/%d", origin, err, retains, releases)
	}
	outside := t.TempDir()
	for _, name := range []string{"init.mp4", "seg_00000.m4s"} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := probeCopyFragmentOrigin(ctx, opts, outside, 0); err == nil {
		t.Fatal("copy probe admitted output outside worker scratch")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := probeCopyFragmentOrigin(canceled, opts, dir, 0); err == nil {
		t.Fatal("copy probe survived cancellation")
	}
}
