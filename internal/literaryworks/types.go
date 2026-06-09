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

type WorkSummary struct {
	WorkID  string              `json:"work_id,omitempty"`
	Title   string              `json:"work_title,omitempty"`
	Formats []WorkFormatSummary `json:"work_formats,omitempty"`
}

type WorkFormatSummary struct {
	Type      string `json:"type"`
	ContentID string `json:"content_id"`
	LibraryID int    `json:"library_id,omitempty"`
}
