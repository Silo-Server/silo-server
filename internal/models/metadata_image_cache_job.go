package models

import "time"

type MetadataImageCacheJob struct {
	ID                int64
	TargetType        string
	TargetContentID   string
	TargetLanguage    string
	SeriesID          string
	SourcePath        string
	ProviderID        string
	ProviderContentID string
	ContentType       string
	ImageType         string
	SeasonNumber      *int
	EpisodeNumber     *int
	Status            string
	AttemptCount      int
	NextAttemptAt     time.Time
	LockedAt          *time.Time
	LockedBy          string
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}
