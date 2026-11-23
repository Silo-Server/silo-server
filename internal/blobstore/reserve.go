package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ErrStorageFull is in the chain when a local store refuses a write that would
// leave its filesystem with less than its free-space reserve. It wraps ENOSPC,
// so callers that already treat a full disk as a temporary storage failure
// handle it the same way.
var ErrStorageFull = fmt.Errorf("blobstore: local storage is below its free-space reserve: %w", syscall.ENOSPC)

const (
	// maxFreeSpaceReserve is the space a local store leaves free on its
	// filesystem. Artwork, previews, and chapter images usually share a disk
	// with PostgreSQL and the rest of the server's data, so generated files
	// stop here instead of filling the disk to the last byte, and the server
	// stays usable while an admin frees space.
	maxFreeSpaceReserve = 2 << 30
	// freeSpaceReserveDivisor caps the reserve at a tenth of a small
	// filesystem, so a volume given to artwork alone stays usable.
	freeSpaceReserveDivisor = 10

	// lowSpaceWarnInterval spaces the warning a store logs while it refuses
	// writes.
	lowSpaceWarnInterval = time.Hour
	// lowSpaceRecoveryMargin is how far above the reserve the available space
	// must climb before a store that refused writes reports it has room again.
	// Small writes still fit just above the reserve, and without the margin
	// each one would log a recovery between the refusals of larger ones.
	lowSpaceRecoveryMargin = 256 << 20
	// streamRecheckBytes is how much a stream writes between readings of
	// free space, so space another writer takes during a long stream (a
	// backup archive while PostgreSQL grows) counts against it before the
	// stream reaches the reserve.
	streamRecheckBytes = 32 << 20
)

type reserveExemptKey struct{}

// WithoutFreeSpaceReserve returns a context whose local writes may use the
// free-space reserve. Only a write that undoes another, such as a restore's
// rollback putting back the files it replaced, should use it: refusing it
// would leave the server worse off than finishing it, and the reserve is the
// room it needs.
func WithoutFreeSpaceReserve(ctx context.Context) context.Context {
	return context.WithValue(ctx, reserveExemptKey{}, true)
}

func reserveExempt(ctx context.Context) bool {
	exempt, _ := ctx.Value(reserveExemptKey{}).(bool)
	return exempt
}

// statSpace reports the bytes this process may still write to the filesystem
// holding path, as df's "available" column does, and the filesystem's size. A
// var so tests can fake it.
var statSpace = func(path string) (available, total int64, err error) {
	var st unix.Statfs_t
	if err = unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	size := blockSize(&st)
	return int64(st.Bavail) * size, int64(st.Blocks) * size, nil
}

// FreeSpace reports the bytes available on the filesystem holding a local
// store at root and the free-space reserve its writes stop at, for a
// preflight such as a backup's that should refuse up front rather than stop
// partway. ok is false when the filesystem reports no size, which the
// reserve does not limit.
func FreeSpace(root string) (available, reserve int64, ok bool, err error) {
	available, total, err := statSpace(root)
	if err != nil || total <= 0 {
		return 0, 0, false, err
	}
	return max(available, 0), reserveFor(total), true, nil
}

// reserveFor is the free space a store keeps on a filesystem of total bytes.
func reserveFor(total int64) int64 {
	return min(int64(maxFreeSpaceReserve), total/freeSpaceReserveDivisor)
}

// lowSpaceState records whether a store is refusing writes, so the warning is
// logged when that starts (and hourly while it lasts), and the recovery once a
// write succeeds with space to spare.
//
// claimed and settled share one budget among the writes in flight. claimed
// totals every byte a write has been allowed to add; settled totals those
// since written to disk, or abandoned. Both only grow. A statfs reading
// already counts the bytes settled before it, so a write remembers settled
// when it reads statfs and counts against its budget only what has been
// claimed beyond that: its own later bytes and every other write's. That
// keeps a write's reading from going stale while others write, without
// counting a byte twice.
type lowSpaceState struct {
	low      atomic.Bool
	lastWarn atomic.Int64
	claimed  atomic.Int64
	settled  atomic.Int64
}

// spaceBudget is what a write may add before it would cut into the reserve:
// limit bytes beyond base, the settled total when free space (available) was
// read. A negative limit (a filesystem that reports no size) does not limit
// writes.
type spaceBudget struct {
	limit, reserve, base, available int64
}

// writeBudget reads the space a write may use. It returns ErrStorageFull when
// nothing may be written.
func (f *Filesystem) writeBudget() (spaceBudget, error) {
	// Read settled first: a byte settled between the two reads is then
	// counted twice, which errs towards refusing.
	base := f.space.settled.Load()
	available, total, statErr := statSpace(f.root)
	if statErr != nil || total <= 0 {
		// Some network and FUSE filesystems report no sizes; the write
		// itself still fails with ENOSPC when they are full.
		return spaceBudget{limit: -1}, nil //nolint:nilerr // an unknown size does not limit writes
	}
	b := spaceBudget{limit: available - reserveFor(total), reserve: reserveFor(total), base: base, available: available}
	if b.limit-(f.space.claimed.Load()-base) <= 0 {
		return b, f.refuse(available, b.reserve)
	}
	return b, nil
}

// noteWritten logs that a store which refused writes has room again, once a
// write succeeds and a fresh reading shows lowSpaceRecoveryMargin above the
// reserve. Waiting for a write to succeed keeps a filesystem out of inodes,
// which still shows free bytes, from logging a recovery before every failed
// attempt; reading again keeps a write that used up the room from logging one.
func (f *Filesystem) noteWritten() {
	if !f.space.low.Load() {
		return
	}
	// Read settled first, as writeBudget does: a byte settled between the
	// two reads is then counted twice, which keeps the low state longer.
	base := f.space.settled.Load()
	available, total, err := statSpace(f.root)
	if err != nil {
		// No reading confirms the room; a later write checks again.
		return
	}
	// A filesystem that reports no size can never show the margin, so a
	// successful write is all the evidence there is.
	if total > 0 && available-reserveFor(total)-(f.space.claimed.Load()-base) < lowSpaceRecoveryMargin {
		return
	}
	if f.space.low.CompareAndSwap(true, false) {
		slog.Info("local storage has free space again; resuming writes",
			"component", "blobstore", "path", f.root, "available_bytes", available, "reserve_bytes", reserveFor(total))
	}
}

// refuse logs that the store is pausing writes and returns ErrStorageFull.
func (f *Filesystem) refuse(available, reserve int64) error {
	f.warnFull(available, reserve)
	return fmt.Errorf("%w (%d bytes available, %d reserved)", ErrStorageFull, max(available, 0), reserve)
}

// warnFull logs that writes are failing for lack of space: when that starts,
// then hourly while it lasts.
func (f *Filesystem) warnFull(available, reserve int64) {
	now := time.Now().UnixNano()
	started := f.space.low.CompareAndSwap(false, true)
	last := f.space.lastWarn.Load()
	if (started || now-last >= int64(lowSpaceWarnInterval)) && f.space.lastWarn.CompareAndSwap(last, now) {
		slog.Warn("local storage is almost full; refusing new artwork, previews, chapter images, and other stored files until space is freed",
			"component", "blobstore", "path", f.root, "available_bytes", available, "reserve_bytes", reserve)
	}
}

// noteWriteError logs a full filesystem that the reserve did not catch, such
// as one out of inodes or filled by another process between the check and
// the write.
func (f *Filesystem) noteWriteError(err error) {
	if !errors.Is(err, syscall.ENOSPC) || errors.Is(err, ErrStorageFull) {
		return
	}
	available, total, statErr := statSpace(f.root)
	if statErr != nil {
		available, total = -1, 0
	}
	f.warnFull(available, reserveFor(total))
}

// claim lets a write add n more bytes, or returns ErrStorageFull when the
// writes in flight would together cut into the reserve. The caller settles
// the bytes once they are written or abandoned. A refused claim takes nothing,
// so it never holds back another write.
func (f *Filesystem) claim(n int64, b spaceBudget) error {
	if used, ok := f.tryClaim(n, b); !ok {
		return f.refuse(b.available-used, b.reserve)
	}
	return nil
}

// tryClaim is claim without the refusal: it reports whether it took the
// bytes, and otherwise how much of the budget other claims use.
func (f *Filesystem) tryClaim(n int64, b spaceBudget) (used int64, ok bool) {
	if b.limit < 0 {
		return 0, true
	}
	for {
		claimed := f.space.claimed.Load()
		if used = claimed - b.base; used+n > b.limit {
			return used, false
		}
		if f.space.claimed.CompareAndSwap(claimed, claimed+n) {
			return used, true
		}
	}
}

// settle records that n claimed bytes are written to disk or abandoned.
func (f *Filesystem) settle(n int64, b spaceBudget) {
	if b.limit >= 0 && n != 0 {
		f.space.settled.Add(n)
	}
}

// budgetWriter claims budget for each chunk of a stream before writing it and
// settles it after, so a stream stops at the reserve. The temporary file is
// discarded on error, so nothing partial is published.
//
// A long stream's free-space reading ages: other writes that failed and
// removed their files, or space freed outside the store, still count against
// it. So when a chunk does not fit, the stream reads free space again and
// retries once before giving up.
//
// Space taken by writers outside this store (PostgreSQL, another process or
// server) only shows in a fresh reading, so a stream also reads free space
// again after every streamRecheckBytes it writes.
type budgetWriter struct {
	f      *Filesystem
	w      io.Writer
	budget spaceBudget
	// sinceRead is how many bytes the stream has written since budget was
	// read.
	sinceRead int64
	// unchecked passes writes straight through: a write of known size,
	// which claimed its bytes before it started, or one exempt from the
	// reserve.
	unchecked bool
}

func (b *budgetWriter) Write(p []byte) (int, error) {
	if b.unchecked {
		return b.w.Write(p)
	}
	n := int64(len(p))
	// Also when the last reading was unavailable, so a passing statfs error
	// does not leave the rest of the stream unlimited.
	if b.sinceRead >= streamRecheckBytes {
		if err := b.refresh(); err != nil {
			return 0, err
		}
	}
	if _, ok := b.f.tryClaim(n, b.budget); !ok {
		if err := b.refresh(); err != nil {
			return 0, err
		}
		if err := b.f.claim(n, b.budget); err != nil {
			return 0, err
		}
	}
	b.sinceRead += n
	defer b.f.settle(n, b.budget)
	return b.w.Write(p)
}

// refresh reads free space again for the rest of the stream. A reading that
// fails keeps the last one, so a passing statfs error does not leave the
// rest of a limited stream unlimited.
func (b *budgetWriter) refresh() error {
	fresh, err := b.f.writeBudget()
	if err != nil {
		return err
	}
	if fresh.limit >= 0 || b.budget.limit < 0 {
		b.budget = fresh
	}
	b.sinceRead = 0
	return nil
}
