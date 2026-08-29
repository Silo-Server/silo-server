package nodemetrics

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// cgroup CPU correction.
//
// /proc/stat describes the host even inside a container, exactly as
// /proc/meminfo does. A transcode node limited to two cores on a 64-core host
// would otherwise report the host's busyness against the host's core count —
// pinned at its quota and dropping segments while the dashboard shows a few
// percent idle. So CPU is corrected the same way memory is: the cgroup's own
// cumulative usage is the busy signal, and its quota is what that usage is
// normalized against.
//
// The correction applies only where something actually caps CPU. A cgroup
// imposes no limit far more often than it imposes one — every unconstrained
// container and systemd service has one — and its usage then describes Silo
// alone rather than the machine, so an uncapped deployment keeps reading
// /proc/stat and reports the load it is really competing with.

// cgroupCPUPath locates one cgroup version's CPU accounting.
type cgroupCPUPath struct {
	// usage is the file holding cumulative CPU time consumed by the cgroup.
	usage string
	// usageKey names the row to read when usage is a "key value" table; empty
	// when the file holds a bare integer.
	usageKey string
	// usageUnit is how long one unit in that file is.
	usageUnit time.Duration
	// quota holds the CPU budget: cgroup v2's "<quota|max> <period>" pair, or
	// cgroup v1's quota alone.
	quota string
	// period is v1's separate period file; empty when quota carries both.
	period string
}

// cgroupCPUUsageKey names the cumulative-usage row of cgroup v2's cpu.stat.
const cgroupCPUUsageKey = "usage_usec"

// cgroupCPUPaths lists where to read CPU accounting, v2 first. cgroup v1 mounts
// cpu and cpuacct together on most distributions and separately on some, so
// both layouts are tried.
var cgroupCPUPaths = []cgroupCPUPath{
	{
		usage:     "/sys/fs/cgroup/cpu.stat",
		usageKey:  cgroupCPUUsageKey,
		usageUnit: time.Microsecond,
		quota:     "/sys/fs/cgroup/cpu.max",
	},
	{
		usage:     "/sys/fs/cgroup/cpu,cpuacct/cpuacct.usage",
		usageUnit: time.Nanosecond,
		quota:     "/sys/fs/cgroup/cpu,cpuacct/cpu.cfs_quota_us",
		period:    "/sys/fs/cgroup/cpu,cpuacct/cpu.cfs_period_us",
	},
	{
		usage:     "/sys/fs/cgroup/cpuacct/cpuacct.usage",
		usageUnit: time.Nanosecond,
		quota:     "/sys/fs/cgroup/cpu/cpu.cfs_quota_us",
		period:    "/sys/fs/cgroup/cpu/cpu.cfs_period_us",
	},
}

// cgroupCPUSetPaths lists where a cgroup publishes the CPUs it may run on, v2
// first. The "effective" file is what the kernel actually allows after
// intersecting with every ancestor, which is the number a process is really
// bounded by; the plain file is the request, and is read only where the
// effective one is absent.
var cgroupCPUSetPaths = []string{
	"/sys/fs/cgroup/cpuset.cpus.effective",
	"/sys/fs/cgroup/cpuset/cpuset.effective_cpus",
	"/sys/fs/cgroup/cpuset.cpus",
	"/sys/fs/cgroup/cpuset/cpuset.cpus",
}

// cgroupCPUSetCores counts the CPUs this cgroup may run on, or 0 when it is not
// restricted to a subset.
//
// A cpuset is the other way a deployment caps CPU, and unlike a CFS quota it
// leaves cpu.max saying "max". Without reading it, a process pinned to two CPUs
// on a sixty-four core host divides its own busy time by sixty-four and reports
// three percent while it is saturated — which defeats the whole point of the
// correction.
//
// The effective file already accounts for ancestors, so unlike the quota this
// needs no walk of its own; where only the pre-intersection file exists, the
// per-level candidates cover the same ground.
//
// A cpuset that spans the whole host is no cpuset at all — see cgroupCapBinds,
// which is the rule, and which applies to the CFS quota the same way.
func cgroupCPUSetCores(paths []string, hostCores int) int {
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		count := countCPUSetEntries(string(raw))
		if count <= 0 {
			continue
		}
		if !cgroupCapBinds(float64(count), hostCores) {
			// The effective set is the intersection with every ancestor, so no
			// file later in the list can narrow what this one just said was the
			// whole machine.
			return 0
		}
		return count
	}
	return 0
}

// cgroupCapBinds reports whether a CPU cap of the given size in cores actually
// restricts a process on a host with hostCores CPUs.
//
// A cap as large as the machine restricts nothing, and it is not a rare
// misconfiguration: every unconstrained container and service publishes an
// effective cpuset holding every online CPU, because it inherits one from a root
// that does, and a deployment sized to "the whole box" writes a quota to match.
//
// Reading such a cap as binding is worse than ignoring it. The cap decides which
// cgroup's usage is measured, not just what it is divided by, so an idle Silo
// beside a saturated neighbor on a shared host would report its own few percent
// as the machine's load — the exact misreport this whole correction exists to
// prevent, arrived at from the other direction.
//
// A host size of 0 means /proc/stat could not be counted, and an unknown host is
// no reason to discard a cap that may well be real.
func cgroupCapBinds(cores float64, hostCores int) bool {
	if cores <= 0 {
		return false
	}
	return hostCores <= 0 || cores < float64(hostCores)
}

// countCPUSetEntries counts the CPUs in a Linux cpu list ("0-3,8,12-13").
// An unparseable or empty list counts nothing rather than guessing.
func countCPUSetEntries(list string) int {
	total := 0
	for _, part := range strings.Split(strings.TrimSpace(list), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		low, high, ranged := strings.Cut(part, "-")
		first, err := strconv.Atoi(strings.TrimSpace(low))
		if err != nil || first < 0 {
			continue
		}
		if !ranged {
			total++
			continue
		}
		last, err := strconv.Atoi(strings.TrimSpace(high))
		if err != nil || last < first {
			continue
		}
		total += last - first + 1
	}
	return total
}

// cgroupCPUSample is one cumulative CPU-time reading. Only differences between
// two readings mean anything.
type cgroupCPUSample struct {
	usageNS int64
	at      time.Time
	valid   bool
}

// cgroupCPU returns this process's cgroup CPU reading for the given instant,
// and the CPU budget in cores that reading must be normalized against (0 when
// nothing caps it).
//
// pinned is the size of this process's cpuset, or 0 when it is not restricted
// to a subset. It is a second kind of cap and it belongs here rather than at the
// caller, because which constraint binds decides which cgroup's usage the
// reading has to come from — and the two answers are different populations.
func (s *Sampler) cgroupCPU(now time.Time, pinned int) (cgroupCPUSample, float64) {
	for _, paths := range s.cgroupCPUPaths {
		if _, err := readCgroupCPUUsage(paths); err != nil {
			continue
		}
		// Usage comes from whichever level supplied the binding quota, not from
		// the leaf. They are one measurement: a quota shared with sibling
		// services throttles on their CPU time too, so dividing only this
		// process's by it reports ten percent for a group that is saturated and
		// being throttled — the same pairing error the memory path had.
		binding, quota := effectiveCgroupCPUQuota(paths)

		// A cpuset applies to this cgroup, not to the ancestor that owns the
		// quota. So when it is the tighter cap the measurement moves back down
		// with it: the ancestor's usage counts siblings that do not share this
		// cpuset, and dividing that by a smaller private budget pins the node at
		// a hundred percent while Silo is idle.
		if pinned > 0 && (quota <= 0 || float64(pinned) < quota) {
			binding, quota = paths, float64(pinned)
		}

		usage, err := readCgroupCPUUsage(binding)
		if err != nil {
			continue
		}
		return cgroupCPUSample{usageNS: usage, at: now, valid: true}, quota
	}
	return cgroupCPUSample{}, 0
}

// effectiveCgroupCPUQuota returns the tightest CPU budget in force on this
// cgroup, in cores, together with the level that imposes it.
//
// The quota is whatever the kernel will actually throttle against, which is the
// smallest limit anywhere between here and the mount root. A systemd unit inside
// a slice with CPUQuota=, or a container under a limited pod cgroup, reads "max"
// at its leaf and is nonetheless capped; taking the leaf's answer would
// normalize a two-core service against sixty-four.
//
// The level is returned because usage has to be read from it too. A quota on a
// shared ancestor is spent by every service under it, so this process's own CPU
// time over that quota describes nothing: Silo at ten percent beside a sibling
// at ninety reports ten, while the group is saturated and throttled.
//
// Everything within a level moves together: a quota from one cgroup divided by a
// period from another describes no real budget.
func effectiveCgroupCPUQuota(paths cgroupCPUPath) (cgroupCPUPath, float64) {
	quotas := cgroupAncestorPaths(paths.quota)
	periods := cgroupAncestorPaths(paths.period)
	usages := cgroupAncestorPaths(paths.usage)
	binding, tightest := paths, 0.0
	for i, quota := range quotas {
		level := paths
		level.quota = quota
		if i < len(usages) {
			level.usage = usages[i]
		}
		if paths.period != "" {
			if i >= len(periods) {
				break
			}
			level.period = periods[i]
		}
		cores, err := readCgroupCPUQuota(level)
		if err != nil || cores <= 0 {
			continue
		}
		// Ties go to the outer level, which the walk reaches later. Two cgroups
		// publishing the same quota are not equivalent: the ancestor's is shared
		// with siblings that can exhaust it, so it is the one whose usage
		// describes what is being throttled. Silo at 0.2 cores beside a sibling
		// at 1.8 under a shared two-core parent reads ten percent from the leaf
		// while the parent is saturated.
		if tightest == 0 || cores <= tightest {
			tightest, binding = cores, level
		}
	}
	return binding, tightest
}

// readCgroupCPUUsage returns cumulative cgroup CPU time in nanoseconds.
func readCgroupCPUUsage(paths cgroupCPUPath) (int64, error) {
	var value int64
	var err error
	if paths.usageKey != "" {
		value, err = readCgroupStatKey(paths.usage, paths.usageKey)
	} else {
		value, err = readCgroupSingleValue(paths.usage)
	}
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("negative cgroup cpu usage")
	}
	return value * int64(paths.usageUnit), nil
}

// readCgroupCPUQuota returns the cgroup's CPU budget in cores, or an error when
// it imposes none.
//
// "No quota" is spelled "max" in v2 and "-1" in v1, and both must read as "this
// cgroup may use the whole host", never as a budget of zero cores.
func readCgroupCPUQuota(paths cgroupCPUPath) (float64, error) {
	raw, err := os.ReadFile(paths.quota)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty cgroup cpu quota")
	}
	if fields[0] == "max" {
		return 0, fmt.Errorf("no cgroup cpu quota")
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	if quota <= 0 {
		return 0, fmt.Errorf("no cgroup cpu quota")
	}

	period := int64(0)
	if len(fields) > 1 {
		// cgroup v2 prints the period beside the quota.
		period, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
	} else if paths.period != "" {
		period, err = readCgroupSingleValue(paths.period)
		if err != nil {
			return 0, err
		}
	}
	if period <= 0 {
		return 0, fmt.Errorf("no cgroup cpu period")
	}
	return float64(quota) / float64(period), nil
}

// cgroupCPUPercent converts two cgroup CPU readings into a busy percentage of
// the given core budget.
//
// A usage counter that went backwards means the readings do not describe one
// continuous run (the container was restarted or migrated), so the pair is
// unusable rather than negative.
func cgroupCPUPercent(previous, current cgroupCPUSample, cores float64) (int, bool) {
	if !previous.valid || !current.valid || cores <= 0 {
		return 0, false
	}
	elapsedNS := current.at.Sub(previous.at).Nanoseconds()
	if elapsedNS <= 0 || current.usageNS < previous.usageNS {
		return 0, false
	}
	busy := float64(current.usageNS-previous.usageNS) * 100 / (float64(elapsedNS) * cores)
	return clampPercent(int(busy + 0.5)), true
}

// cgroupQuotaCores rounds a fractional CPU quota up to whole cores, which is
// how many CPUs the workload can actually be running on at one instant.
func cgroupQuotaCores(quota float64, hostCores int) int {
	cores := int(math.Ceil(quota))
	if cores < 1 {
		cores = 1
	}
	if hostCores > 0 && cores > hostCores {
		// A quota above the host's core count is not a limit worth reporting.
		return hostCores
	}
	return cores
}
