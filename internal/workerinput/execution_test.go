package workerinput

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnregisteredPositionalProbeCannotStart(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "started")
	probe := filepath.Join(root, "ffprobe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{LocalRoot: root, Origins: []string{"https://core.example"}, Allowed: func(string, uint64) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	ctx := WithContext(context.Background(), a)
	for _, input := range []string{"https://unapproved.example/media", "/etc/passwd", "concat:/etc/passwd"} {
		if err := CommandContext(ctx, probe, "-v", "error", "-of", "json", input).Run(); err == nil {
			t.Errorf("probe accepted %q", input)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("refused positional input started a subprocess")
	}
}
