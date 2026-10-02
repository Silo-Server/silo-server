package nodemetrics

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const bytesPerKB = int64(1024)

// cgroupInactiveFileKeyV2 is the cgroup v2 memory.stat key for reclaimable
// page cache. cgroup v1's memory.stat spells the same figure
// "total_inactive_file".
const (
	cgroupInactiveFileKeyV2 = "inactive_file"
	// cgroupInactiveFileKeyV1 is the same quantity under cgroup v1, which
	// prefixes its memory.stat totals.
	cgroupInactiveFileKeyV1 = "total_inactive_file"
)

// CgroupMemoryLimitPaths returns the memory-limit files to consult, cgroup v2
// first. A container that has a limit publishes it in exactly one of these; a
// host has neither.
//
// It returns a fresh slice per call so a caller iterating it cannot reorder the
// preference for everyone else.
func CgroupMemoryLimitPaths() []string {
	paths := make([]string, 0, len(cgroupMemoryUsagePaths))
	for _, level := range cgroupMemoryUsagePaths {
		paths = append(paths, level.limit)
	}
	return paths
}

// cgroupUsagePath pairs one cgroup level's limit with the current-usage file,
// the stat file, and the key that names its page cache.
//
// All four move together because a limit and the usage measured against it have
// to describe the same cgroup. When the binding limit comes from an ancestor —
// a pod cgroup shared with sidecars, a systemd slice shared with other services
// — the memory that fills it is everything charged to that ancestor, not just
// this process's leaf. Pairing the parent's capacity with the leaf's working
// set shows headroom that does not exist, right up until the parent OOMs.
type cgroupUsagePath struct {
	limit        string
	usage        string
	stat         string
	inactiveFile string
}

// cgroupMemoryUsagePaths lists where to read current memory charge, v2 first.
//
// Usage counts reclaimable page cache, so reporting it raw would show a node
// that merely read a large file as nearly out of memory; subtracting inactive
// file pages yields the working set, which is the number that actually predicts
// an OOM kill (and is what `docker stats` reports).
var cgroupMemoryUsagePaths = []cgroupUsagePath{
	{
		limit:        "/sys/fs/cgroup/memory.max",
		usage:        "/sys/fs/cgroup/memory.current",
		stat:         "/sys/fs/cgroup/memory.stat",
		inactiveFile: cgroupInactiveFileKeyV2,
	},
	{
		limit:        "/sys/fs/cgroup/memory/memory.limit_in_bytes",
		usage:        "/sys/fs/cgroup/memory/memory.usage_in_bytes",
		stat:         "/sys/fs/cgroup/memory/memory.stat",
		inactiveFile: cgroupInactiveFileKeyV1,
	},
}

// ReadMeminfoTotalBytes returns MemTotal from a /proc/meminfo-formatted file.
//
// This lives here rather than beside its first caller because two unrelated
// subsystems need the same answer — Postgres auto-tuning sizes shared_buffers
// from it, and node metrics report it — and a host's memory is one fact, not
// two implementations of one fact.
func ReadMeminfoTotalBytes(path string) (int64, error) {
	fields, err := ReadMeminfoBytes(path)
	if err != nil {
		return 0, err
	}
	total, ok := fields["MemTotal"]
	if !ok {
		return 0, errors.New("MemTotal not found")
	}
	return total, nil
}

// ReadMeminfoBytes parses a /proc/meminfo-formatted file into bytes per key.
// Keys are returned without the trailing colon. Lines that do not parse are
// skipped rather than failing the read: meminfo grows new keys across kernel
// versions, and one unfamiliar line must not cost the caller every familiar
// one.
func ReadMeminfoBytes(path string) (map[string]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	values := make(map[string]int64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := parseMeminfoLine(scanner.Text())
		if !ok {
			continue
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// parseMeminfoLine reads one "Key:  <value> kB" line, converting to bytes. The
// unit column is optional (a handful of meminfo entries are bare counts).
func parseMeminfoLine(line string) (string, int64, bool) {
	key, rest, ok := strings.Cut(line, ":")
	if !ok {
		return "", 0, false
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", 0, false
	}
	value, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return "", 0, false
	}
	if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
		value *= bytesPerKB
	}
	return strings.TrimSpace(key), value, true
}

// EffectiveMemoryLimitBytes returns the tightest cgroup memory limit in force
// on this process, or 0 when none is set.
//
// The root-level limit files alone are only right inside a cgroup-namespaced
// container. A systemd unit with MemoryMax=, or a leaf inheriting a tighter
// limit from a slice or pod ancestor, publishes its limit at this process's
// own cgroup or somewhere above it while the root file reads "max" — so this
// walks the process's own cgroup, every ancestor, and the mount root, and
// takes the tightest concrete limit found, mirroring what the sampler reports
// (the kernel OOM-kills against the tightest level).
func EffectiveMemoryLimitBytes() int64 {
	return effectiveMemoryLimitBytes("/proc", cgroupMemoryUsagePaths)
}

func effectiveMemoryLimitBytes(procDir string, layouts []cgroupUsagePath) int64 {
	relative := cgroupRelativePaths(procDir)
	tightest := int64(0)
	for _, level := range withCgroupSelfUsagePaths(relative, layouts) {
		limit, err := ReadCgroupMemoryLimit(level.limit)
		if err != nil || limit <= 0 {
			continue
		}
		if tightest == 0 || limit < tightest {
			tightest = limit
		}
	}
	return tightest
}

// ReadCgroupMemoryLimit reads a cgroup memory-limit file and returns the limit
// in bytes, or an error when the cgroup imposes none.
//
// "No limit" is spelled three different ways depending on kernel and runtime —
// the literal "max", an empty file, or a saturated sentinel close to 2^63 — and
// all three must read as "ask the host instead", never as an enormous budget.
func ReadCgroupMemoryLimit(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "max" {
		return 0, fmt.Errorf("no cgroup memory limit")
	}
	mem, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	// Docker may expose a huge sentinel when no concrete memory limit is set.
	if mem <= 0 || mem > 1<<60 {
		return 0, fmt.Errorf("no concrete cgroup memory limit")
	}
	return mem, nil
}

// readCgroupSingleValue reads a file holding one integer.
func readCgroupSingleValue(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("negative cgroup value")
	}
	return value, nil
}

// readCgroupStatKey reads one key out of a "key value" cgroup stat file.
func readCgroupStatKey(path, key string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != key {
			continue
		}
		return strconv.ParseInt(fields[1], 10, 64)
	}
	return 0, fmt.Errorf("%s not found in %s", key, path)
}
