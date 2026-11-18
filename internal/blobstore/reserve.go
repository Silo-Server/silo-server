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
type lowSpaceState struct {
	low      atomic.Bool
	lastWarn atomic.Int64
}

// writeBudget returns how many bytes a write may add before it would cut into
// the reserve, and the reserve. It returns ErrStorageFull when nothing may be
// written. A filesystem that cannot report its size is not limited: the
// budget is -1.
func (f *Filesystem) writeBudget() (budget, reserve int64, err error) {
	available, total, statErr := statSpace(f.root)
	if statErr != nil || total <= 0 {
		// Some network and FUSE filesystems report no sizes; the write
		// itself still fails with ENOSPC when they are full.
		return -1, 0, nil //nolint:nilerr // an unknown size does not limit writes
	}
	reserve = reserveFor(total)
	budget = available - reserve
	if budget <= 0 {
		return 0, reserve, f.refuse(available, reserve)
	}
	if budget >= lowSpaceRecoveryMargin && f.space.low.CompareAndSwap(true, false) {
		slog.Info("local storage has free space again; resuming writes",
			"component", "blobstore", "path", f.root, "available_bytes", available, "reserve_bytes", reserve)
	}
	return budget, reserve, nil
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

// budgetWriter fails a write that would take the filesystem into the reserve.
// The temporary file is discarded on error, so nothing partial is published.
type budgetWriter struct {
	f       *Filesystem
	w       io.Writer
	budget  int64
	reserve int64
}

func (b *budgetWriter) Write(p []byte) (int, error) {
	if b.budget >= 0 {
		if int64(len(p)) > b.budget {
			return 0, b.f.refuse(b.budget+b.reserve, b.reserve)
		}
		b.budget -= int64(len(p))
	}
	return b.w.Write(p)
}
