package nodemetrics

import (
	"log/slog"
	"strconv"
	"time"
)

// maxSampledDisks caps how many mounts a snapshot reports. A deployment can
// have dozens of library roots, and a health response that grows with the
// library count would eventually be the reason health requests are slow.
const maxSampledDisks = 8

// diskProbeTimeout is how long a probe may be outstanding before the entry it
// belongs to is reported stale. It is not a cancellation: statfs(2) on a dead
// NFS server is uninterruptible, so the goroutine stays parked until the mount
// recovers or the process exits. What the timeout bounds is how long a reader
// is told numbers are current.
const diskProbeTimeout = 5 * time.Second

// fsStats is one filesystem's capacity, in the portable shape this package
// needs from statfs(2).
type fsStats struct {
	UsedBytes  uint64
	TotalBytes uint64
	// FSID identifies the filesystem itself, so two paths on one volume — the
	// common case where scratch and media live on the same disk — are reported
	// once instead of twice with identical numbers. It is empty when the
	// filesystem publishes no usable id (FUSE mounts do not), in which case each
	// path is reported separately rather than collapsed onto a shared non-id.
	FSID string
}

// fsCapacity converts raw statfs(2) block counts into the used/total shape this
// package reports. It is separate from the syscall so the arithmetic — which is
// where the reserved-block subtlety lives — can be exercised directly.
//
// A filesystem can only report more free blocks than it holds if the numbers
// are nonsense, so the subtraction is guarded rather than allowed to wrap an
// unsigned counter into a petabyte.
func fsCapacity(blocks, free, available, blockSize uint64) fsStats {
	if free > blocks {
		free = blocks
	}
	used := (blocks - free) * blockSize
	return fsStats{UsedBytes: used, TotalBytes: used + available*blockSize}
}

// diskEntry is one path's probe state. Probes run detached from the sample
// loop, so this holds the last good answer for readers to fall back to.
type diskEntry struct {
	path string
	// inFlight is what keeps a permanently stuck mount from accumulating one
	// parked goroutine per sample. A path already being probed is skipped
	// entirely; there is never more than one goroutine per path.
	inFlight    bool
	startedAt   time.Time
	haveGood    bool
	good        fsStats
	goodAt      time.Time
	lastErr     bool
	unreachable bool
}

// stale reports whether this entry's last good numbers should be flagged as
// carried over. Either the current probe has outlived its budget — the wedged
// network mount case — or no probe has landed for longer than one full sampling
// cycle plus that budget.
func (e *diskEntry) stale(now time.Time, interval time.Duration) bool {
	if e.lastErr {
		return true
	}
	if e.inFlight && now.Sub(e.startedAt) > diskProbeTimeout {
		return true
	}
	return now.Sub(e.goodAt) > interval+diskProbeTimeout
}

// maxOutstandingDiskProbes is the ceiling on statfs goroutines this sampler may
// have parked at once, across every path it has ever been asked about.
//
// Bounding the paths offered per sample is not enough on its own. A probe stuck
// on a dead mount is kept — dropping its entry would only let the next sample
// start a second goroutine against the same mount — so a deployment whose
// library roots churn while mounts are wedged would retire one set of parked
// goroutines' paths and immediately be free to park a fresh set for the
// replacements. Repeat that and the count grows without limit. This ceiling is
// what makes it a fixed cost instead: once it is reached no new probe starts,
// which is the correct backpressure, since a sampler with this many mounts
// wedged has nothing useful left to measure anyway.
const maxOutstandingDiskProbes = maxSampledDisks

// refreshDisks starts a probe for every path that is not already being probed
// and returns immediately. It never waits for a result: the caller is the
// sample loop, and the whole point of this package is that one bad mount cannot
// delay a node's health answer.
func (s *Sampler) refreshDisks(paths []string, now time.Time) {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()

	wanted := make(map[string]bool, len(paths))
	for _, path := range paths {
		if path != "" {
			wanted[path] = true
		}
	}
	s.pruneDisksLocked(wanted)

	seen := make(map[string]bool, len(paths))
	candidates := make([]*diskEntry, 0, len(paths))
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		entry := s.disks[path]
		if entry == nil {
			entry = &diskEntry{path: path}
			s.disks[path] = entry
			// Reporting order is the order the caller offered, whatever order
			// the probes below are started in.
			s.diskOrder = append(s.diskOrder, path)
		}
		candidates = append(candidates, entry)
	}

	for _, entry := range s.probeOrderLocked(candidates) {
		if entry.inFlight {
			continue
		}
		if s.probesInFlight >= maxOutstandingDiskProbes {
			s.noteProbeBudgetExhaustedLocked()
			break
		}
		entry.inFlight = true
		entry.startedAt = now
		s.probesInFlight++
		go s.probeDisk(entry)
	}
}

// probeOrderLocked is the order this sample offers entries to the probe budget.
//
// Scratch stays first: it is the mount transcode admission reads, so it is the
// one that must get a freed slot. The rest rotate, and that is not fairness for
// its own sake. The budget is a global ceiling, and a probe parked on a wedged
// mount holds its slot until the mount recovers or the process exits — so a
// deployment with one dead mount permanently runs one slot short. Offering the
// same list in the same order every sample would then spend the whole remaining
// budget on the same prefix and never reach the last path at all: it would be
// reported unavailable indefinitely, which is a lie about a disk that is fine
// and would have answered instantly. Advancing the start by one each sample
// costs a path its refresh roughly once per cycle and guarantees every mount is
// measured.
//
// Callers must hold diskMu.
func (s *Sampler) probeOrderLocked(candidates []*diskEntry) []*diskEntry {
	rotatable := candidates
	ordered := make([]*diskEntry, 0, len(candidates))
	if s.scratchDir != "" && len(candidates) > 0 && candidates[0].path == s.scratchDir {
		ordered = append(ordered, candidates[0])
		rotatable = candidates[1:]
	}
	if len(rotatable) == 0 {
		return ordered
	}
	start := s.diskProbeCursor % len(rotatable)
	s.diskProbeCursor = (start + 1) % len(rotatable)
	for i := range rotatable {
		ordered = append(ordered, rotatable[(start+i)%len(rotatable)])
	}
	return ordered
}

// noteProbeBudgetExhaustedLocked logs the first sample in which the probe
// ceiling stopped new work, and nothing further until it clears. Every entry it
// skips keeps reporting its last good numbers marked stale, so without a line
// here the state reads as mounts that merely went quiet.
// Callers must hold diskMu.
func (s *Sampler) noteProbeBudgetExhaustedLocked() {
	if s.probeBudgetExhausted {
		return
	}
	s.probeBudgetExhausted = true
	slog.Warn("node metrics disk probes are at their ceiling; mounts are not being re-measured",
		"component", "nodemetrics", "outstanding", s.probesInFlight, "limit", maxOutstandingDiskProbes)
}

// pruneDisksLocked forgets paths no longer offered, so a server whose libraries
// churn over months does not accumulate an entry per path ever configured.
// An entry with a probe still parked is kept: dropping it would let the next
// sample start a second goroutine against the same wedged mount, which is
// exactly what the in-flight guard exists to prevent.
// Callers must hold diskMu.
func (s *Sampler) pruneDisksLocked(wanted map[string]bool) {
	kept := s.diskOrder[:0]
	for _, path := range s.diskOrder {
		entry := s.disks[path]
		if wanted[path] || (entry != nil && entry.inFlight) {
			kept = append(kept, path)
			continue
		}
		delete(s.disks, path)
	}
	s.diskOrder = kept
}

// probeDisk runs one statfs and records the outcome. It runs on its own
// goroutine and may never return; that is expected and is why nothing waits on
// it.
func (s *Sampler) probeDisk(entry *diskEntry) {
	stats, err := s.statfs(entry.path)

	s.diskMu.Lock()
	entry.inFlight = false
	if s.probesInFlight > 0 {
		s.probesInFlight--
	}
	if s.probeBudgetExhausted && s.probesInFlight < maxOutstandingDiskProbes {
		s.probeBudgetExhausted = false
		slog.Info("node metrics disk probes are below their ceiling again", "component", "nodemetrics")
	}
	if err != nil {
		entry.lastErr = true
		// A path that has never been measured and just failed is not a mount
		// this node can see at all — a media root that exists on another node,
		// or a scratch dir that has not been created yet.
		entry.unreachable = !entry.haveGood
	} else {
		entry.lastErr = false
		entry.unreachable = false
		entry.haveGood = true
		entry.good = stats
		entry.goodAt = s.now()
	}
	s.diskMu.Unlock()

	if s.diskProbeDone != nil {
		s.diskProbeDone <- entry.path
	}
}

// diskStats reports the latest known state of every tracked path, in the order
// the paths were first offered — scratch dir first, since that is the volume a
// full disk breaks first — deduplicated by filesystem and capped.
func (s *Sampler) diskStats(paths []string, now time.Time) []DiskStats {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()

	wanted := make(map[string]bool, len(paths))
	for _, path := range paths {
		wanted[path] = true
	}

	out := make([]DiskStats, 0, min(len(s.diskOrder), maxSampledDisks))
	seenFS := make(map[string]bool, len(s.diskOrder))
	libraries := 0
	for _, path := range s.diskOrder {
		if !wanted[path] {
			continue
		}
		entry := s.disks[path]
		if entry == nil {
			continue
		}
		scratch := s.scratchDir != "" && path == s.scratchDir
		// The role is assigned before the measurability check, so the index
		// belongs to the mount rather than to its luck this pass. Numbering
		// only the measurable ones would slide every library root up a place
		// the moment one went unavailable, and a Prometheus alert keyed on
		// library-1 would silently follow a different volume.
		role := ScratchDiskRole
		if !scratch {
			libraries++
			role = "library-" + strconv.Itoa(libraries)
		}
		if !entry.haveGood {
			// Report it rather than hiding it: a media root this node cannot
			// see is a deployment fact an operator needs, and silently dropping
			// it looks identical to the path not being configured.
			out = append(out, DiskStats{Path: path, Role: role, Unavailable: true, Scratch: scratch})
		} else {
			if entry.good.FSID != "" {
				if seenFS[entry.good.FSID] {
					continue
				}
				seenFS[entry.good.FSID] = true
			}
			out = append(out, DiskStats{
				Path:    path,
				Role:    role,
				UsedGB:  bytesToGB(entry.good.UsedBytes),
				TotalGB: bytesToGB(entry.good.TotalBytes),
				Stale:   entry.stale(now, s.interval),
				Scratch: scratch,
			})
		}
		// The cap applies to every entry, measured or not. A host whose library
		// roots all live on other nodes produces nothing but unavailable
		// entries, and those grow with the library count just as measured ones
		// would.
		if len(out) >= maxSampledDisks {
			break
		}
	}
	return out
}

// formatFSID renders a statfs f_fsid, or "" when the filesystem published none.
//
// A zero f_fsid is not an identity: the FUSE protocol has no fsid field at all,
// so every rclone, mergerfs and s3fs mount reports zero — and those are exactly
// the mounts a media server uses as library roots. Formatting that as "0:0"
// would make two unrelated mounts look like one filesystem, and the second one
// would be silently dropped from the disk panel, from Prometheus, and from the
// fullest-mount warning. A mount at 98% nobody can see is worse than a
// duplicated row.
func formatFSID(a, b int64) string {
	if a == 0 && b == 0 {
		return ""
	}
	return strconv.FormatInt(a, 16) + ":" + strconv.FormatInt(b, 16)
}

// bytesToGB converts to gibibytes with two decimals kept, which is the
// precision a capacity readout is read at.
func bytesToGB(value uint64) float64 {
	const bytesPerGB = float64(1024 * 1024 * 1024)
	gb := float64(value) / bytesPerGB
	return float64(int64(gb*100+0.5)) / 100
}
