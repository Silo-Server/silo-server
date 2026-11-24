package nodemetrics

import (
	"os"
	"path/filepath"
	"testing"
)

// A systemd unit with MemoryMax= publishes its limit on the slice above the
// leaf while both the leaf and the mount root read "max". The effective limit
// must come from whichever level actually binds, not from the root files
// alone.
func TestEffectiveMemoryLimitBytesFindsTheBindingAncestor(t *testing.T) {
	root := t.TempDir()
	previousRoot := cgroupMountRoot
	cgroupMountRoot = root
	t.Cleanup(func() { cgroupMountRoot = previousRoot })

	leaf := filepath.Join(root, "system.slice", "silo.service")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatalf("create %s: %v", leaf, err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	procDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procDir, "self"), 0o755); err != nil {
		t.Fatalf("create proc self dir: %v", err)
	}
	write(filepath.Join(procDir, "self", "cgroup"), "0::/system.slice/silo.service\n")

	layouts := []cgroupUsagePath{{
		limit:        filepath.Join(root, "memory.max"),
		usage:        filepath.Join(root, "memory.current"),
		stat:         filepath.Join(root, "memory.stat"),
		inactiveFile: cgroupInactiveFileKeyV2,
	}}
	const gib = int64(1) << 30

	write(filepath.Join(root, "memory.max"), "max\n")
	write(filepath.Join(root, "system.slice", "memory.max"), "2147483648\n")
	write(filepath.Join(leaf, "memory.max"), "max\n")
	if got := effectiveMemoryLimitBytes(procDir, layouts); got != 2*gib {
		t.Fatalf("effective limit = %d, want the slice's 2GiB", got)
	}

	// A leaf tighter than its ancestor binds instead.
	write(filepath.Join(leaf, "memory.max"), "1073741824\n")
	if got := effectiveMemoryLimitBytes(procDir, layouts); got != gib {
		t.Fatalf("effective limit = %d, want the leaf's 1GiB", got)
	}

	// No concrete limit anywhere reads as no limit, never as a sentinel.
	write(filepath.Join(leaf, "memory.max"), "max\n")
	write(filepath.Join(root, "system.slice", "memory.max"), "max\n")
	if got := effectiveMemoryLimitBytes(procDir, layouts); got != 0 {
		t.Fatalf("effective limit = %d, want 0 for no limit", got)
	}
}

// The OOM-kill counter comes from this process's own cgroup: the slice above
// it also counts its siblings' kills, and a namespaced container reads its own
// cgroup at the mount root.
func TestCgroupOOMKillsReadsTheProcessCgroup(t *testing.T) {
	root := t.TempDir()
	previousRoot := cgroupMountRoot
	cgroupMountRoot = root
	t.Cleanup(func() { cgroupMountRoot = previousRoot })
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	procDir := t.TempDir()

	write(filepath.Join(procDir, "self", "cgroup"), "0::/system.slice/silo.service\n")
	write(filepath.Join(root, "memory.events"), "oom 7\noom_kill 7\n")
	write(filepath.Join(root, "system.slice", "memory.events"), "oom 9\noom_kill 9\n")
	// The process's cgroup exists but has no memory.events (the memory
	// controller isn't enabled there): no counter, not an ancestor's.
	if err := os.MkdirAll(filepath.Join(root, "system.slice", "silo.service"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := cgroupOOMKills(procDir); got != nil {
		t.Fatalf("cgroupOOMKills = %d with no memory.events in the process's cgroup, want nil", *got)
	}
	write(filepath.Join(root, "system.slice", "silo.service", "memory.events"), "low 0\nhigh 3\nmax 2\noom 1\noom_kill 2\n")
	if got := cgroupOOMKills(procDir); got == nil || *got != 2 {
		t.Fatalf("cgroupOOMKills = %v, want the service's 2", got)
	}

	// A reported path that doesn't exist under the mount (the mount is the
	// container's own cgroup) reads the mount root.
	write(filepath.Join(procDir, "self", "cgroup"), "0::/docker/abc123\n")
	if got := cgroupOOMKills(procDir); got == nil || *got != 7 {
		t.Fatalf("cgroupOOMKills = %v, want the mount root's 7", got)
	}

	write(filepath.Join(procDir, "self", "cgroup"), "0::/\n")
	write(filepath.Join(root, "memory.events"), "oom 4\noom_kill 4\n")
	if got := cgroupOOMKills(procDir); got == nil || *got != 4 {
		t.Fatalf("cgroupOOMKills = %v, want the namespaced root's 4", got)
	}
}
