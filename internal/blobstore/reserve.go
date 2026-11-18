package blobstore

import (
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
)

// statSpace reports the bytes this process may still write to the filesystem
// holding path, as df's "available" column does, and the filesystem's size. A
// var so tests can fake it.
var statSpace = func(path string) (available, total int64, err error) {
	var st unix.Statfs_t
	if err = unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	size := uint64(st.Bsize)
	return int64(st.Bavail * size), int64(st.Blocks * size), nil
}

// reserveFor is the free space a store keeps on a filesystem of total bytes.
func reserveFor(total int64) int64 {
	return min(int64(maxFreeSpaceReserve), total/freeSpaceReserveDivisor)
}

// lowSpaceState records whether a store is refusing writes, so the warning is
// logged when that starts (and hourly while it lasts), and the recovery once
// space is back.
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
// limit bytes beyond base, the settled total when free space was read. A
// negative limit (a filesystem that reports no size) does not limit writes.
type spaceBudget struct {
	limit, reserve, base int64
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
	b := spaceBudget{limit: available - reserveFor(total), reserve: reserveFor(total), base: base}
	if b.limit-(f.space.claimed.Load()-base) <= 0 {
		return b, f.refuse(available, b.reserve)
	}
	if b.limit >= lowSpaceRecoveryMargin && f.space.low.CompareAndSwap(true, false) {
		slog.Info("local storage has free space again; resuming writes",
			"component", "blobstore", "path", f.root, "available_bytes", available, "reserve_bytes", b.reserve)
	}
	return b, nil
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
	if b.limit < 0 {
		return nil
	}
	for {
		claimed := f.space.claimed.Load()
		if used := claimed - b.base; used+n > b.limit {
			return f.refuse(b.limit+b.reserve-used, b.reserve)
		}
		if f.space.claimed.CompareAndSwap(claimed, claimed+n) {
			return nil
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
type budgetWriter struct {
	f      *Filesystem
	w      io.Writer
	budget spaceBudget
}

func (b *budgetWriter) Write(p []byte) (int, error) {
	n := int64(len(p))
	if err := b.f.claim(n, b.budget); err != nil {
		return 0, err
	}
	defer b.f.settle(n, b.budget)
	return b.w.Write(p)
}
