// Package passwordhash runs every bcrypt hash and comparison in the server,
// for account passwords and profile PINs alike, under one per-process limit
// on how many run at once.
//
// A bcrypt operation holds a CPU core for tens of milliseconds, and an
// anonymous sign-in costs one even for a name with no account. Unbounded, a
// burst of sign-ins puts one in flight per request and takes every core away
// from transcodes and the rest of the API. Here at most half the cores hash;
// a few more operations queue briefly for a turn, and the rest are refused
// with ErrBusy, which callers answer as "try again", never as a wrong
// password.
package passwordhash

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ErrBusy means nothing was hashed or compared: every slot was taken and the
// caller's queue was full, no turn came within the wait limit, or the request
// ended first.
var ErrBusy = errors.New("password hashing is at capacity")

// RetryAfterSeconds is the Retry-After a busy answer carries. The queue
// drains within a few hash times, so a retry after a burst ends gets in.
const RetryAfterSeconds = 1

var (
	// slotLimit is how many operations may run at once: half the processors
	// Go schedules on, at least one. GOMAXPROCS follows a container's CPU
	// limit, so it is read at each admission.
	slotLimit = func() int { return max(1, runtime.GOMAXPROCS(0)/2) }
	// waitersPerSlot bounds each queue, and with them how long a turn takes.
	waitersPerSlot = 4
	// maxWait gives up on a turn that does not come, for example while
	// hashes run slowly on a starved machine.
	maxWait = 2 * time.Second
)

var (
	mu       sync.Mutex
	inFlight int
	ahead    []chan struct{} // AdmitAhead waiters in arrival order
	queue    []chan struct{} // Admit waiters in arrival order
	// lastAhead says the last turn handed to a waiter went to ahead: while
	// both lists wait, turns alternate between them.
	lastAhead bool
)

// Permit is a held slot. Release it when done; a permit belongs to one
// goroutine.
type Permit struct{ released bool }

// Admit waits for a slot. Its only error is ErrBusy.
func Admit(ctx context.Context) (*Permit, error) {
	return admit(ctx, false)
}

// AdmitAhead waits for a slot in a queue of its own, which skips the Admit
// queue and takes every other turn while both wait. It is for a check whose
// attempt is already counted, a profile PIN: a burst of sign-ins then
// neither spends a household's PIN attempts on busy answers nor keeps it
// from switching profiles, and PIN checks cannot starve sign-ins either. Its
// only error is ErrBusy.
func AdmitAhead(ctx context.Context) (*Permit, error) {
	return admit(ctx, true)
}

func admit(ctx context.Context, first bool) (*Permit, error) {
	if ctx.Err() != nil {
		return nil, busy(ctx)
	}
	mu.Lock()
	limit := slotLimit()
	if inFlight < limit && len(ahead) == 0 && (first || len(queue) == 0) {
		inFlight++
		mu.Unlock()
		return &Permit{}, nil
	}
	waiters := &queue
	if first {
		waiters = &ahead
	}
	if len(*waiters) >= limit*waitersPerSlot {
		mu.Unlock()
		return nil, ErrBusy
	}
	turn := make(chan struct{})
	*waiters = append(*waiters, turn)
	mu.Unlock()

	wait := time.NewTimer(maxWait)
	defer wait.Stop()
	var err error
	select {
	case <-turn:
		if ctx.Err() == nil {
			return &Permit{}, nil
		}
		err = busy(ctx)
	case <-ctx.Done():
		err = busy(ctx)
	case <-wait.C:
		err = ErrBusy
	}
	mu.Lock()
	i := slices.Index(*waiters, turn)
	if i >= 0 {
		*waiters = slices.Delete(*waiters, i, i+1)
	}
	mu.Unlock()
	if i < 0 {
		// The turn came anyway: pass it on.
		release()
	}
	return nil, err
}

// Release frees the slot. Releasing twice is a no-op.
func (p *Permit) Release() {
	if p.released {
		return
	}
	p.released = true
	release()
}

// Matches reports whether password matches hash, comparing in the held
// slot. The error is a hash bcrypt cannot read.
func (p *Permit) Matches(hash, password []byte) (bool, error) {
	err := bcrypt.CompareHashAndPassword(hash, password)
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return false, nil
	}
	return err == nil, err
}

// Compare checks password against hash once a slot is free. A wrong password
// is bcrypt.ErrMismatchedHashAndPassword; one never checked is ErrBusy.
func Compare(ctx context.Context, hash, password []byte) error {
	p, err := Admit(ctx)
	if err != nil {
		return err
	}
	defer p.Release()
	return bcrypt.CompareHashAndPassword(hash, password)
}

// Generate hashes password at cost once a slot is free.
func Generate(ctx context.Context, password []byte, cost int) ([]byte, error) {
	p, err := Admit(ctx)
	if err != nil {
		return nil, err
	}
	defer p.Release()
	return bcrypt.GenerateFromPassword(password, cost)
}

func release() {
	mu.Lock()
	defer mu.Unlock()
	inFlight--
	for inFlight < slotLimit() && len(ahead)+len(queue) > 0 {
		next := &queue
		if len(ahead) > 0 && (len(queue) == 0 || !lastAhead) {
			next = &ahead
		}
		lastAhead = next == &ahead
		inFlight++
		close((*next)[0])
		*next = slices.Delete(*next, 0, 1)
	}
}

func busy(ctx context.Context) error {
	return fmt.Errorf("%w: %w", ErrBusy, context.Cause(ctx))
}
