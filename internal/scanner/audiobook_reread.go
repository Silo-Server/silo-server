package scanner

import (
	"context"
	"sync/atomic"
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
