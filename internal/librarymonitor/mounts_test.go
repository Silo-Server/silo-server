package librarymonitor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseMountInfo(t *testing.T) {
	data := `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
35 22 0:44 / /mnt/media rw shared:20 master:3 - xfs /dev/sdb1 rw
36 35 0:52 /export /mnt/media/nas\040share rw - nfs4 server:/export rw,vers=4.2
bad line
37 22 0:53 / /mnt/no-separator rw ext4 /dev/sdc1 rw
`
	got := parseMountInfo(data)
	want := []mountEntry{
		{id: 22, dev: "8:1", point: "/", fsType: "ext4"},
		{id: 35, dev: "0:44", point: "/mnt/media", fsType: "xfs"},
		{id: 36, dev: "0:52", point: "/mnt/media/nas share", fsType: mountTypeNFS4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMountInfo = %+v, want %+v", got, want)
	}
}

func TestViewMounts(t *testing.T) {
	rootfs := mountEntry{id: 1, dev: "8:1", point: "/", fsType: "ext4"}
	media := mountEntry{id: 2, dev: "8:17", point: "/mnt/media", fsType: "xfs"}
	disk := mountEntry{id: 3, dev: "8:33", point: "/mnt/media/Movies/disk1", fsType: "ext4"}
	nas := mountEntry{id: 4, dev: "0:50", point: "/mnt/media/Movies/nas", fsType: mountTypeNFS4}
	other := mountEntry{id: 5, dev: "8:49", point: "/mnt/media/Shows", fsType: "ext4"}
	sibling := mountEntry{id: 6, dev: "8:65", point: "/mnt/media/Movies2", fsType: "ext4"}
	table := []mountEntry{rootfs, media, disk, nas, other, sibling}

	view := viewMounts("/library/Movies", "/mnt/media/Movies", table)
	// The covering mount and the mounts inside; not a sibling that shares
	// the prefix, and not another folder's mount.
	wantSig := joinSorted(disk.key(), media.key(), nas.key())
	if view.signature != wantSig {
		t.Fatalf("signature = %q, want %q", view.signature, wantSig)
	}
	if want := []string{"/library/Movies/nas (NFS)"}; !slices.Equal(view.unsupported, want) {
		t.Fatalf("unsupported = %v, want %v (logical paths)", view.unsupported, want)
	}

	// A root that is itself a mount point is covered by that mount alone.
	if got := viewMounts("/mnt/media", "/mnt/media", []mountEntry{rootfs, media}).signature; got != media.key() {
		t.Fatalf("signature of a mounted root = %q", got)
	}
	// A later mount over the same point covers the earlier one.
	over := mountEntry{id: 7, dev: "8:81", point: "/mnt/media", fsType: "ext4"}
	if got := viewMounts("/mnt/media/x", "/mnt/media/x", []mountEntry{rootfs, media, over}).signature; got != over.key() {
		t.Fatalf("signature under an over-mount = %q, want the top mount", got)
	}
}

func joinSorted(keys ...string) string {
	slices.Sort(keys)
	return strings.Join(keys, "\n")
}

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
