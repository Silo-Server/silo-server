package jellycompat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/text/language"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// RemoteSubtitleService is the native subtitle search and download that
// Jellyfin's remote subtitle routes call (#3205). The API listener's subtitle
// search handler implements it.
type RemoteSubtitleService interface {
	SearchSubtitles(ctx context.Context, access catalog.AccessFilter, fileID int, languages []string) (*subtitles.SearchResponse, error)
	DownloadStoredSubtitle(ctx context.Context, access catalog.AccessFilter, req subtitles.DownloadRequest) (*subtitles.DownloadedSubtitle, error)
}

// RemoteSubtitlesHandler serves Jellyfin's subtitle search and download,
// GET and POST /Items/{id}/RemoteSearch/Subtitles/{...}, through the
// providers the server has configured.
type RemoteSubtitlesHandler struct {
	content ContentService
	codec   *ResourceIDCodec
	access  AccessFilterResolver
	// service returns the subtitle service once the API listener built it,
	// or nil while it isn't configured.
	service  func() RemoteSubtitleService
	settings SettingsReader
}

// NewRemoteSubtitlesHandler returns the handler for the remote subtitle routes.
func NewRemoteSubtitlesHandler(content ContentService, codec *ResourceIDCodec, access AccessFilterResolver, service func() RemoteSubtitleService, settings SettingsReader) *RemoteSubtitlesHandler {
	return &RemoteSubtitlesHandler{content: content, codec: codec, access: access, service: service, settings: settings}
}

// remoteSubtitleInfo is Jellyfin's RemoteSubtitleInfo.
type remoteSubtitleInfo struct {
	ThreeLetterISOLanguageName string     `json:"ThreeLetterISOLanguageName"`
	ID                         string     `json:"Id"`
	ProviderName               string     `json:"ProviderName"`
	Name                       string     `json:"Name"`
	Format                     string     `json:"Format"`
	DateCreated                *time.Time `json:"DateCreated,omitempty"`
	CommunityRating            *float64   `json:"CommunityRating,omitempty"`
	DownloadCount              *int       `json:"DownloadCount,omitempty"`
	IsHashMatch                bool       `json:"IsHashMatch"`
	HearingImpaired            bool       `json:"HearingImpaired"`
}

// remoteSubtitleToken is what a search result's Id carries, so the download
// that names it needs no server-side state: the media file the search ran
// for and the provider result's fields the native download takes.
type remoteSubtitleToken struct {
	FileID          int     `json:"f"`
	Provider        string  `json:"p"`
	SubtitleID      string  `json:"s"`
	Language        string  `json:"l"`
	ReleaseName     string  `json:"r,omitempty"`
	Score           float64 `json:"c,omitempty"`
	HearingImpaired bool    `json:"h,omitempty"`
}

func encodeRemoteSubtitleToken(t remoteSubtitleToken) (string, bool) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", false
	}
	return base64.RawURLEncoding.EncodeToString(raw), true
}

func decodeRemoteSubtitleToken(raw string) (remoteSubtitleToken, bool) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return remoteSubtitleToken{}, false
	}
	var t remoteSubtitleToken
	if json.Unmarshal(data, &t) != nil || t.FileID <= 0 || t.Provider == "" || t.SubtitleID == "" || t.Language == "" {
		return remoteSubtitleToken{}, false
	}
	return t, true
}

// HandleSearch answers GET /Items/{id}/RemoteSearch/Subtitles/{language}
// with the configured providers' results for the item's media file.
func (h *RemoteSubtitlesHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	languages, err := subtitles.NormalizeSearchLanguages([]string{chi.URLParam(r, "language")})
	if err != nil || len(languages) == 0 {
		writeError(w, http.StatusBadRequest, "BadRequest", "Invalid subtitle language")
		return
	}
	fileID, ok := h.itemFile(w, r, session, r.URL.Query().Get("MediaSourceId"))
	if !ok {
		return
	}
	service := h.subtitleService()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Subtitle search is not configured")
		return
	}
	result, err := service.SearchSubtitles(r.Context(), h.viewerAccess(r.Context(), session), fileID, languages)
	if err != nil {
		writeRemoteSubtitleError(w, r, err)
		return
	}
	out := []remoteSubtitleInfo{}
	if result != nil {
		for _, row := range result.Results {
			if info, ok := remoteSubtitleInfoFor(fileID, row); ok {
				out = append(out, info)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// remoteSubtitleInfoFor maps one provider result found for fileID, or
// reports false for a result whose language the provider can't name.
func remoteSubtitleInfoFor(fileID int, row subtitles.SubtitleResult) (remoteSubtitleInfo, bool) {
	tag, ok := subtitles.CanonicalProviderLanguage(row.Provider, row.Language)
	if !ok {
		return remoteSubtitleInfo{}, false
	}
	id, ok := encodeRemoteSubtitleToken(remoteSubtitleToken{
		FileID: fileID, Provider: row.Provider, SubtitleID: row.ID, Language: tag,
		ReleaseName: row.ReleaseName, Score: row.Score, HearingImpaired: row.HearingImpaired,
	})
	if !ok {
		return remoteSubtitleInfo{}, false
	}
	info := remoteSubtitleInfo{
		ThreeLetterISOLanguageName: threeLetterLanguage(tag),
		ID:                         id,
		ProviderName:               row.Provider,
		Name:                       row.ReleaseName,
		Format:                     string(row.Format),
		HearingImpaired:            row.HearingImpaired,
	}
	if !row.UploadDate.IsZero() {
		info.DateCreated = new(row.UploadDate.UTC())
	}
	if row.Score > 0 {
		info.CommunityRating = new(row.Score)
	}
	if row.Downloads > 0 {
		info.DownloadCount = new(row.Downloads)
	}
	return info, true
}

// HandleDownload answers POST /Items/{id}/RemoteSearch/Subtitles/{subtitleId}:
// it stores the search result the id names for the item's media file, where
// PlaybackInfo then lists it as an external subtitle.
func (h *RemoteSubtitlesHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	token, ok := decodeRemoteSubtitleToken(chi.URLParam(r, "subtitleId"))
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Subtitle not found")
		return
	}
	// The result must be one searched for this item: its file has to be one
	// of the item's versions the viewer can see.
	if _, ok := h.itemFile(w, r, session, h.codec.EncodeIntID(EncodedIDMediaSource, int64(token.FileID))); !ok {
		return
	}
	if h.demoRestricted(r.Context()) {
		writeError(w, http.StatusForbidden, "Forbidden", "This action is not available in demo mode.")
		return
	}
	service := h.subtitleService()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Subtitle download is not configured")
		return
	}
	_, err := service.DownloadStoredSubtitle(r.Context(), h.viewerAccess(r.Context(), session), subtitles.DownloadRequest{
		MediaFileID: token.FileID, ProviderName: token.Provider, SubtitleID: token.SubtitleID, Language: token.Language,
		ReleaseName: token.ReleaseName, Score: token.Score, HearingImpaired: token.HearingImpaired,
	})
	if err != nil {
		writeRemoteSubtitleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// itemFile resolves the route's item to the media file a subtitle is for: the
// media source the route names, else the wanted one when it is one of the
// item's versions, else the item's first version. It writes the error itself.
func (h *RemoteSubtitlesHandler) itemFile(w http.ResponseWriter, r *http.Request, session *Session, wanted string) (int, bool) {
	contentID, pathFileID, err := decodeItemOrMediaSourceID(r.Context(), h.codec, chi.URLParam(r, "id"))
	if err != nil {
		writeItemIDError(w, r, err)
		return 0, false
	}
	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return 0, false
	}
	if detail == nil || len(detail.Versions) == 0 {
		writeError(w, http.StatusNotFound, "NotFound", "Item does not have a media file")
		return 0, false
	}
	has := func(fileID int64) bool {
		return slices.ContainsFunc(detail.Versions, func(v catalog.FileVersion) bool { return int64(v.FileID) == fileID })
	}
	if pathFileID > 0 {
		if !has(pathFileID) {
			writeError(w, http.StatusNotFound, "NotFound", "Media source not found")
			return 0, false
		}
		return int(pathFileID), true
	}
	if wanted != "" {
		if fileID, err := h.codec.DecodeIntID(EncodedIDMediaSource, wanted); err == nil && has(fileID) {
			return int(fileID), true
		}
		if r.Method == http.MethodPost {
			// A download names its file; one outside the item is refused
			// rather than stored on another version.
			writeError(w, http.StatusNotFound, "NotFound", "Subtitle not found")
			return 0, false
		}
	}
	return detail.Versions[0].FileID, true
}

func (h *RemoteSubtitlesHandler) subtitleService() RemoteSubtitleService {
	if h.service == nil {
		return nil
	}
	return h.service()
}

func (h *RemoteSubtitlesHandler) viewerAccess(ctx context.Context, session *Session) catalog.AccessFilter {
	if h.access == nil {
		return catalog.AccessFilter{UserID: session.StreamAppUserID}
	}
	return h.access(ctx, session.StreamAppUserID, session.ProfileID)
}

// demoRestricted refuses subtitle downloads on a demo server, as the native
// download does. A compat session carries no role, so administrators are
// refused too; they can download through the web app.
func (h *RemoteSubtitlesHandler) demoRestricted(ctx context.Context) bool {
	if h.settings == nil {
		return false
	}
	enabled, _ := h.settings.Get(ctx, "demo.enabled")
	return enabled == "true"
}

// statusError is a refusal the native subtitle service answers with, as
// handlers.APIError carries it.
type statusError interface {
	error
	HTTPStatus() int
	ErrorCode() string
	PublicMessage() string
}

func writeRemoteSubtitleError(w http.ResponseWriter, r *http.Request, err error) {
	if refusal, ok := errors.AsType[statusError](err); ok {
		switch status := refusal.HTTPStatus(); {
		case status >= 400 && status < 500:
			writeError(w, status, refusal.ErrorCode(), refusal.PublicMessage())
			return
		case status == http.StatusServiceUnavailable:
			writeError(w, status, "ServiceUnavailable", refusal.PublicMessage())
			return
		}
	}
	slog.ErrorContext(r.Context(), "jellycompat: remote subtitle request failed", "component", "jellycompat", "error", err)
	writeError(w, http.StatusInternalServerError, "ServerError", "Subtitle request failed")
}

// threeLetterLanguage is the ISO 639-2 code Jellyfin names a subtitle's
// language by, from its BCP 47 tag.
func threeLetterLanguage(tag string) string {
	base, _ := language.Make(tag).Base()
	if iso3 := base.ISO3(); iso3 != "" && iso3 != "und" {
		return iso3
	}
	return tag
}
