// internal/subtitles/repo.go
package subtitles

import "context"

// SubtitleMetadataUpdate contains mutable fields for a downloaded subtitle record.
type SubtitleMetadataUpdate struct {
	Language        string
	ReleaseName     string
	HearingImpaired bool
	S3Key           string
}

// Repository defines database operations for subtitle management.
type Repository interface {
	InsertDownloadedSubtitle(ctx context.Context, sub *DownloadedSubtitle) error
	GetDownloadedSubtitle(ctx context.Context, id int) (*DownloadedSubtitle, error)
	ListDownloadedSubtitles(ctx context.Context, mediaFileID int) ([]DownloadedSubtitle, error)
	UpdateDownloadedSubtitle(ctx context.Context, id int, update SubtitleMetadataUpdate) (*DownloadedSubtitle, error)
	DeleteDownloadedSubtitle(ctx context.Context, id int) (*DownloadedSubtitle, error)
	GetDownloadedSubtitleByS3Key(ctx context.Context, s3Key string) (*DownloadedSubtitle, error)

	ListProviderConfigs(ctx context.Context) ([]ProviderConfig, error)
	GetProviderConfig(ctx context.Context, providerName string) (*ProviderConfig, error)
	UpsertProviderConfig(ctx context.Context, cfg *ProviderConfig) error
}
