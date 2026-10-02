package nodemetrics

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeSelfCgroup lays down a <procDir>/self/cgroup with the given body.
func writeSelfCgroup(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "self"), 0o755); err != nil {
		t.Fatalf("create self dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "self", "cgroup"), []byte(body), 0o644); err != nil {
		t.Fatalf("write self/cgroup: %v", err)
	}
	return dir
}

func TestCgroupRelativePaths(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]string
	}{
		{
			// A systemd unit with CPUQuota= or MemoryMax=. Its limits live
			// below the mount root, so the root files describe the machine.
			name: "systemd unit on cgroup v2",
			body: "0::/system.slice/silo.service\n",
			want: map[string]string{"": "system.slice/silo.service"},
		},
		{
			// v1 names its controllers, and the mount directory is named with
			// the whole comma-joined field, so both forms have to resolve.
			name: "cgroup v1 controllers",
			body: "9:cpu,cpuacct:/system.slice/silo.service\n5:memory:/system.slice/silo.service\n",
			want: map[string]string{
				"cpu,cpuacct": "system.slice/silo.service",
				"cpu":         "system.slice/silo.service",
				"cpuacct":     "system.slice/silo.service",
				"memory":      "system.slice/silo.service",
			},
		},
		{
			// A namespaced container is already at its own root, which is
			// exactly what the unrewritten paths read.
			name: "namespaced container",
			body: "0::/\n",
			want: nil,
		},
		{
			name: "unreadable lines are skipped",
			body: "garbage\n0::/system.slice/silo.service\n",
			want: map[string]string{"": "system.slice/silo.service"},
		},
		{
			// The path field may contain colons; only the first two separators
			// are structural.
			name: "path containing a colon",
			body: "0::/system.slice/silo:one.service\n",
			want: map[string]string{"": "system.slice/silo:one.service"},
		},
		{name: "empty file", body: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cgroupRelativePaths(writeSelfCgroup(t, tt.body))
			if len(got) != len(tt.want) {
				t.Fatalf("cgroupRelativePaths() = %v, want %v", got, tt.want)
			}
			for key, value := range tt.want {
				if got[key] != value {
					t.Fatalf("cgroupRelativePaths()[%q] = %q, want %q", key, got[key], value)
				}
			}
		})
	}
}

// A missing file is the ordinary case on a host that is not Linux, or one whose
// /proc is not where we looked. It must read as "no rewrite", never as an error
// that costs the root reading too.
func TestCgroupRelativePathsWithoutTheFile(t *testing.T) {
	if got := cgroupRelativePaths(t.TempDir()); got != nil {
		t.Fatalf("cgroupRelativePaths() = %v, want none when /self/cgroup is absent", got)
	}
}

func TestCgroupSelfFile(t *testing.T) {
	v2 := map[string]string{"": "system.slice/silo.service"}
	v1 := map[string]string{"cpu,cpuacct": "system.slice/silo.service", "memory": "system.slice/silo.service"}

	tests := []struct {
		name     string
		relative map[string]string
		file     string
		want     string
	}{
		{
			name: "v2 file sits at the mount root", relative: v2,
			file: cgroupCPUPaths[0].quota,
			want: "/sys/fs/cgroup/system.slice/silo.service/cpu.max",
		},
		{
			name: "v1 file sits under its controller", relative: v1,
			file: "/sys/fs/cgroup/cpu,cpuacct/cpuacct.usage",
			want: "/sys/fs/cgroup/cpu,cpuacct/system.slice/silo.service/cpuacct.usage",
		},
		{
			// v1 memory under v2 membership: no unified entry names it, so the
			// root path stands rather than being rewritten into nonsense.
			name: "controller this process has no membership for", relative: v2,
			file: "/sys/fs/cgroup/memory/memory.stat",
			want: "",
		},
		{name: "no membership at all", relative: nil, file: cgroupCPUPaths[0].quota, want: ""},
		{name: "path outside the cgroup mount", relative: v2, file: "/proc/stat", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cgroupSelfFile(tt.relative, tt.file); got != tt.want {
				t.Fatalf("cgroupSelfFile() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The rewritten path goes first and the root stays behind it, so a container
// whose /proc names a host path it cannot open still falls through to the read
// that has always worked.
func TestWithCgroupSelfPathsKeepsTheRootFallback(t *testing.T) {
	relative := map[string]string{"": "system.slice/silo.service", "memory": "system.slice/silo.service"}
	got := withCgroupSelfPaths(relative, CgroupMemoryLimitPaths())
	// Every cgroup between this process and the root, then the root path the
	// list started with. The intermediate levels are the point: a leaf that says
	// "max" inside a slice that does not is exactly the case being covered.
	want := []string{
		"/sys/fs/cgroup/system.slice/silo.service/memory.max",
		"/sys/fs/cgroup/system.slice/memory.max",
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/system.slice/silo.service/memory.limit_in_bytes",
		"/sys/fs/cgroup/memory/system.slice/memory.limit_in_bytes",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
		"/sys/fs/cgroup/memory.limit_in_bytes",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("withCgroupSelfPaths() =\n%v\nwant\n%v", got, want)
	}

	// With no membership the list is exactly what it was.
	if got := withCgroupSelfPaths(nil, CgroupMemoryLimitPaths()); !slices.Equal(got, CgroupMemoryLimitPaths()) {
		t.Fatalf("withCgroupSelfPaths(nil) = %v, want the original list", got)
	}
}

// A CPU layout names several files that have to move together: this process's
// usage normalized against the root's quota would divide the service's own CPU
// time by the whole machine's budget.
func TestWithCgroupSelfCPUPathsRewritesEveryFileOrNone(t *testing.T) {
	relative := map[string]string{"": "system.slice/silo.service"}
	got := withCgroupSelfCPUPaths(relative, cgroupCPUPaths)

	if len(got) != len(cgroupCPUPaths)+1 {
		t.Fatalf("got %d layouts, want only the v2 one rewritten alongside the originals", len(got))
	}
	own := got[0]
	if own.usage != "/sys/fs/cgroup/system.slice/silo.service/cpu.stat" {
		t.Fatalf("usage = %q, want this process's own cpu.stat", own.usage)
	}
	if own.quota != "/sys/fs/cgroup/system.slice/silo.service/cpu.max" {
		t.Fatalf("quota = %q, want this process's own cpu.max", own.quota)
	}
	if got[1].usage != cgroupCPUPaths[0].usage {
		t.Fatalf("got[1].usage = %q, want the root layout kept behind it", got[1].usage)
	}
	// The v1 layouts have no unified membership to resolve, so they are carried
	// through unrewritten rather than half-rewritten.
	for _, layout := range got[1:] {
		if layout.usage != "" && layout.quota == "" {
			t.Fatalf("layout %+v has a usage file with no quota file", layout)
		}
	}
}

func TestWithCgroupSelfUsagePathsRewritesEveryFileOrNone(t *testing.T) {
	relative := map[string]string{"memory": "system.slice/silo.service"}
	got := withCgroupSelfUsagePaths(relative, cgroupMemoryUsagePaths)

	// The v1 layout resolves, so it contributes one tuple per cgroup from this
	// process up to the mount root, followed by the root layouts the list
	// started with. The v2 layout has no "" membership here and is carried
	// through unrewritten.
	want := []cgroupUsagePath{
		{
			limit:        "/sys/fs/cgroup/memory/system.slice/silo.service/memory.limit_in_bytes",
			usage:        "/sys/fs/cgroup/memory/system.slice/silo.service/memory.usage_in_bytes",
			stat:         "/sys/fs/cgroup/memory/system.slice/silo.service/memory.stat",
			inactiveFile: cgroupInactiveFileKeyV1,
		},
		{
			limit:        "/sys/fs/cgroup/memory/system.slice/memory.limit_in_bytes",
			usage:        "/sys/fs/cgroup/memory/system.slice/memory.usage_in_bytes",
			stat:         "/sys/fs/cgroup/memory/system.slice/memory.stat",
			inactiveFile: cgroupInactiveFileKeyV1,
		},
		{
			limit:        cgroupMemoryUsagePaths[1].limit,
			usage:        cgroupMemoryUsagePaths[1].usage,
			stat:         cgroupMemoryUsagePaths[1].stat,
			inactiveFile: cgroupInactiveFileKeyV1,
		},
		{
			limit:        "/sys/fs/cgroup/memory.limit_in_bytes",
			usage:        "/sys/fs/cgroup/memory.usage_in_bytes",
			stat:         "/sys/fs/cgroup/memory.stat",
			inactiveFile: cgroupInactiveFileKeyV1,
		},
	}
	var v1 []cgroupUsagePath
	for _, level := range got {
		if strings.Contains(level.limit, "limit_in_bytes") {
			v1 = append(v1, level)
		}
	}
	if !slices.Equal(v1, want) {
		t.Fatalf("v1 levels =\n%+v\nwant\n%+v", v1, want)
	}

	// Every emitted level names all three files from one cgroup: memoryStats
	// picks by limit and then reads usage from the same tuple, so a mismatch
	// measures one population against another's capacity.
	for _, level := range got {
		if level.limit == "" {
			continue
		}
		dir := filepath.Dir(level.limit)
		if filepath.Dir(level.usage) != dir || filepath.Dir(level.stat) != dir {
			t.Fatalf("level %+v mixes cgroups; limit, usage and stat must share one directory", level)
		}
	}

	// With no membership the list is exactly what it was.
	if got := withCgroupSelfUsagePaths(nil, cgroupMemoryUsagePaths); !slices.Equal(got, cgroupMemoryUsagePaths) {
		t.Fatalf("withCgroupSelfUsagePaths(nil) = %+v, want the original list", got)
	}
}

// A limit is not always written where the process sits: a systemd unit inherits
// its quota from the slice containing it, and a container from its pod cgroup.
// The leaf reads "max" and the kernel throttles anyway, so a walk that stops at
// the leaf reports the whole host to a process that has two cores.
func TestCgroupAncestorPaths(t *testing.T) {
	got := cgroupAncestorPaths("/sys/fs/cgroup/kubepods/burstable/podabc/container1/cpu.max")
	want := []string{
		"/sys/fs/cgroup/kubepods/burstable/podabc/container1/cpu.max",
		"/sys/fs/cgroup/kubepods/burstable/podabc/cpu.max",
		"/sys/fs/cgroup/kubepods/burstable/cpu.max",
		"/sys/fs/cgroup/kubepods/cpu.max",
		cgroupCPUPaths[0].quota,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("cgroupAncestorPaths() =\n%v\nwant\n%v", got, want)
	}

	// The mount root itself has nowhere to climb to.
	if got := cgroupAncestorPaths(cgroupCPUPaths[0].quota); !slices.Equal(got, []string{cgroupCPUPaths[0].quota}) {
		t.Fatalf("cgroupAncestorPaths(root) = %v, want just the file", got)
	}
	// A path outside the hierarchy still reads itself: a test harness pointing
	// these at a temp directory has no ancestors, and must not lose its file.
	if got := cgroupAncestorPaths("/tmp/fake/cpu.max"); !slices.Equal(got, []string{"/tmp/fake/cpu.max"}) {
		t.Fatalf("cgroupAncestorPaths(outside) = %v, want just the file", got)
	}
	if got := cgroupAncestorPaths(""); got != nil {
		t.Fatalf("cgroupAncestorPaths(\"\") = %v, want none", got)
	}
}

// The quota a process is throttled against is the tightest anywhere above it,
// and it has to be paired with the period from the same cgroup — a quota from
// one level over a period from another describes no real budget.
func TestEffectiveCgroupCPUQuotaTakesTheTightestAncestor(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "system.slice", "silo.service")
	slice := filepath.Join(root, "system.slice")
	for _, dir := range []string{leaf, slice} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, name, err)
		}
	}

	// cgroupAncestorPaths only climbs inside the real cgroup mount, so the walk
	// is exercised here by handing effectiveCgroupCPUQuota each level directly.
	quotaAt := func(dir string) float64 {
		_, cores := effectiveCgroupCPUQuota(cgroupCPUPath{quota: filepath.Join(dir, "cpu.max")})
		return cores
	}

	// The service says "max" while its slice allows two cores.
	write(leaf, "cpu.max", "max 100000\n")
	write(slice, "cpu.max", "200000 100000\n")
	if got := quotaAt(leaf); got != 0 {
		t.Fatalf("leaf alone = %v cores, want 0 — it imposes none", got)
	}
	if got := quotaAt(slice); got != 2 {
		t.Fatalf("slice = %v cores, want 2", got)
	}

	// A leaf tighter than its slice wins, and a looser one loses.
	write(leaf, "cpu.max", "100000 100000\n")
	if _, got := effectiveCgroupCPUQuota(cgroupCPUPath{quota: filepath.Join(leaf, "cpu.max")}); got != 1 {
		t.Fatalf("tighter leaf = %v cores, want 1", got)
	}
}

// v1 keeps quota and period in separate files, so both have to move together as
// the walk climbs.
func TestEffectiveCgroupCPUQuotaPairsQuotaWithItsOwnPeriod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cpu.cfs_quota_us"), []byte("400000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.cfs_period_us"), []byte("100000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, got := effectiveCgroupCPUQuota(cgroupCPUPath{
		quota:  filepath.Join(dir, "cpu.cfs_quota_us"),
		period: filepath.Join(dir, "cpu.cfs_period_us"),
	})
	if got != 4 {
		t.Fatalf("v1 quota = %v cores, want 4", got)
	}
}

// The quota and the usage measured against it have to come from the same
// cgroup. A quota on a shared ancestor is spent by every service under it, so
// this process's own CPU time over that quota describes nothing: Silo at ten
// percent beside a sibling at ninety would report ten, while the group is
// saturated and being throttled.
func TestEffectiveCgroupCPUQuotaReturnsTheLevelThatBinds(t *testing.T) {
	root := t.TempDir()
	// The walk only climbs inside the cgroup mount, so the fixture becomes one.
	previousRoot := cgroupMountRoot
	cgroupMountRoot = root
	t.Cleanup(func() { cgroupMountRoot = previousRoot })

	leaf := filepath.Join(root, "silo.service")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatalf("create %s: %v", leaf, err)
	}
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, name, err)
		}
	}
	// The service imposes nothing; the slice above it allows two cores and
	// carries the usage of everything under it.
	write(leaf, "cpu.max", "max 100000\n")
	write(leaf, "cpu.stat", "usage_usec 1000000\n")
	write(root, "cpu.max", "200000 100000\n")
	write(root, "cpu.stat", "usage_usec 9000000\n")

	binding, cores := effectiveCgroupCPUQuota(cgroupCPUPath{
		usage:     filepath.Join(leaf, "cpu.stat"),
		usageKey:  cgroupCPUUsageKey,
		usageUnit: 1,
		quota:     filepath.Join(leaf, "cpu.max"),
	})
	if cores != 2 {
		t.Fatalf("cores = %v, want the slice's 2", cores)
	}
	if want := filepath.Join(root, "cpu.stat"); binding.usage != want {
		t.Fatalf("binding usage = %q, want the slice's own %q", binding.usage, want)
	}

	// Nothing above it binds: the level stays the leaf's, so an unconstrained
	// or leaf-limited process still measures itself.
	write(root, "cpu.max", "max 100000\n")
	write(leaf, "cpu.max", "100000 100000\n")
	binding, cores = effectiveCgroupCPUQuota(cgroupCPUPath{
		usage:     filepath.Join(leaf, "cpu.stat"),
		usageKey:  cgroupCPUUsageKey,
		usageUnit: 1,
		quota:     filepath.Join(leaf, "cpu.max"),
	})
	if cores != 1 {
		t.Fatalf("cores = %v, want the leaf's 1", cores)
	}
	if want := filepath.Join(leaf, "cpu.stat"); binding.usage != want {
		t.Fatalf("binding usage = %q, want the leaf's own %q", binding.usage, want)
	}
}

// A quota on a shared ancestor and a tighter cpuset on the leaf are caps on
// different populations. Taking the ancestor's usage — which counts every
// sibling under that quota — and dividing it by this process's smaller private
// cpuset pins the node at a hundred percent while Silo is idle.
func TestCgroupCPUMovesUsageDownWhenTheCpusetBinds(t *testing.T) {
	root := t.TempDir()
	previousRoot := cgroupMountRoot
	cgroupMountRoot = root
	t.Cleanup(func() { cgroupMountRoot = previousRoot })

	leaf := filepath.Join(root, "silo.service")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatalf("create %s: %v", leaf, err)
	}
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, name, err)
		}
	}
	// The slice allows eight cores and has burned a lot of CPU across every
	// service under it; this one has burned very little and may use two CPUs.
	write(root, "cpu.max", "800000 100000\n")
	write(root, "cpu.stat", "usage_usec 9000000\n")
	write(leaf, "cpu.max", "max 100000\n")
	write(leaf, "cpu.stat", "usage_usec 1000000\n")

	s := &Sampler{cgroupCPUPaths: []cgroupCPUPath{{
		usage:     filepath.Join(leaf, "cpu.stat"),
		usageKey:  cgroupCPUUsageKey,
		usageUnit: time.Microsecond,
		quota:     filepath.Join(leaf, "cpu.max"),
	}}}

	// No cpuset: the slice's quota binds, so its usage is the right numerator.
	sample, quota := s.cgroupCPU(time.Now(), 0)
	if quota != 8 {
		t.Fatalf("quota = %v, want the slice's 8 cores", quota)
	}
	if want := int64(9_000_000) * int64(time.Microsecond); sample.usageNS != want {
		t.Fatalf("usage = %d, want the slice's %d", sample.usageNS, want)
	}

	// A two-CPU cpuset is tighter, and it applies to this cgroup — so the
	// measurement moves back down with it.
	sample, quota = s.cgroupCPU(time.Now(), 2)
	if quota != 2 {
		t.Fatalf("quota = %v, want the cpuset's 2", quota)
	}
	if want := int64(1_000_000) * int64(time.Microsecond); sample.usageNS != want {
		t.Fatalf("usage = %d, want this cgroup's own %d, not the slice's", sample.usageNS, want)
	}

	// A cpuset looser than the quota changes nothing.
	sample, quota = s.cgroupCPU(time.Now(), 32)
	if quota != 8 {
		t.Fatalf("quota = %v, want the slice's 8 to still bind", quota)
	}
	if want := int64(9_000_000) * int64(time.Microsecond); sample.usageNS != want {
		t.Fatalf("usage = %d, want the slice's %d", sample.usageNS, want)
	}
}

// Two cgroups publishing the same quota are not equivalent. The ancestor's is
// shared with siblings that can exhaust it, so it is the level whose usage
// describes what is actually being throttled: Silo at 0.2 cores beside a
// sibling at 1.8 under a shared two-core parent reads ten percent from the leaf
// while the parent is saturated.
func TestEffectiveCgroupCPUQuotaPrefersTheOuterLevelOnATie(t *testing.T) {
	root := t.TempDir()
	previousRoot := cgroupMountRoot
	cgroupMountRoot = root
	t.Cleanup(func() { cgroupMountRoot = previousRoot })

	leaf := filepath.Join(root, "silo.service")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatalf("create %s: %v", leaf, err)
	}
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, name, err)
		}
	}
	write(leaf, "cpu.max", "200000 100000\n")
	write(leaf, "cpu.stat", "usage_usec 200000\n")
	write(root, "cpu.max", "200000 100000\n")
	write(root, "cpu.stat", "usage_usec 2000000\n")

	binding, cores := effectiveCgroupCPUQuota(cgroupCPUPath{
		usage:     filepath.Join(leaf, "cpu.stat"),
		usageKey:  cgroupCPUUsageKey,
		usageUnit: 1,
		quota:     filepath.Join(leaf, "cpu.max"),
	})
	if cores != 2 {
		t.Fatalf("cores = %v, want 2 from either level", cores)
	}
	if want := filepath.Join(root, "cpu.stat"); binding.usage != want {
		t.Fatalf("binding usage = %q, want the shared parent's %q", binding.usage, want)
	}
}
