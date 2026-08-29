package nodemetrics

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// nvidiaSMITimeout bounds one query. A wedged driver makes nvidia-smi hang
// indefinitely, and a metrics sample must never inherit that.
const nvidiaSMITimeout = 3 * time.Second

// sourceFailureLimit is how many consecutive failures retire an enrichment
// source for the life of the process.
//
// A source that has failed five times running is not having a bad moment: the
// binary is missing its driver, the container lacks the device, or the query
// syntax is not supported by the installed version. Retrying it forever would
// spawn a doomed subprocess every 5 seconds on every node, which costs more
// than the signal is worth. Recovery is a node restart, which is also what
// installing or fixing the toolkit requires.
const sourceFailureLimit = 5

// nvidiaSMIFields is the query column order. utilization.gpu is whole-GPU
// busyness including other tenants; the encoder/decoder columns are the
// fixed-function video engines, which is what a transcode node is actually
// competing for.
const nvidiaSMIFields = "index,uuid,pci.bus_id,utilization.gpu,utilization.encoder,utilization.decoder,memory.used,memory.total"

// runNVIDIASMI is the execution seam. Tests replace it instead of installing a
// fake binary on PATH.
var runNVIDIASMI = func(ctx context.Context) ([]byte, error) {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, path,
		"--query-gpu="+nvidiaSMIFields,
		"--format=csv,noheader,nounits").Output()
}

// nvidiaGPU is one parsed nvidia-smi row.
//
// The measurement columns are optional because a successful row can still be
// only partly measurable: a driver reports "[N/A]" or "[Not Supported]" per
// column for engines or memory it cannot see, and coercing those to zero would
// publish an unobservable video engine as idle and unsupported VRAM as 0 bytes
// under an "nvidia-smi" source that claims they were measured.
type nvidiaGPU struct {
	Index       int
	UUID        string
	PCIAddress  string
	GPUUtil     *int
	EncoderUtil *int
	DecoderUtil *int
	MemUsedMB   *int64
	MemTotalMB  *int64
}

// videoUtil is the higher of the two fixed-function video engines, or nil when
// the driver reported neither.
func (g nvidiaGPU) videoUtil() *int {
	switch {
	case g.EncoderUtil != nil && g.DecoderUtil != nil:
		return ptr(max(*g.EncoderUtil, *g.DecoderUtil))
	case g.EncoderUtil != nil:
		return g.EncoderUtil
	default:
		return g.DecoderUtil
	}
}

func ptr[T any](value T) *T { return &value }

// sourceRetryInterval is how long a retired source waits before one
// probationary query.
//
// The breaker exists so a host without the NVIDIA toolkit stops spawning a
// doomed subprocess every five seconds, and at this interval it still does:
// one exec per ten minutes instead of a hundred and twenty. What it must not do
// is confuse "this host has no toolkit" with "this driver is resetting", which
// look identical for the handful of samples the limit counts. Retiring the
// source until the process restarts turns a recoverable outage into a node that
// reports no GPU utilization or VRAM for as long as it stays up.
const sourceRetryInterval = 10 * time.Minute

// sourceBreaker retires an enrichment source after repeated failure, and lets
// it back in on probation.
type sourceBreaker struct {
	// mu guards the fields below. The sampling goroutine is their only regular
	// writer, but reset comes from whatever goroutine serves a re-probe.
	mu       sync.Mutex
	name     string
	failures int
	tripped  bool
	// retryAt is when a tripped source may next be tried. It advances on every
	// attempt, so one probationary query runs per interval whether or not it
	// succeeds.
	retryAt time.Time
	logOnce sync.Once
}

// allow reports whether the source may be queried, admitting one probationary
// query per sourceRetryInterval once the breaker has tripped.
func (b *sourceBreaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.tripped {
		return true
	}
	if now.Before(b.retryAt) {
		return false
	}
	b.retryAt = now.Add(sourceRetryInterval)
	return true
}

// succeeded clears the failure count, and closes the breaker when the query
// that succeeded was a probationary one.
func (b *sourceBreaker) succeeded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	if b.tripped {
		b.tripped = false
		slog.Info("node metrics source answered again", "component", "nodemetrics", "source", b.name)
	}
}

// failed records one failure and trips the breaker at the limit, logging the
// retirement exactly once so a node without the toolkit does not narrate it
// every interval. A probationary query that fails costs nothing further: allow
// has already pushed the next attempt out by a full interval.
func (b *sourceBreaker) failed(now time.Time, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tripped {
		return
	}
	b.failures++
	if b.failures < sourceFailureLimit {
		return
	}
	b.tripped = true
	b.retryAt = now.Add(sourceRetryInterval)
	b.logOnce.Do(func() {
		slog.Info("node metrics source unavailable; retrying occasionally",
			"component", "nodemetrics", "source", b.name, "failures", b.failures,
			"retry_interval", sourceRetryInterval, "error", err)
	})
}

// reset returns the source to service immediately.
//
// It is what an operator's hardware re-probe calls: a re-probe is the explicit
// statement that something changed underneath this node, which is exactly the
// event the retry interval is otherwise waiting to discover on its own.
func (b *sourceBreaker) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.tripped = false
	b.retryAt = time.Time{}
}

// queryNVIDIA runs one bounded nvidia-smi query, honoring the breaker.
func (s *Sampler) queryNVIDIA(ctx context.Context) []nvidiaGPU {
	now := s.now()
	if !s.nvidiaBreaker.allow(now) {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, nvidiaSMITimeout)
	defer cancel()
	output, err := s.runNVIDIASMI(queryCtx)
	if err != nil {
		s.nvidiaBreaker.failed(now, err)
		return nil
	}
	gpus := parseNVIDIASMI(output)
	if len(gpus) == 0 {
		// A successful command that says nothing is as useless as a failure and
		// is how a stale query syntax presents.
		s.nvidiaBreaker.failed(now, errNoNVIDIARows)
		return nil
	}
	s.nvidiaBreaker.succeeded()
	return gpus
}

// RetrySources returns every retired enrichment source to service.
//
// A node's hardware re-probe calls it: the operator is saying that something
// changed underneath this process, which is the same event the breaker's retry
// interval would otherwise take up to sourceRetryInterval to notice on its own.
// A driver reinstalled or a toolkit added should show in the next sample, not in
// ten minutes.
func (s *Sampler) RetrySources() {
	if s == nil {
		return
	}
	s.nvidiaBreaker.reset()
}

// errNoNVIDIARows marks a query that succeeded but produced nothing parseable.
var errNoNVIDIARows = errors.New("nvidia-smi returned no parseable rows")

// parseNVIDIASMI reads "csv,noheader,nounits" rows. Malformed rows are skipped
// individually; a driver that reports "[N/A]" for one column on one GPU must
// not cost the reading for the others.
func parseNVIDIASMI(output []byte) []nvidiaGPU {
	var gpus []nvidiaGPU
	for line := range strings.Lines(string(output)) {
		fields := strings.Split(line, ",")
		if len(fields) < 8 {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			continue
		}
		address := NormalizePCIAddress(fields[2])
		uuid := strings.TrimSpace(fields[1])
		if address == "" && uuid == "" {
			continue
		}
		gpus = append(gpus, nvidiaGPU{
			Index:       index,
			UUID:        uuid,
			PCIAddress:  address,
			GPUUtil:     parseNVIDIAInt(fields[3]),
			EncoderUtil: parseNVIDIAInt(fields[4]),
			DecoderUtil: parseNVIDIAInt(fields[5]),
			MemUsedMB:   parseNVIDIAInt64(fields[6]),
			MemTotalMB:  parseNVIDIAInt64(fields[7]),
		})
	}
	return gpus
}

// parseNVIDIAInt reads one numeric column, or nil for the driver's "[N/A]" and
// "[Not Supported]" placeholders — which say the value was not measured, not
// that it is zero.
func parseNVIDIAInt(field string) *int {
	value, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

func parseNVIDIAInt64(field string) *int64 {
	value := parseNVIDIAInt(field)
	if value == nil {
		return nil
	}
	return ptr(int64(*value))
}
