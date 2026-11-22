package scanner

import (
	"context"
	"errors"

	"os"

	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/themesongs"
)

func TestThemeAncestorRulesRejectsPathsOutsideLibrary(t *testing.T) {
	root := t.TempDir()
	d := themeDiscovery{
		roots:     []string{root},
		ancestors: make(map[themeIgnoreKey]themeIgnoreState),
		readDir: func(path string) ([]os.DirEntry, error) {
			t.Fatalf("read outside library root: %s", path)
			return nil, nil
		},
	}
	for _, directory := range []string{"/", filepath.Dir(root), root + "-other"} {
		if state := d.ancestorRules(directory, root); !errors.Is(state.err, themesongs.ErrUnavailable) {
			t.Errorf("directory=%q: err=%v, want unavailable", directory, state.err)
		}
	}
}

func TestThemeDiscoveryUnreadableAndCancelledAreNotEmptySnapshots(t *testing.T) {
	root := t.TempDir()
	if _, err := discoverThemeSongs(context.Background(), filepath.Join(root, "missing"), []string{root}, "ffprobe", nil); err == nil {
		t.Fatal("missing directory treated as an empty snapshot")
	}
	if err := os.WriteFile(filepath.Join(root, "theme.mp3"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := discoverThemeSongs(ctx, root, []string{root}, "ffprobe", nil); err == nil {
		t.Fatal("canceled scan treated as authoritative")
	}
}
