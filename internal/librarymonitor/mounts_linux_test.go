//go:build linux

package librarymonitor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loopDisk is an ext4 image loop-mounted at dir for the test.
type loopDisk struct {
	image, dir    string
	mount, umount string
}

// newLoopDisk formats image and mounts it at dir; the mount is removed when
// the test ends. It skips unless the test runs as root with the tools.
func newLoopDisk(t *testing.T, image, dir string) *loopDisk {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("loop mounts need root")
	}
	tools := make(map[string]string)
	for _, name := range []string{"mkfs.ext4", "mount", "umount"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s is not installed", name)
		}
		tools[name] = path
	}
	if err := os.WriteFile(image, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(image, 16<<20); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(tools["mkfs.ext4"], "-q", "-F", image).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v: %s", err, out)
	}
	d := &loopDisk{image: image, dir: dir, mount: tools["mount"], umount: tools["umount"]}
	d.attach(t)
	t.Cleanup(func() { _ = exec.Command(d.umount, d.dir).Run() })
	return d
}

func (d *loopDisk) attach(t *testing.T) {
	t.Helper()
	if out, err := exec.Command(d.mount, "-o", "loop", d.image, d.dir).CombinedOutput(); err != nil {
		t.Fatalf("mount: %v: %s", err, out)
	}
}

// remount unmounts and mounts the image again at once, as `umount && mount`
// does: the new mount has the same device, fsid, root inode, and usually
// the same mount ID, but a new superblock.
func (d *loopDisk) remount(t *testing.T) {
	t.Helper()
	if out, err := exec.Command(d.umount, d.dir).CombinedOutput(); err != nil {
		t.Fatalf("umount: %v: %s", err, out)
	}
	d.attach(t)
}

// hasMount reports whether a recorded mount signature includes a mount at
// point.
func hasMount(signature, point string) bool {
	for _, line := range strings.Split(signature, "\n") {
		if strings.HasSuffix(line, " "+point) {
			return true
		}
	}
	return false
}

// TestRemountIsRecordedAgain unmounts and mounts a filesystem again at once,
// either the library folder itself or a disk mounted inside it, and checks
// that changes on the new mount are still seen. The kernel sends inotify
// IN_UNMOUNT but nothing when the filesystem comes back. It runs only as
// root.
func TestRemountIsRecordedAgain(t *testing.T) {
	backends := []integrationBackend{
		{name: BackendInotify, configure: func(*testing.T, *Config) {}},
	}
	for _, backend := range backends {
		for _, nested := range []bool{false, true} {
			name := backend.name + "/library folder remounted"
			if nested {
				name = backend.name + "/disk inside the library remounted"
			}
			t.Run(name, func(t *testing.T) {
				parent := t.TempDir()
				root := filepath.Join(parent, "library")
				mkdirs(t, parent, "library")
				mountDir := root
				if nested {
					mountDir = filepath.Join(root, "disk")
					mkdirs(t, root, "disk")
				}
				disk := newLoopDisk(t, filepath.Join(parent, "disk.img"), mountDir)
				mkdirs(t, mountDir, "Movie")
				movie := fmt.Sprintf("1 subtree %s %s", filepath.Join(mountDir, "Movie"), Trigger)

				folders := &fakeFolders{}
				folders.set(library(1, root))
				queue, status := newFakeQueue(), newFakeStatus()
				walks := make(chan string, 64)
				cfg := testConfig(folders, queue, status)
				cfg.hooks.afterWalk = func(_, mounts string) { walks <- mounts }
				backend.configure(t, &cfg)
				m := startMonitor(t, cfg)
				rows := waitStatus(t, status, "monitoring", hasState(1, StateMonitoring))
				if rows[0].Backend != backend.name {
					t.Fatalf("backend = %q, want %q", rows[0].Backend, backend.name)
				}
				writeFile(t, filepath.Join(mountDir, "Movie", "a.mkv"), "data")
				waitTargets(t, queue, movie)
				drain(walks)

				disk.remount(t)
				// Reconcile runs every 30 seconds in production; nudge it
				// until a walk has recorded the filesystem mounted again.
				nudge := time.NewTicker(50 * time.Millisecond)
				defer nudge.Stop()
				deadline := time.After(waitTimeout)
				for recorded := false; !recorded; {
					select {
					case mounts := <-walks:
						recorded = hasMount(mounts, mountDir)
					case <-nudge.C:
						m.Poke()
					case <-deadline:
						t.Fatal("no walk recorded the remounted filesystem")
					}
				}
				writeFile(t, filepath.Join(mountDir, "Movie", "b.mkv"), "data")
				waitTargets(t, queue, movie)
			})
		}
	}
}
