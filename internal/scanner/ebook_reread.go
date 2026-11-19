package scanner

import "context"

type ebookFileRereadKey struct{}

// WithEbookFileReread marks a scan as an admin's or metadata curator's Reset
// to Provider of the ebook contentID: that book's file is read again even
// when it matches the stored size and modification time, so the reset starts
// from the file's own metadata. The book keeps its item and content ID. Other
// books the scan reaches are skipped as usual. Scheduled scans, real-time
// monitoring, and library scans never set it.
func WithEbookFileReread(ctx context.Context, contentID string) context.Context {
	return context.WithValue(ctx, ebookFileRereadKey{}, contentID)
}

func ebookFileRereadRequested(ctx context.Context, contentID string) bool {
	marked, _ := ctx.Value(ebookFileRereadKey{}).(string)
	return marked != "" && marked == contentID
}
