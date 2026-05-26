package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/go-chi/chi/v5"
)

const (
	subtitleUploadMaxSize = subtitles.MaxUploadSize
	// Allow multipart framing and small form fields above the file size cap.
	subtitleUploadMaxBodySize = subtitleUploadMaxSize + (256 << 10)
)

func parseSubtitleMultipartForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, subtitleUploadMaxBodySize)
	if err := r.ParseMultipartForm(subtitleUploadMaxSize); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Subtitle file must be under 5 MB")
		} else {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid multipart form")
		}
		return false
	}
	return true
}

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

func (h *SubtitleSearchHandler) authorizeMediaFile(w http.ResponseWriter, r *http.Request, fileID int) bool {
	if h.FileAuthorizer == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Media file authorization is not configured")
		return false
	}
	if _, err := h.FileAuthorizer.Authorize(r, fileID); err != nil {
		switch {
		case errors.Is(err, catalog.ErrItemNotFound), errors.Is(err, catalog.ErrEpisodeNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
		}
		return false
	}
	return true
}

// HandleSearch handles POST /api/v1/subtitles/search
func (h *SubtitleSearchHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchSubtitlesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}

	if !h.authorizeMediaFile(w, r, req.MediaFileID) {
		return
	}

	meta, err := h.mediaResolver.GetMediaFileWithMetadata(r.Context(), req.MediaFileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "metadata_error", "Failed to look up media metadata")
		return
	}
	if meta == nil {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}

	releaseInfo := subtitles.ParseReleaseInfo(meta.FilePath)
	searchReq := subtitles.SearchRequest{
		IMDbID:    meta.IMDbID,
		Title:     meta.Title,
		Year:      meta.Year,
		Season:    meta.Season,
		Episode:   meta.Episode,
		Languages: req.Languages,
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

	resp, err := h.manager.Search(r.Context(), searchReq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_error", "Subtitle search failed")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleDownload handles POST /api/v1/subtitles/download
func (h *SubtitleSearchHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	var req downloadSubtitleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid request body")
		return
	}

	if !h.authorizeMediaFile(w, r, req.MediaFileID) {
		return
	}

	userID := apimw.GetUserID(r.Context())

	sub, err := h.manager.Download(r.Context(), subtitles.DownloadRequest{
		ProviderName:    req.Provider,
		SubtitleID:      req.SubtitleID,
		MediaFileID:     req.MediaFileID,
		UserID:          &userID,
		Language:        req.Language,
		ReleaseName:     req.ReleaseName,
		Score:           req.Score,
		HearingImpaired: req.HearingImpaired,
	})
	if err != nil {
		slog.Error("subtitle download failed", "provider", req.Provider, "subtitle_id", req.SubtitleID, "error", err)
		writeError(w, http.StatusInternalServerError, "download_error", "Failed to download subtitle")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"subtitle": sub})
}

// HandleUpload handles POST /api/v1/subtitles/upload
func (h *SubtitleSearchHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if !parseSubtitleMultipartForm(w, r) {
		return
	}

	mediaFileID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("media_file_id")))
	if err != nil || mediaFileID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid media_file_id")
		return
	}

	if !h.authorizeMediaFile(w, r, mediaFileID) {
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing subtitle file")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, subtitleUploadMaxSize+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read upload")
		return
	}
	if len(data) > subtitleUploadMaxSize {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Subtitle file must be under 5 MB")
		return
	}

	releaseName := strings.TrimSpace(r.FormValue("release_name"))
	hearingImpaired := parseBoolFormValue(r.FormValue("hearing_impaired"))
	userID := apimw.GetUserID(r.Context())

	userLanguage := strings.TrimSpace(r.FormValue("language"))
	preferUserLanguage := parseBoolFormValue(r.FormValue("language_override"))

	sub, err := h.manager.Upload(r.Context(), subtitles.UploadRequest{
		MediaFileID:        mediaFileID,
		UserID:             &userID,
		Language:           userLanguage,
		PreferUserLanguage: preferUserLanguage,
		Filename:           header.Filename,
		ReleaseName:        releaseName,
		HearingImpaired:    hearingImpaired,
		Data:               data,
	})
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "unsupported subtitle format"),
			strings.Contains(err.Error(), "missing file extension"),
			strings.Contains(err.Error(), "empty subtitle file"),
			strings.Contains(err.Error(), "could not detect subtitle language"),
			strings.Contains(err.Error(), "invalid subtitle language"):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		case strings.Contains(err.Error(), "exceeds maximum size"):
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Subtitle file must be under 5 MB")
		default:
			slog.Error("subtitle upload failed", "media_file_id", mediaFileID, "error", err)
			writeError(w, http.StatusInternalServerError, "upload_error", "Failed to upload subtitle")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"subtitle": sub})
}

// HandleDetectLanguage handles POST /api/v1/subtitles/detect-language
func (h *SubtitleSearchHandler) HandleDetectLanguage(w http.ResponseWriter, r *http.Request) {
	if !parseSubtitleMultipartForm(w, r) {
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing subtitle file")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, subtitleUploadMaxSize+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read upload")
		return
	}
	if len(data) > subtitleUploadMaxSize {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Subtitle file must be under 5 MB")
		return
	}

	format, err := subtitles.FormatFromFilename(header.Filename)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	userLanguage := strings.TrimSpace(r.FormValue("language"))
	detected, err := subtitles.ResolveUploadLanguage(header.Filename, format, data, userLanguage, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, detected)
}

// HandleList handles GET /api/v1/subtitles/{media_file_id}
func (h *SubtitleSearchHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	mediaFileID, err := strconv.Atoi(chi.URLParam(r, "media_file_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid media file ID")
		return
	}

	if !h.authorizeMediaFile(w, r, mediaFileID) {
		return
	}

	subs, err := h.repo.ListDownloadedSubtitles(r.Context(), mediaFileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_error", "Failed to list subtitles")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"subtitles": subs})
}

// HandleDelete handles DELETE /api/v1/subtitles/{id}
func (h *SubtitleSearchHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "Invalid subtitle ID")
		return
	}

	sub, err := h.repo.GetDownloadedSubtitle(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup_error", "Failed to look up subtitle")
		return
	}
	if sub == nil {
		writeError(w, http.StatusNotFound, "not_found", "Subtitle not found")
		return
	}

	if !h.authorizeMediaFile(w, r, sub.MediaFileID) {
		return
	}

	claims := apimw.GetClaims(r.Context())
	isAdmin := claims != nil && claims.Role == "admin"
	isOwner := sub.DownloadedBy != nil && claims != nil && *sub.DownloadedBy == claims.UserID
	if !isAdmin && !isOwner {
		writeError(w, http.StatusForbidden, "forbidden", "Not authorized to delete this subtitle")
		return
	}

	if err := h.manager.DeleteSubtitle(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete_error", "Failed to delete subtitle")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseBoolFormValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
