package handlers

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const (
	subtitleUploadMaxSize = subtitles.MaxUploadSize
	// Allow multipart framing and small form fields above the file size cap.
	subtitleUploadMaxBodySize = subtitleUploadMaxSize + (256 << 10)
)

// SubtitleMediaResolver looks up media metadata for subtitle search.
type SubtitleMediaResolver interface {
	GetMediaFileWithMetadata(ctx context.Context, fileID int) (*MediaFileMetadata, error)
}

// MediaFileMetadata combines file info with parent item metadata for search.
type MediaFileMetadata struct {
	FileID     int
	FilePath   string
	FileSize   int64
	FileHash   string // OSHash (16-char hex)
	Resolution string
	VideoCodec string
	AudioCodec string
	Title      string
	Year       int
	IMDbID     string
	Season     int
	Episode    int
}

// SubtitleSearchHandler handles user-facing subtitle search operations.
type SubtitleSearchHandler struct {
	manager        *subtitles.Manager
	repo           subtitles.Repository
	mediaResolver  SubtitleMediaResolver
	FileAuthorizer *MediaFileAuthorizer
	sync           SubtitleSyncService
	external       ExternalTimingStore
}

// NewSubtitleSearchHandler creates a new SubtitleSearchHandler.
func NewSubtitleSearchHandler(
	manager *subtitles.Manager,
	repo subtitles.Repository,
	mediaResolver SubtitleMediaResolver,
) *SubtitleSearchHandler {
	return &SubtitleSearchHandler{
		manager:       manager,
		repo:          repo,
		mediaResolver: mediaResolver,
	}
}

type searchSubtitlesRequest struct {
	MediaFileID int      `json:"media_file_id"`
	Languages   []string `json:"languages"`
}

type downloadSubtitleRequest struct {
	MediaFileID     int     `json:"media_file_id"`
	Provider        string  `json:"provider"`
	SubtitleID      string  `json:"subtitle_id"`
	Language        string  `json:"language"`
	ReleaseName     string  `json:"release_name"`
	Format          string  `json:"format"`
	Score           float64 `json:"score"`
	HearingImpaired bool    `json:"hearing_impaired"`
}

// SubtitleProviderStatusView tells a client whether this deployment can
// search external subtitle providers at all, following the per-subsystem
// capability convention (/subtitles/ai/status, /items/trailers/capability).
//
// Without it a search on a server with no providers configured answers exactly
// like a search that ran and matched nothing, so a player has no way to tell
// "this server cannot do that" from "nothing found for this file" and ends up
// offering an entry point that can never succeed. A client that finds enabled
// false should disable the search action and say why rather than let the user
// run a query that is guaranteed to come back empty.
//
// Provider names are safe to return to any authenticated viewer: they already
// travel in every SubtitleResult.provider and DownloadedSubtitle.provider. The
// credentials behind them stay in the admin-only provider config.
type SubtitleProviderStatusView struct {
	SchemaVersion int `json:"schema_version"`
	// Enabled reports that at least one provider is registered, so
	// POST /subtitles/search can actually reach an upstream.
	Enabled bool `json:"enabled"`
	// Providers is the registered provider identifiers, sorted. Always a
	// list — empty rather than null — so clients can iterate it unguarded.
	Providers []string `json:"providers"`
}

// SubtitleProviderStatus returns public provider identifiers without credentials.
// A missing subsystem is a disabled capability, not an unavailable endpoint.
func (h *SubtitleSearchHandler) SubtitleProviderStatus() SubtitleProviderStatusView {
	providers := []string{}
	if h != nil && h.manager != nil {
		providers = h.manager.ProviderNames()
	}
	return SubtitleProviderStatusView{
		SchemaVersion: 1,
		Enabled:       len(providers) > 0,
		Providers:     providers,
	}
}

func (h *SubtitleSearchHandler) searchAuthorizedSubtitles(ctx context.Context, fileID int, languages []string, bridge bool) (*subtitles.SearchResponse, *APIError) {
	languages = subtitles.NormalizeBridgeSearchLanguages(languages)
	meta, err := h.mediaResolver.GetMediaFileWithMetadata(ctx, fileID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "metadata_error", "Failed to look up media metadata")
	}
	if meta == nil {
		return nil, apiError(http.StatusNotFound, "not_found", "Media file not found")
	}

	releaseInfo := subtitles.ParseReleaseInfo(meta.FilePath)
	searchReq := subtitles.SearchRequest{
		IMDbID:    meta.IMDbID,
		Title:     meta.Title,
		Year:      meta.Year,
		Season:    meta.Season,
		Episode:   meta.Episode,
		Languages: languages,
		Filename:  filepath.Base(meta.FilePath),
		FileHash:  meta.FileHash,
		MediaInfo: &subtitles.MediaMatchInfo{
			ReleaseGroup: releaseInfo.ReleaseGroup,
			Resolution:   firstNonEmpty(meta.Resolution, releaseInfo.Resolution),
			VideoCodec:   firstNonEmpty(meta.VideoCodec, releaseInfo.VideoCodec),
			AudioCodec:   firstNonEmpty(meta.AudioCodec, releaseInfo.AudioCodec),
			Source:       releaseInfo.Source,
		},
	}

	var resp *subtitles.SearchResponse
	if bridge {
		resp, err = h.manager.SearchBridge(ctx, searchReq)
	} else {
		resp, err = h.manager.Search(ctx, searchReq)
	}
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "search_error", "Subtitle search failed")
	}

	return resp, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
