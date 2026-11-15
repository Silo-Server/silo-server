package scanner

import "context"

type audiobookTagRereadKey struct{}

// WithAudiobookTagReread marks a scan as an admin's explicit metadata
// refresh: every audiobook it reaches is read from its files again, even when
// they match the stored size and modification time. Without it an unchanged
// book is skipped, so changes to how tags are read never reach it. The book
// keeps its item, files, and progress; only the fields its tags own change.
// Scheduled scans, real-time monitoring, and library scans never set it.
func WithAudiobookTagReread(ctx context.Context) context.Context {
	return context.WithValue(ctx, audiobookTagRereadKey{}, true)
}

func audiobookTagRereadRequested(ctx context.Context) bool {
	reread, _ := ctx.Value(audiobookTagRereadKey{}).(bool)
	return reread
}
