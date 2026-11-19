package scanner

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
)

type audiobookTagRereadKey struct{}

// audiobookTagReread is the mark WithAudiobookTagReread or
// WithAudiobookLibraryTagReread puts on a scan: the book to read again, or
// every book, and what a scan under the mark read.
type audiobookTagReread struct {
	contentID string
	all       bool
	done      atomic.Bool
	read      atomic.Int64
	// reset, when set, runs inside the book's write transaction before
	// the tags are applied (WithAudiobookTagRereadReset).
	reset    func(ctx context.Context, tx pgx.Tx) error
	resetMu  sync.Mutex
	resetErr error
}

// WithAudiobookTagReread marks a scan as an admin's or metadata curator's
// explicit refresh of the audiobook contentID: that book is read from its
// files again even when they match the stored size and modification time.
// Without it an unchanged book is skipped, so changes to how tags are read
// never reach it. The book keeps its item, files, and progress; only the
// fields its tags own change. Other books the scan reaches are skipped as
// usual. Scheduled scans, real-time monitoring, and library scans never set
// it. AudiobookTagRereadDone reports, on the returned context, whether the
// book was read.
func WithAudiobookTagReread(ctx context.Context, contentID string) context.Context {
	return context.WithValue(ctx, audiobookTagRereadKey{}, &audiobookTagReread{contentID: contentID})
}

// WithAudiobookTagRereadReset is WithAudiobookTagReread for an admin's Reset
// to Provider: reset runs inside the transaction that writes the book's
// re-read tags, before they are applied, so the book's match is cleared only
// when its files were read and the tags land in the same commit. A scan that
// cannot read the book leaves it as it was. AudiobookTagRereadResetErr
// reports a failed reset.
func WithAudiobookTagRereadReset(ctx context.Context, contentID string, reset func(ctx context.Context, tx pgx.Tx) error) context.Context {
	return context.WithValue(ctx, audiobookTagRereadKey{}, &audiobookTagReread{contentID: contentID, reset: reset})
}

// AudiobookTagRereadResetErr returns the error of the reset a scan under
// ctx's mark ran, or nil.
func AudiobookTagRereadResetErr(ctx context.Context) error {
	mark, _ := ctx.Value(audiobookTagRereadKey{}).(*audiobookTagReread)
	if mark == nil {
		return nil
	}
	mark.resetMu.Lock()
	defer mark.resetMu.Unlock()
	return mark.resetErr
}

// runAudiobookTagRereadReset runs the mark's reset for contentID in tx.
func runAudiobookTagRereadReset(ctx context.Context, tx pgx.Tx, contentID string) error {
	mark, _ := ctx.Value(audiobookTagRereadKey{}).(*audiobookTagReread)
	if mark == nil || mark.reset == nil || !mark.covers(contentID) {
		return nil
	}
	err := mark.reset(ctx, tx)
	if err != nil {
		mark.resetMu.Lock()
		mark.resetErr = err
		mark.resetMu.Unlock()
	}
	return err
}

// WithAudiobookLibraryTagReread marks a library scan as an admin's explicit
// Refresh Metadata of the whole audiobook library: every book the scan reaches
// is read from its files again, as WithAudiobookTagReread reads one. The scan
// otherwise runs as any library scan (walk, grouping, held folders, missing
// files), and each book keeps its item, files, and progress. Only that admin
// action sets it. AudiobookTagRereadCount reports how many books were read.
func WithAudiobookLibraryTagReread(ctx context.Context) context.Context {
	return context.WithValue(ctx, audiobookTagRereadKey{}, &audiobookTagReread{all: true})
}

// AudiobookTagRereadCount reports how many books a scan under ctx's mark read.
func AudiobookTagRereadCount(ctx context.Context) int64 {
	mark, _ := ctx.Value(audiobookTagRereadKey{}).(*audiobookTagReread)
	if mark == nil {
		return 0
	}
	return mark.read.Load()
}

// AudiobookTagRereadDone reports whether a scan under ctx's mark read the
// marked book again. A scan reports a failed book among others that succeeded
// or were skipped as a success, so a caller that needs the book read checks
// this instead.
func AudiobookTagRereadDone(ctx context.Context) bool {
	mark, _ := ctx.Value(audiobookTagRereadKey{}).(*audiobookTagReread)
	return mark != nil && mark.done.Load()
}

func audiobookTagRereadRequested(ctx context.Context, contentID string) bool {
	mark, _ := ctx.Value(audiobookTagRereadKey{}).(*audiobookTagReread)
	return mark.covers(contentID)
}

func (m *audiobookTagReread) covers(contentID string) bool {
	return m != nil && contentID != "" && (m.all || m.contentID == contentID)
}

// noteAudiobookTagReread records that the book contentID was read under the
// scan's mark.
func noteAudiobookTagReread(ctx context.Context, contentID string) {
	if mark, _ := ctx.Value(audiobookTagRereadKey{}).(*audiobookTagReread); mark.covers(contentID) {
		mark.done.Store(true)
		mark.read.Add(1)
	}
}
