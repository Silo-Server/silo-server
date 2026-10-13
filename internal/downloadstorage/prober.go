package downloadstorage

import (
	"sync"
	"time"
)

// DefaultInterval is how often a Prober re-reads an artifact directory. A read
// is one directory listing and one stat per file, cheap at the few thousand
// files a large deployment holds, but there is no reason to repeat it every
// few seconds: prepared files appear and disappear on the scale of minutes.
const DefaultInterval = 5 * time.Minute

// probeTimeout is how long a measurement may be outstanding before the last
// answer is reported stale. It does not cancel anything: a read parked on a
// dead network mount stays parked until the mount recovers.
const probeTimeout = 10 * time.Second

// Prober measures one artifact directory in the background and always answers
// from its last result, so a wedged mount never delays a stats push or an
// admin request. At most one measurement goroutine exists per Prober.
type Prober struct {
	interval time.Duration
	measure  func(dir, scratchDir string, now time.Time) Usage
	now      func() time.Time

	mu        sync.Mutex
	inFlight  bool
	startedAt time.Time
	key       string
	last      Usage
	have      bool
	// done, when set, receives after every finished measurement (tests).
	done chan struct{}
}

// NewProber returns a Prober that re-measures at most every interval
// (DefaultInterval when interval <= 0).
func NewProber(interval time.Duration) *Prober {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Prober{interval: interval, measure: Measure, now: time.Now}
}

// Current returns the latest measurement of dir and starts a new one when the
// last is older than the interval, or was taken of a different directory. The
// second result is false until a first measurement has finished.
func (p *Prober) Current(dir, scratchDir string) (Usage, bool) {
	now := p.now()
	key := dir + "\x00" + scratchDir
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retargetLocked(key)
	due := !p.have || now.Sub(p.last.MeasuredAt) >= p.interval
	if due && !p.inFlight {
		p.inFlight, p.startedAt = true, now
		go p.run(key, dir, scratchDir, now)
	}
	if !p.have {
		return Usage{}, false
	}
	out := p.last
	if out.Error != "" || (p.inFlight && now.Sub(p.startedAt) > probeTimeout) || now.Sub(out.MeasuredAt) > p.interval+probeTimeout {
		out.Stale = true
	}
	return out, true
}

// Refresh starts a measurement now unless one is already running, so an
// administrator who just freed space sees it without waiting an interval.
func (p *Prober) Refresh(dir, scratchDir string) {
	now := p.now()
	key := dir + "\x00" + scratchDir
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight {
		return
	}
	p.retargetLocked(key)
	p.inFlight, p.startedAt = true, now
	go p.run(key, dir, scratchDir, now)
}

// retargetLocked forgets the last answer when the directory changed (a
// setting edit): it describes a different place and must never be reported
// under the new path. Callers hold mu.
func (p *Prober) retargetLocked(key string) {
	if p.key != key {
		p.key, p.have, p.last = key, false, Usage{}
	}
}

func (p *Prober) run(key, dir, scratchDir string, now time.Time) {
	u := p.measure(dir, scratchDir, now)
	p.mu.Lock()
	p.inFlight = false
	if p.key == key {
		if u.Error != "" && p.have {
			// Keep the last good numbers, flagged by the error, rather than
			// replacing them with zeros that read as an empty directory.
			p.last.Error, p.last.MeasuredAt = u.Error, u.MeasuredAt
		} else {
			p.last, p.have = u, true
		}
	}
	done := p.done
	p.mu.Unlock()
	if done != nil {
		done <- struct{}{}
	}
}
