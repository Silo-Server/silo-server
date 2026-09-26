// internal/subtitles/provider.go
package subtitles

import "context"

// Provider defines the interface that each subtitle source must implement.
type Provider interface {
	// Name returns the provider identifier (e.g., "opensubtitles", "subdl", "subsource").
	Name() string

	// Search queries the provider for subtitles matching the request.
	Search(ctx context.Context, req SearchRequest) ([]SubtitleResult, error)

	// Download fetches the subtitle file content by provider-specific ID.
	Download(ctx context.Context, id string) ([]byte, SubtitleFormat, error)
}

// BlobStore is the object storage the subtitle system needs. Subtitle objects
// are always read through the server, never by a redirect to storage, so this
// is key-only: no bucket, no signed URLs. blobstore.NewByteStore adapts either
// backend to it.
type BlobStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}
