// internal/subtitles/types.go
package subtitles

import "time"

// SubtitleFormat represents a subtitle file format.
type SubtitleFormat string

const (
	FormatSRT SubtitleFormat = "srt"
	FormatASS SubtitleFormat = "ass"
	FormatSSA SubtitleFormat = "ssa"
	FormatVTT SubtitleFormat = "vtt"
	FormatSUB SubtitleFormat = "sub"
)

// SearchRequest contains the parameters for searching subtitle providers.
type SearchRequest struct {
	IMDbID    string
	TMDbID    string
	Title     string
	Year      int
	Season    int // 0 for movies
	Episode   int // 0 for movies
	Languages []string
	Filename  string // media filename for provider-side matching
	FileHash  string // OSHash for OpenSubtitles moviehash search
	MediaInfo *MediaMatchInfo
}

// MediaMatchInfo holds metadata about the media file for scoring subtitle matches.
type MediaMatchInfo struct {
	ReleaseGroup string
	Resolution   string
	VideoCodec   string
	AudioCodec   string
	Source       string // BluRay, WEB-DL, etc.
}

// SubtitleResult represents a single subtitle search result from a provider.
type SubtitleResult struct {
	ID              string         `json:"id"`
	Provider        string         `json:"provider"`
	Language        string         `json:"language"`
	ReleaseName     string         `json:"release_name"`
	Format          SubtitleFormat `json:"format"`
	Score           float64        `json:"score"`
	Downloads       int            `json:"downloads"`
	HearingImpaired bool           `json:"hearing_impaired"`
	UploadDate      time.Time      `json:"upload_date,omitempty"`
	rawLanguage     string
}

// SetRawLanguageForBridge records the provider spelling for the frozen v1
// bridge. It is intentionally excluded from JSON and unavailable to clients.
func (r *SubtitleResult) SetRawLanguageForBridge(value string) { r.rawLanguage = value }

// DownloadedSubtitle represents a subtitle that has been downloaded and stored.
type DownloadedSubtitle struct {
	ID              int            `json:"id"`
	MediaFileID     int            `json:"media_file_id"`
	Provider        string         `json:"provider"`
	Language        string         `json:"language"`
	Format          SubtitleFormat `json:"format"`
	ReleaseName     string         `json:"release_name"`
	ContentSHA256   string         `json:"-"`
	Revision        int64          `json:"-"`
	S3Key           string         `json:"-"` // internal, not exposed to frontend
	Score           float64        `json:"score"`
	HearingImpaired bool           `json:"hearing_impaired"`
	DownloadedBy    *int           `json:"-"` // audit only
	CreatedAt       time.Time      `json:"created_at"`
}

// ProviderConfig stores the configuration for a subtitle provider.
type ProviderConfig struct {
	ProviderName   string    `json:"provider_name"`
	Enabled        bool      `json:"enabled"`
	APIKey         string    `json:"-"` // never serialized
	HasAPIKey      bool      `json:"has_api_key"`
	Username       string    `json:"-"`
	Password       string    `json:"-"`
	HasCredentials bool      `json:"has_credentials"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// SearchResponse contains merged search results and any warnings.
type SearchResponse struct {
	Results  []SubtitleResult `json:"results"`
	Warnings []string         `json:"warnings,omitempty"`
}
