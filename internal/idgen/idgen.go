// Package idgen provides unique ID generation for Silo entities.
// All content IDs (media items, seasons, episodes) are generated locally
// using Sonyflake — a distributed unique ID generator that produces
// time-sorted 64-bit integers encoded as decimal strings.
//
// Two processes generate the same ID only if they share a Sonyflake machine
// ID, so each process leases its machine ID from PostgreSQL with [Start]
// before it generates any.
package idgen

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sony/sonyflake/v2"
)

// epoch is the Sonyflake epoch: 2024-01-01 UTC.
// Sonyflake measures elapsed time from this point.
var epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// ErrNotStarted reports a process that generates IDs without first leasing a
// machine ID with [Start].
var ErrNotStarted = errors.New("idgen: no machine ID leased; call idgen.Start at startup")

// ErrLeaseExpired reports a process that could not renew its machine ID lease
// in time. Another process may claim the machine ID after the lease lapses, so
// NextID stops issuing IDs until a renewal succeeds.
var ErrLeaseExpired = errors.New("idgen: machine ID lease expired")

// generator is a Sonyflake instance and the machine ID it was built with.
type generator struct {
	sf        *sonyflake.Sonyflake
	machineID int
	// validUntil is the moment past which this process may no longer hold
	// machineID. Nil means the generator does not expire.
	validUntil atomic.Pointer[instant]
}

// instant is a moment on two clocks. A lease deadline is checked on both and
// has passed once either clock reaches it: a wall-clock step cannot extend a
// lease past its monotonic deadline, and a host suspension, which on Linux
// stops the monotonic clock, cannot extend it past its wall-clock deadline.
type instant struct {
	mono int64 // nanoseconds since clockBase on the monotonic clock
	wall int64 // Unix nanoseconds on the wall clock
}

var clockBase = time.Now()

func currentInstant() instant {
	now := time.Now()
	return instant{mono: int64(now.Sub(clockBase)), wall: now.UnixNano()}
}

func (g *generator) expired() bool {
	until := g.validUntil.Load()
	if until == nil {
		return false
	}
	now := currentInstant()
	return now.mono >= until.mono || now.wall >= until.wall
}

// active is the generator NextID uses. [Start] installs it.
var active atomic.Pointer[generator]

// testGenerator serves NextID in test binaries that never call Start. Test
// databases are disposable, so a random machine ID is enough there.
var testGenerator = sync.OnceValues(func() (*generator, error) {
	return newGenerator(rand.IntN(1 << 16))
})

func newGenerator(machineID int) (*generator, error) {
	sf, err := newSonyflake(sonyflake.Settings{
		StartTime: epoch,
		MachineID: func() (int, error) { return machineID, nil },
	})
	if err != nil {
		return nil, err
	}
	return &generator{sf: sf, machineID: machineID}, nil
}

// newSonyflake creates a generator from st and explains a clock set before the
// ID epoch, which otherwise surfaces as a bare "start time is ahead" error.
func newSonyflake(st sonyflake.Settings) (*sonyflake.Sonyflake, error) {
	gen, err := sonyflake.New(st)
	if errors.Is(err, sonyflake.ErrStartTimeAhead) {
		return nil, fmt.Errorf("system clock %s is before the ID epoch %s: %w",
			time.Now().UTC().Format(time.RFC3339), st.StartTime.UTC().Format(time.RFC3339), err)
	}
	return gen, err
}

// NextID returns a new unique Sonyflake ID as a decimal string.
func NextID() (string, error) {
	g := active.Load()
	if g == nil {
		if !testing.Testing() {
			return "", ErrNotStarted
		}
		var err error
		if g, err = testGenerator(); err != nil {
			return "", fmt.Errorf("idgen: %w", err)
		}
	}
	if g.expired() {
		return "", fmt.Errorf("%w (machine ID %d)", ErrLeaseExpired, g.machineID)
	}
	id, err := g.sf.NextID()
	if err != nil {
		return "", fmt.Errorf("idgen: %w", err)
	}
	return strconv.FormatInt(id, 10), nil
}
