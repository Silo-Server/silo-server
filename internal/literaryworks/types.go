package literaryworks

import "time"

const (
	FormatEbook     = "ebook"
	FormatAudiobook = "audiobook"
	FormatComic     = "comic"
	FormatManga     = "manga"

	LinkManual        = "manual"
	LinkExternalID    = "external_id"
	LinkMetadataMatch = "metadata_match"
	LinkSeriesMatch   = "series_match"
	LinkScanSeed      = "scan_seed"

	DecisionConfirmed = "confirmed"
	DecisionIgnored   = "ignored"
)

type Work struct {
	WorkID                string
	CanonicalTitle        string
	SortTitle             string
	NormalizedTitle       string
	PrimaryAuthorKey      string
	PrimaryCoverContentID string
	Description           string
	PublishedDate         *time.Time
	Publisher             string
	Genres                []string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type WorkItem struct {
	WorkID      string
	ContentID   string
	FormatType  string
	LinkSource  string
	Confidence  float64
	ConfirmedAt *time.Time
	IgnoredAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Candidate struct {
	SourceContentID string            `json:"source_content_id"`
	TargetContentID string            `json:"target_content_id"`
	TargetWorkID    string            `json:"target_work_id,omitempty"`
	Score           float64           `json:"score"`
	LinkSource      string            `json:"link_source"`
	Evidence        map[string]string `json:"evidence"`
}

type DetailResponse struct {
	WorkID          string           `json:"work_id"`
	WorkTitle       string           `json:"work_title"`
	Authors         []PersonResponse `json:"authors"`
	Formats         []FormatResponse `json:"formats"`
	PrimaryCoverURL string           `json:"primary_cover_url,omitempty"`
	Metadata        WorkMetadata     `json:"metadata"`
}

type PersonResponse struct {
	PersonID string `json:"person_id,omitempty"`
	Name     string `json:"name"`
}

type FormatResponse struct {
	Type           string            `json:"type"`
	ContentID      string            `json:"content_id"`
	LibraryID      int               `json:"library_id,omitempty"`
	AvailableFiles []FileResponse    `json:"available_files"`
	Progress       *ProgressResponse `json:"progress,omitempty"`
}

type FileResponse struct {
	FileID          int     `json:"file_id"`
	OriginalName    string  `json:"original_filename"`
	Format          string  `json:"format"`
	MIMEType        string  `json:"mime_type,omitempty"`
	Size            int64   `json:"size,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
}

type ProgressResponse struct {
	Kind            string   `json:"kind"`
	Progress        *float64 `json:"progress,omitempty"`
	PositionSeconds *float64 `json:"position_seconds,omitempty"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

type WorkMetadata struct {
	Description   string      `json:"description,omitempty"`
	Series        *SeriesInfo `json:"series,omitempty"`
	Genres        []string    `json:"genres"`
	PublishedDate string      `json:"published_date,omitempty"`
	Publisher     string      `json:"publisher,omitempty"`
}

type SeriesInfo struct {
	Name  string   `json:"name"`
	Index *float64 `json:"index,omitempty"`
}
