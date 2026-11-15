package scanner

import "context"

type audiobookTagRereadKey struct{}

// WithAudiobookTagReread marks a scan as an admin's or metadata curator's
// explicit refresh of the audiobook contentID: that book is read from its
// files again even when they match the stored size and modification time.
// Without it an unchanged book is skipped, so changes to how tags are read
// never reach it. The book keeps its item, files, and progress; only the
// fields its tags own change. Other books the scan reaches are skipped as
// usual. Scheduled scans, real-time monitoring, and library scans never set
// it.
func WithAudiobookTagReread(ctx context.Context, contentID string) context.Context {
	return context.WithValue(ctx, audiobookTagRereadKey{}, contentID)
}

func audiobookTagRereadRequested(ctx context.Context, contentID string) bool {
	marked, _ := ctx.Value(audiobookTagRereadKey{}).(string)
	return marked != "" && marked == contentID
}
