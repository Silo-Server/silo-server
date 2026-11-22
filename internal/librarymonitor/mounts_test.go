package librarymonitor

import (
	"context"
	"os"
	"path/filepath"

	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

func TestWalkTreeSkipsNetworkMountsBelowTheRoot(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Movie", "nas/Movie")
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	walkMounts.mu.Lock()
	walkMounts.skips = map[string]bool{filepath.Join(physical, "nas"): true}
	walkMounts.at = time.Now().Add(time.Hour)
	walkMounts.mu.Unlock()
	t.Cleanup(func() {
		walkMounts.mu.Lock()
		walkMounts.skips = nil
		walkMounts.mu.Unlock()
	})

	var entered []string
	err = walkTree(context.Background(), root, walkVisitor{enter: func(dir string, _ bool) (bool, error) {
		entered = append(entered, rel(t, root, dir))
		return true, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".", "Movie"}; !slices.Equal(entered, want) {
		t.Fatalf("walk entered %v, want %v", entered, want)
	}
}

func TestWalkTreeDoesNotFollowSymlinksOntoNetworkMounts(t *testing.T) {
	root := t.TempDir()
	share := t.TempDir()
	mkdirs(t, root, "Local")
	mkdirs(t, share, "Movie", "Movie/Extras")
	if err := os.Symlink(filepath.Join(share, "Movie"), filepath.Join(root, "Linked Movie")); err != nil {
		t.Fatal(err)
	}
	physicalShare, err := filepath.EvalSymlinks(share)
	if err != nil {
		t.Fatal(err)
	}
	walkMounts.mu.Lock()
	walkMounts.skips = map[string]bool{share: true, physicalShare: true}
	walkMounts.at = time.Now().Add(time.Hour)
	walkMounts.mu.Unlock()
	t.Cleanup(func() {
		walkMounts.mu.Lock()
		walkMounts.skips = nil
		walkMounts.mu.Unlock()
	})

	ctx, skipped := withSkippedPaths(context.Background())
	var entered []string
	err = walkTree(ctx, root, walkVisitor{enter: func(dir string, _ bool) (bool, error) {
		entered = append(entered, rel(t, root, dir))
		return true, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".", "Local"}; !slices.Equal(entered, want) {
		t.Fatalf("walk entered %v, want %v", entered, want)
	}
	if got, want := firstList(skipped.lists()), []string{filepath.Join(root, "Linked Movie")}; !slices.Equal(got, want) {
		t.Fatalf("reported network links %v, want %v", got, want)
	}
}

// A link whose own name is not UTF-8 reaches the check fspath-encoded; its
// text must still be read without touching the target.
func TestLinkOntoUnsupportedMountReadsNonUTF8LinkName(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "Linked B\xe5rd")
	if err := os.Symlink("/srv/nas/Movie", link); err != nil {
		t.Skipf("file system rejects non-UTF-8 names: %v", err)
	}
	mounts := map[string]bool{"/": false, "/srv/nas": true}
	if !linkOntoUnsupportedMount(mounts, fspath.Encode(link), root) {
		t.Fatal("link onto a network mount was not detected for an encoded link name")
	}
}

func TestOnUnsupportedMountUsesTheMountThatHoldsThePath(t *testing.T) {
	mounts := map[string]bool{"/": false, "/srv/nas": true, "/srv/nas/disk": false}
	cases := map[string]bool{
		"/srv/nas":               true,
		"/srv/nas/Movies/A":      true,
		"/srv/nas/disk":          false,
		"/srv/nas/disk/Movies/A": false,
		"/srv/nasty":             false,
		"/media/Movies":          false,
	}
	for path, want := range cases {
		if got := onUnsupportedMount(mounts, path); got != want {
			t.Errorf("onUnsupportedMount(%q) = %v, want %v", path, got, want)
		}
	}
}

// setWalkMounts pins the walk's mount table for a test.
func setWalkMounts(t *testing.T, mounts map[string]bool) {
	t.Helper()
	pinned := make(map[string]bool, len(mounts)*2)
	for point, skip := range mounts {
		pinned[point] = skip
		if physical, err := filepath.EvalSymlinks(point); err == nil {
			pinned[physical] = skip
		}
	}
	walkMounts.mu.Lock()
	walkMounts.skips = pinned
	walkMounts.at = time.Now().Add(time.Hour)
	walkMounts.mu.Unlock()
	t.Cleanup(func() {
		walkMounts.mu.Lock()
		walkMounts.skips = nil
		walkMounts.mu.Unlock()
	})
}

func firstList(first, _ []string) []string { return first }
