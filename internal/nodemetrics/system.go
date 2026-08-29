package nodemetrics

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// cpuTimes is one /proc/stat aggregate reading. Both fields are monotonic jiffy
// counters; only differences between two readings mean anything.
type cpuTimes struct {
	busy  uint64
	total uint64
	valid bool
}

// ifaceCounters is one interface's monotonic byte counters.
type ifaceCounters struct {
	rx uint64
	tx uint64
}

// netCounters is one /proc/net/dev reading, excluding loopback.
//
// Counters are kept per interface rather than pre-summed because deltas are
// only meaningful per interface: an interface that enters the namespace
// mid-run (a veth or tunnel moved into a running container) arrives carrying
// its lifetime totals, and an aggregate baseline would report all of that
// history as one interval's traffic.
type netCounters struct {
	interfaces map[string]ifaceCounters
	at         time.Time
	valid      bool
}

// readCPUTimes parses the aggregate "cpu" line of /proc/stat and counts the
// per-core lines beneath it.
//
// Idle and iowait are both subtracted from busy: a core waiting on storage is
// not doing work, and counting it as busy would make every node with a slow
// disk look CPU-bound.
func readCPUTimes(procDir string) (cpuTimes, int) {
	raw, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		return cpuTimes{}, 0
	}
	var times cpuTimes
	cores := 0
	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			// "cpu0", "cpu1", … — one line per online core.
			cores++
			continue
		}
		if times.valid {
			continue
		}
		var total, idle uint64
		for i, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				continue
			}
			// Columns are user, nice, system, idle, iowait, irq, softirq,
			// steal, guest, guest_nice. guest time is already included in user,
			// so summing past steal would double-count it.
			if i >= 8 {
				break
			}
			total += value
			if i == 3 || i == 4 {
				idle += value
			}
		}
		if total == 0 {
			continue
		}
		times = cpuTimes{busy: total - idle, total: total, valid: true}
	}
	return times, cores
}

// cpuBusyPercent converts two readings into a busy percentage.
//
// A counter that went backwards means the readings do not describe one
// continuous run (a container was migrated, /proc was remounted, or a test
// rewound the fixture), so the pair is unusable rather than negative.
func cpuBusyPercent(previous, current cpuTimes) (int, bool) {
	if !previous.valid || !current.valid {
		return 0, false
	}
	if current.total <= previous.total || current.busy < previous.busy {
		return 0, false
	}
	totalDelta := current.total - previous.total
	busyDelta := current.busy - previous.busy
	return clampPercent(int((busyDelta*100 + totalDelta/2) / totalDelta)), true
}

// readLoad1 parses the 1-minute load average from /proc/loadavg.
func readLoad1(procDir string) float64 {
	raw, err := os.ReadFile(filepath.Join(procDir, "loadavg"))
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// readNetCounters sums received and transmitted bytes across every interface
// except loopback, which would otherwise double-count traffic a node sends to
// itself (health checks, the local proxy hop).
func readNetCounters(procDir string, at time.Time) netCounters {
	raw, err := os.ReadFile(filepath.Join(procDir, "net", "dev"))
	if err != nil {
		return netCounters{}
	}
	counters := netCounters{at: at, interfaces: map[string]ifaceCounters{}}
	for line := range strings.Lines(string(raw)) {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			// The two header lines carry no colon.
			continue
		}
		iface := strings.TrimSpace(name)
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		// Eight receive columns then eight transmit columns; bytes lead each.
		if len(fields) < 9 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			continue
		}
		counters.interfaces[iface] = ifaceCounters{rx: rx, tx: tx}
		counters.valid = true
	}
	return counters
}

// netThroughputBps converts two readings into bits per second, summing only
// per-interface deltas between interfaces present in both readings.
//
// The pairing is what keeps every kind of interface churn out of an
// operator's bandwidth graph. An interface that appears between readings
// arrives with its lifetime totals — history, not this interval's traffic —
// so it contributes nothing until the next reading gives it a baseline. One
// that disappears takes only its own baseline with it, so the remaining
// interfaces keep measuring instead of being masked by a shrunken aggregate.
// And a single counter that runs backwards (a 32-bit counter on a busy link
// wrapping, or a device slot reused) zeroes only that interface's delta,
// since an invented number is worse than a missing one.
func netThroughputBps(previous, current netCounters) (rxBps, txBps int64, ok bool) {
	if !previous.valid || !current.valid {
		return 0, 0, false
	}
	seconds := current.at.Sub(previous.at).Seconds()
	if seconds <= 0 {
		return 0, 0, false
	}
	var rxDelta, txDelta uint64
	for name, cur := range current.interfaces {
		prev, seen := previous.interfaces[name]
		if !seen {
			continue
		}
		if cur.rx >= prev.rx {
			rxDelta += cur.rx - prev.rx
		}
		if cur.tx >= prev.tx {
			txDelta += cur.tx - prev.tx
		}
	}
	return int64(float64(rxDelta) * 8 / seconds), int64(float64(txDelta) * 8 / seconds), true
}

// procDirFor resolves which proc tree to read name ("stat", "loadavg", or
// "meminfo") from — the three files a container's cgroup cannot itself
// correct.
//
// On an LXC host running Docker nested inside it, this process's own /proc is
// the raw kernel view: /proc/stat, /proc/loadavg, and /proc/meminfo describe
// the physical machine rather than the LXC, and this nested container's own
// cgroup shows no limit at all, because the LXC's cap lives on an ancestor
// cgroup outside this container's namespace that it cannot see or read.
// lxcfs, running on the LXC host, virtualizes those same three files to the
// LXC's own limits; when an operator bind-mounts that virtualized view in at
// hostProcDir/<name>, it is the only correct source, so it wins whenever
// present. Otherwise procDir/<name> — the file this process actually sees —
// is the only option, exactly as on plain Docker or bare metal.
func (s *Sampler) procDirFor(name string) string {
	if s.hostProcDir != "" {
		if _, err := os.Stat(filepath.Join(s.hostProcDir, name)); err == nil {
			return s.hostProcDir
		}
	}
	return s.procDir
}

// memoryStats reports used and total bytes for this process's memory domain.
//
// /proc/meminfo describes the host even inside a container, so a cgroup limit —
// which is what the kernel will actually OOM-kill against — always wins over
// it, and cgroup usage (page cache excluded) wins over the host's own
// used figure for the same reason.
func (s *Sampler) memoryStats() (usedBytes, totalBytes int64) {
	fields, err := ReadMeminfoBytes(filepath.Join(s.procDirFor("meminfo"), "meminfo"))
	if err == nil {
		totalBytes = fields["MemTotal"]
		if available, ok := fields["MemAvailable"]; ok && totalBytes >= available {
			usedBytes = totalBytes - available
		}
	}

	// Every limit in force is read, not just the first: the list runs from this
	// process's own cgroup up through its ancestors, and a leaf that says "max"
	// can still sit inside a slice or pod cgroup that does not. The kernel
	// OOM-kills against the tightest of them, so that is the one to report — and
	// the level it came from is kept, because the usage that fills it is
	// everything charged to that cgroup, not just this process's own.
	var binding *cgroupUsagePath
	hostTotal := totalBytes
	for i, level := range s.cgroupUsagePaths {
		limit, err := ReadCgroupMemoryLimit(level.limit)
		if err != nil || limit <= 0 {
			continue
		}
		// A limit at or above the host's memory is not a limit worth reporting;
		// it would only make a node look like it has headroom the kernel cannot
		// give it.
		if hostTotal > 0 && limit >= hostTotal {
			continue
		}
		// Ties go to the outer level, which the list reaches later. Two cgroups
		// publishing the same limit are not equivalent: the ancestor's is shared
		// with siblings that can fill it, so it is the one whose usage says how
		// much of it is left. Reading the leaf instead shows headroom right up
		// until the parent OOMs. Compared against the running choice rather than
		// against the host figure, so a cgroup limit that merely equals host RAM
		// still reads as no limit at all.
		if binding == nil || limit <= totalBytes {
			totalBytes = limit
			binding = &s.cgroupUsagePaths[i]
		}
	}

	// Only when the total above is the cgroup's. A container with no memory
	// limit still publishes a readable memory.current, so taking it
	// unconditionally would pair this process's working set with the host's RAM
	// — "1 GiB of 64 GiB" on a machine that is nearly out of memory, because the
	// two numbers describe different domains. Whichever domain total came from,
	// used has to come from the same one.
	if binding != nil {
		if usage, ok := cgroupMemoryUsage(*binding); ok {
			usedBytes = usage
		}
	}
	if totalBytes > 0 && usedBytes > totalBytes {
		usedBytes = totalBytes
	}
	return usedBytes, totalBytes
}

// cgroupMemoryUsage returns the working set of one memory cgroup: its current
// charge minus reclaimable file pages.
//
// The level is the caller's choice rather than a search, because the only level
// worth measuring is the one whose limit binds — see memoryStats.
func cgroupMemoryUsage(level cgroupUsagePath) (int64, bool) {
	usage, err := readCgroupSingleValue(level.usage)
	if err != nil {
		return 0, false
	}
	if inactive, err := readCgroupStatKey(level.stat, level.inactiveFile); err == nil && inactive > 0 && inactive <= usage {
		usage -= inactive
	}
	return usage, true
}

func clampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

const bytesPerMB = int64(1024 * 1024)

func bytesToMB(value int64) int64 { return value / bytesPerMB }
