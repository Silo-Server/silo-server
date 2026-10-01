package nodemetrics

import (
	"os"
	"path"
	"strings"
)

// Resolving this process's own cgroup.
//
// Every cgroup file this package reads is named from the mount root —
// /sys/fs/cgroup/cpu.max and friends. Inside a container that is exactly right:
// the cgroup namespace makes the container's own cgroup appear as the root, so
// the root files describe the container.
//
// A process that is limited *without* being namespaced sees something else. A
// systemd unit with CPUQuota= or MemoryMax= lives at
// /sys/fs/cgroup/system.slice/silo.service/, and the root files there describe
// the whole machine. Reading them reports host-wide CPU busyness and the host's
// memory total, so a service pegged at its quota looks mostly idle with plenty
// of memory left — the exact failure the cgroup correction exists to prevent,
// reintroduced for anyone who runs Silo as a plain service rather than a
// container.
//
// So each path is tried at this process's own cgroup first and at the root
// second. The fallback is what keeps every container case working unchanged:
// a namespaced container reports "/" and rewrites to the root anyway, and a
// container without a cgroup namespace reports a host path
// (/docker/<id>) that does not exist under its own mount, so the rewritten
// path simply fails to open and the root read happens as before.

// cgroupMountRoot is where the cgroup hierarchy is mounted on Linux. It is a
// var so a test can point the ancestor walk at a temporary tree; nothing in
// production writes it.
var cgroupMountRoot = "/sys/fs/cgroup"

// cgroupRelativePaths reports this process's path within each cgroup hierarchy,
// read from <procDir>/self/cgroup.
//
// The v2 unified hierarchy has an empty controller field and is keyed by "".
// A v1 line names one or more controllers, and is keyed both by each controller
// individually and by the whole comma-joined field, because that joined form is
// what the mount directory is named ("cpu,cpuacct").
//
// A missing or unreadable file yields no entries, which leaves every path at
// the root — the behavior this process had before.
func cgroupRelativePaths(procDir string) map[string]string {
	raw, err := os.ReadFile(path.Join(procDir, "self", "cgroup"))
	if err != nil {
		return nil
	}
	paths := map[string]string{}
	for line := range strings.Lines(string(raw)) {
		// "<hierarchy-id>:<controllers>:<path>", and the path may itself
		// contain colons, so only the first two separators are structural.
		fields := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(fields) != 3 {
			continue
		}
		controllers, relative := fields[1], strings.TrimPrefix(fields[2], "/")
		if relative == "" {
			// Already at the root of its hierarchy: a namespaced container, or
			// an unconstrained process. Recording it would rewrite to the same
			// place at extra cost.
			continue
		}
		paths[controllers] = relative
		for _, controller := range strings.Split(controllers, ",") {
			if controller != "" {
				paths[controller] = relative
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return paths
}

// cgroupSelfFile rewrites a root-relative cgroup file path to this process's own
// cgroup, or returns "" when it already reads the right file.
//
// The controller is taken from the path itself: a v1 file sits under a
// controller directory (/sys/fs/cgroup/memory/memory.stat), a v2 file sits
// directly at the root (/sys/fs/cgroup/memory.stat) and belongs to the unified
// hierarchy, which cgroupRelativePaths keys by "".
func cgroupSelfFile(relative map[string]string, file string) string {
	if len(relative) == 0 {
		return ""
	}
	rest, ok := strings.CutPrefix(file, cgroupMountRoot+"/")
	if !ok {
		return ""
	}
	controller, name := "", rest
	if dir, base, found := strings.Cut(rest, "/"); found {
		controller, name = dir, base
	}
	own, ok := relative[controller]
	if !ok || name == "" {
		return ""
	}
	return path.Join(cgroupMountRoot, controller, own, name)
}

// cgroupAncestorPaths returns file and the same file name at every cgroup above
// it, nearest first, ending at the hierarchy mount root.
//
// A limit is not always written where the process sits. A systemd unit can
// inherit its quota from the slice that contains it, and a container can inherit
// one from its pod cgroup; in both cases the leaf reads "max" while the kernel
// throttles against an ancestor. Reading only the leaf would report the host's
// whole capacity for a process that has far less.
//
// The walk is by path, so every level it produces is a genuine ancestor of this
// process. Levels that hold no such file simply fail to read, which is how the
// v1 layouts skip the unified root they never had.
func cgroupAncestorPaths(file string) []string {
	if file == "" {
		return nil
	}
	// The file itself is always a level. Only the walk above it needs the file
	// to live under the cgroup mount — a test harness pointing these at a temp
	// directory has no hierarchy to climb, and must still read what it was given.
	if !strings.HasPrefix(file, cgroupMountRoot+"/") {
		return []string{file}
	}
	name := path.Base(file)
	out := []string{file}
	for dir := path.Dir(file); strings.HasPrefix(dir, cgroupMountRoot); dir = path.Dir(dir) {
		if candidate := path.Join(dir, name); candidate != file {
			out = append(out, candidate)
		}
		if dir == cgroupMountRoot {
			break
		}
	}
	return out
}

// withCgroupSelfPaths returns files preceded by their this-process equivalents
// and every cgroup between the two, so a read sees each limit in force on this
// process rather than only the nearest and the root.
//
// The caller picks the tightest of what it can read, not the first: an ancestor
// with a finite limit binds a leaf that says "max", so stopping at the first
// readable file would report no limit for a process that has one.
func withCgroupSelfPaths(relative map[string]string, files []string) []string {
	out := make([]string, 0, len(files)*2)
	seen := make(map[string]bool, len(files)*2)
	add := func(candidate string) {
		if candidate == "" || seen[candidate] {
			return
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	for _, file := range files {
		if own := cgroupSelfFile(relative, file); own != "" {
			for _, candidate := range cgroupAncestorPaths(own) {
				add(candidate)
			}
		}
		add(file)
	}
	return out
}

// withCgroupSelfCPUPaths is withCgroupSelfPaths for the CPU layouts, where one
// entry names several files that must be rewritten together — a usage file from
// this process's cgroup paired with the root's quota would normalize the
// service's own CPU time against the whole machine's budget.
func withCgroupSelfCPUPaths(relative map[string]string, layouts []cgroupCPUPath) []cgroupCPUPath {
	out := make([]cgroupCPUPath, 0, len(layouts)*2)
	for _, layout := range layouts {
		own := layout
		own.usage = cgroupSelfFile(relative, layout.usage)
		own.quota = cgroupSelfFile(relative, layout.quota)
		if layout.period != "" {
			own.period = cgroupSelfFile(relative, layout.period)
		}
		if own.usage != "" && own.quota != "" && (layout.period == "" || own.period != "") {
			out = append(out, own)
		}
		out = append(out, layout)
	}
	return out
}

// withCgroupSelfUsagePaths is withCgroupSelfPaths for the memory layouts, which
// name three files that have to move together: memoryStats picks the level whose
// *limit* binds and then reads usage from that same level, so a tuple whose
// limit and usage come from different cgroups measures one population against
// another's capacity.
//
// Every level from this process's own cgroup up to the mount root is emitted,
// because the binding limit is often an ancestor's — a slice with MemoryMax=, a
// pod cgroup shared with sidecars — while the leaf says "max".
func withCgroupSelfUsagePaths(relative map[string]string, layouts []cgroupUsagePath) []cgroupUsagePath {
	out := make([]cgroupUsagePath, 0, len(layouts)*2)
	seen := make(map[string]bool, len(layouts)*2)
	add := func(level cgroupUsagePath) {
		if level.limit == "" || seen[level.limit] {
			return
		}
		seen[level.limit] = true
		out = append(out, level)
	}
	for _, layout := range layouts {
		if own := cgroupSelfFile(relative, layout.limit); own != "" {
			usageName, statName := path.Base(layout.usage), path.Base(layout.stat)
			for _, ancestor := range cgroupAncestorPaths(own) {
				dir := path.Dir(ancestor)
				add(cgroupUsagePath{
					limit:        ancestor,
					usage:        path.Join(dir, usageName),
					stat:         path.Join(dir, statName),
					inactiveFile: layout.inactiveFile,
				})
			}
		}
		add(layout)
	}
	return out
}
