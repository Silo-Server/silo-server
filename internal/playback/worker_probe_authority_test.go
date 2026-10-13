package playback

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerinput"
)

func TestDVRPUCacheRequiresCurrentSourceAuthority(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	adapter, err := workerinput.New(workerinput.Options{Origins: []string{"http://core.invalid"}, AllowHTTP: true, Allowed: func(string, uint64) bool { return active.Load() }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	source := workerinput.Source{ID: "media", Revision: "revision", URL: "http://core.invalid/source", Grant: "opaque", ETag: `"revision"`, Size: 10, WorkID: "work", Generation: 1}
	first, err := adapter.Open(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := adapter.Open(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffmpeg")
	runs := filepath.Join(dir, "runs")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho run >> '"+runs+"'\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	probe := NewDVRPUProbe()
	ctx := workerinput.WithContext(context.Background(), adapter)
	if !probe.CanStrip(ctx, bin, first.URL()) || !probe.CanStrip(ctx, bin, second.URL()) {
		t.Fatal("admitted clean probe was refused")
	}
	data, err := os.ReadFile(runs)
	if err != nil || strings.Count(string(data), "run") != 1 {
		t.Fatalf("renewed URL did not reuse immutable source identity: runs=%q err=%v", data, err)
	}
	active.Store(false)
	if probe.CanStrip(ctx, bin, second.URL()) {
		t.Fatal("revoked source inherited the warm verdict")
	}
	if probe.CanStrip(ctx, bin, "http://core.invalid/unregistered") {
		t.Fatal("unregistered URL failed open")
	}
	data, _ = os.ReadFile(runs)
	if strings.Count(string(data), "run") != 1 {
		t.Fatal("refused source started another subprocess")
	}
}
