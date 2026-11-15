package scanner

import (
	"context"
	"sync/atomic"
)

type audiobookTagRereadKey struct{}

// audiobookTagReread is the mark WithAudiobookTagReread puts on a scan: the
// book to read again, and whether a scan under the mark read it.
type audiobookTagReread struct {
	contentID string
	done      atomic.Bool
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
	return mark != nil && contentID != "" && mark.contentID == contentID
}

// noteAudiobookTagReread records that the book contentID was read under the
// scan's mark.
func noteAudiobookTagReread(ctx context.Context, contentID string) {
	if mark, _ := ctx.Value(audiobookTagRereadKey{}).(*audiobookTagReread); mark != nil && contentID != "" && mark.contentID == contentID {
		mark.done.Store(true)
	}
}
