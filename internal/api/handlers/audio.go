package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const audioPlaybackSessionTTL = 12 * time.Hour

type AudioDetailService interface {
	GetItemDetail(ctx context.Context, contentID string, filter catalog.AccessFilter) (*catalog.ItemDetail, error)
}

type AudioFileResolver interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
}

type AudioHandler struct {
	detailSvc     AudioDetailService
	files         AudioFileResolver
	storeProvider userstore.UserStoreProvider
	bookmarkStore *PGAudioBookmarkStore

	mu       sync.Mutex
	sessions map[string]audioPlaybackSession
	now      func() time.Time
}

func NewAudioHandler(
	detailSvc AudioDetailService,
	files AudioFileResolver,
	storeProvider userstore.UserStoreProvider,
	bookmarkStore *PGAudioBookmarkStore,
) *AudioHandler {
	return &AudioHandler{
		detailSvc:     detailSvc,
		files:         files,
		storeProvider: storeProvider,
		bookmarkStore: bookmarkStore,
		sessions:      make(map[string]audioPlaybackSession),
		now:           func() time.Time { return time.Now().UTC() },
	}
}

type audioPlaybackSession struct {
	ID           string
	UserID       int
	ProfileID    string
	ContentID    string
	TotalSeconds float64
	Files        []audioSessionFile
	CreatedAt    time.Time
	LastSeenAt   time.Time
	ExpiresAt    time.Time
}

type audioSessionFile struct {
	FileID   int
	FilePath string
	Start    float64
	Duration float64
	Mode     string
	TrackIdx int
}

type audioStartPlaybackRequest struct {
	ContentID     string   `json:"content_id"`
	StartPosition *float64 `json:"start_position,omitempty"`
	Restart       bool     `json:"restart,omitempty"`
}

type audioProgressRequest struct {
	Position float64 `json:"position"`
	Duration float64 `json:"duration,omitempty"`
	IsPaused bool    `json:"is_paused"`
}

type audioPlaybackResponse struct {
	SessionID             string               `json:"session_id"`
	ContentID             string               `json:"content_id"`
	Type                  string               `json:"type"`
	Title                 string               `json:"title"`
	Subtitle              string               `json:"subtitle,omitempty"`
	PosterURL             string               `json:"poster_url,omitempty"`
	BackdropURL           string               `json:"backdrop_url,omitempty"`
	TotalDurationSeconds  float64              `json:"total_duration_seconds"`
	ResumePositionSeconds float64              `json:"resume_position_seconds"`
	Tracks                []audioTrackResponse `json:"tracks"`
	Chapters              []audioChapter       `json:"chapters"`
	Bookmarks             []audioBookmark      `json:"bookmarks"`
}

type audioTrackResponse struct {
	Index              int            `json:"index"`
	FileID             int            `json:"file_id"`
	FileName           string         `json:"file_name,omitempty"`
	DurationSeconds    float64        `json:"duration_seconds"`
	StartOffsetSeconds float64        `json:"start_offset_seconds"`
	StreamURL          string         `json:"stream_url"`
	StreamType         string         `json:"stream_type"`
	PlayMethod         string         `json:"play_method"`
	Codec              string         `json:"codec,omitempty"`
	Container          string         `json:"container,omitempty"`
	Bitrate            int            `json:"bitrate,omitempty"`
	Chapters           []audioChapter `json:"chapters"`
}

type audioChapter struct {
	Index        int     `json:"index"`
	Title        string  `json:"title,omitempty"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds,omitempty"`
	TrackIndex   int     `json:"track_index,omitempty"`
}

type audioBookmark struct {
	ID          string  `json:"id"`
	ContentID   string  `json:"content_id"`
	TimeSeconds float64 `json:"time_seconds"`
	Title       string  `json:"title"`
	CreatedAt   string  `json:"created_at,omitempty"`
	UpdatedAt   string  `json:"updated_at,omitempty"`
}

type audioBookmarkRequest struct {
	TimeSeconds *float64 `json:"time_seconds"`
	Title       string   `json:"title"`
}

func (h *AudioHandler) HandleStartPlayback(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.detailSvc == nil || h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Audio playback is not configured")
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile is required")
		return
	}

	var req audioStartPlaybackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req.ContentID = strings.TrimSpace(req.ContentID)
	if req.ContentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return
	}

	filter := requestAccessFilter(r)
	detail, err := h.detailSvc.GetItemDetail(r.Context(), req.ContentID, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item detail")
		return
	}
	if detail == nil || detail.Type != "audiobook" {
		writeError(w, http.StatusNotFound, "not_found", "Audiobook not found")
		return
	}

	files, err := h.files.GetByContentID(r.Context(), req.ContentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load audio files")
		return
	}
	files = catalog.FilterMediaFilesByAccess(files, filter)
	sortAudioFiles(files)
	if len(files) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "No playable audio files found")
		return
	}

	sessionID := ulid.Make().String()
	tracks, sessionFiles, chapters, totalDuration := buildAudioTrackResponses(sessionID, files)
	if detail.Audiobook != nil && detail.Audiobook.TotalDurationSeconds > 0 {
		totalDuration = float64(detail.Audiobook.TotalDurationSeconds)
	}

	resume := resolveAudioResumePosition(r.Context(), h.storeProvider, userID, profileID, req.ContentID)
	if req.Restart {
		resume = 0
	} else if req.StartPosition != nil && isFiniteNonNegative(*req.StartPosition) {
		resume = clampFloat(*req.StartPosition, 0, totalDuration)
	}

	bookmarks, _ := h.listBookmarks(r.Context(), userID, profileID, req.ContentID)
	session := audioPlaybackSession{
		ID:           sessionID,
		UserID:       userID,
		ProfileID:    profileID,
		ContentID:    req.ContentID,
		TotalSeconds: totalDuration,
		Files:        sessionFiles,
		CreatedAt:    h.now(),
		LastSeenAt:   h.now(),
		ExpiresAt:    h.now().Add(audioPlaybackSessionTTL),
	}
	h.putSession(session)

	writeJSON(w, http.StatusOK, audioPlaybackResponse{
		SessionID:             sessionID,
		ContentID:             detail.ContentID,
		Type:                  detail.Type,
		Title:                 detail.Title,
		Subtitle:              audioSubtitle(detail),
		PosterURL:             detail.PosterURL,
		BackdropURL:           detail.BackdropURL,
		TotalDurationSeconds:  totalDuration,
		ResumePositionSeconds: resume,
		Tracks:                tracks,
		Chapters:              chapters,
		Bookmarks:             bookmarks,
	})
}

func (h *AudioHandler) HandleSyncPlayback(w http.ResponseWriter, r *http.Request) {
	session, ok := h.sessionForRequest(w, r)
	if !ok {
		return
	}
	var req audioProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	duration := req.Duration
	if duration <= 0 {
		duration = session.TotalSeconds
	}
	position := clampFloat(req.Position, 0, duration)
	if err := h.persistProgress(r.Context(), session.UserID, session.ProfileID, session.ContentID, position, duration); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to sync audio progress")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": session.ID,
		"position":   position,
		"duration":   duration,
		"is_paused":  req.IsPaused,
	})
}

func (h *AudioHandler) HandleClosePlayback(w http.ResponseWriter, r *http.Request) {
	session, ok := h.sessionForRequest(w, r)
	if !ok {
		return
	}
	var req audioProgressRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if isFiniteNonNegative(req.Position) {
		duration := req.Duration
		if duration <= 0 {
			duration = session.TotalSeconds
		}
		position := clampFloat(req.Position, 0, duration)
		_ = h.persistProgress(r.Context(), session.UserID, session.ProfileID, session.ContentID, position, duration)
	}
	h.deleteSession(session.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AudioHandler) HandleStreamTrack(w http.ResponseWriter, r *http.Request) {
	session, ok := h.sessionForRequest(w, r)
	if !ok {
		return
	}
	rawIndex := chi.URLParam(r, "track_index")
	index, err := strconv.Atoi(rawIndex)
	if err != nil || index < 0 || index >= len(session.Files) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid track index")
		return
	}
	file := session.Files[index]
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = file.Mode
	}
	if mode == "aac" {
		seek := 0.0
		if raw := r.URL.Query().Get("seek"); raw != "" {
			if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 {
				seek = parsed
			}
		}
		if err := playback.ServeRemux(w, r, file.FilePath, "mp4", seek, true, 0); err != nil {
			return
		}
		return
	}
	_ = playback.ServeDirectPlay(w, r, file.FilePath)
}

func (h *AudioHandler) HandleListBookmarks(w http.ResponseWriter, r *http.Request) {
	contentID, ok := h.contentIDParam(w, r)
	if !ok {
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	bookmarks, err := h.listBookmarks(r.Context(), userID, profileID, contentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list bookmarks")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookmarks": bookmarks})
}

func (h *AudioHandler) HandleCreateBookmark(w http.ResponseWriter, r *http.Request) {
	contentID, ok := h.contentIDParam(w, r)
	if !ok {
		return
	}
	if h.bookmarkStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Bookmarks are not configured")
		return
	}
	var req audioBookmarkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.TimeSeconds == nil || !isFiniteNonNegative(*req.TimeSeconds) {
		writeError(w, http.StatusBadRequest, "bad_request", "time_seconds is required")
		return
	}
	filter := requestAccessFilter(r)
	detail, err := h.detailSvc.GetItemDetail(r.Context(), contentID, filter)
	if err != nil || detail == nil || detail.Type != "audiobook" {
		writeError(w, http.StatusNotFound, "not_found", "Audiobook not found")
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	bookmark, err := h.bookmarkStore.Upsert(r.Context(), userID, profileID, contentID, *req.TimeSeconds, req.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save bookmark")
		return
	}
	writeJSON(w, http.StatusOK, bookmark)
}

func (h *AudioHandler) HandleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	contentID, ok := h.contentIDParam(w, r)
	if !ok {
		return
	}
	if h.bookmarkStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Bookmarks are not configured")
		return
	}
	rawTime := chi.URLParam(r, "time_seconds")
	timeSeconds, err := strconv.ParseFloat(rawTime, 64)
	if err != nil || !isFiniteNonNegative(timeSeconds) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid bookmark time")
		return
	}
	if err := h.bookmarkStore.Delete(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), contentID, timeSeconds); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete bookmark")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AudioHandler) contentIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	contentID := strings.TrimSpace(chi.URLParam(r, "content_id"))
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return "", false
	}
	return contentID, true
}

func (h *AudioHandler) sessionForRequest(w http.ResponseWriter, r *http.Request) (audioPlaybackSession, bool) {
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Session ID is required")
		return audioPlaybackSession{}, false
	}
	session, ok := h.getSession(sessionID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Audio playback session not found")
		return audioPlaybackSession{}, false
	}
	if session.UserID != apimw.GetUserID(r.Context()) {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return audioPlaybackSession{}, false
	}
	profileID := apimw.GetProfileID(r.Context())
	if profileID != "" && session.ProfileID != profileID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another profile")
		return audioPlaybackSession{}, false
	}
	return session, true
}

func (h *AudioHandler) putSession(session audioPlaybackSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneExpiredLocked(h.now())
	h.sessions[session.ID] = session
}

func (h *AudioHandler) getSession(sessionID string) (audioPlaybackSession, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.pruneExpiredLocked(now)
	session, ok := h.sessions[sessionID]
	if !ok {
		return audioPlaybackSession{}, false
	}
	if now.After(session.ExpiresAt) {
		delete(h.sessions, sessionID)
		return audioPlaybackSession{}, false
	}
	session.LastSeenAt = now
	h.sessions[sessionID] = session
	return session, true
}

func (h *AudioHandler) deleteSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, sessionID)
}

func (h *AudioHandler) pruneExpiredLocked(now time.Time) {
	for id, session := range h.sessions {
		if now.After(session.ExpiresAt) {
			delete(h.sessions, id)
		}
	}
}

func (h *AudioHandler) persistProgress(ctx context.Context, userID int, profileID, contentID string, position, duration float64) error {
	if h.storeProvider == nil {
		return nil
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return err
	}
	return store.SetProgress(ctx, profileID, contentID, position, duration, userstore.ProgressThresholds{})
}

func (h *AudioHandler) listBookmarks(ctx context.Context, userID int, profileID, contentID string) ([]audioBookmark, error) {
	if h.bookmarkStore == nil {
		return []audioBookmark{}, nil
	}
	return h.bookmarkStore.List(ctx, userID, profileID, contentID)
}

func buildAudioTrackResponses(sessionID string, files []*models.MediaFile) ([]audioTrackResponse, []audioSessionFile, []audioChapter, float64) {
	tracks := make([]audioTrackResponse, 0, len(files))
	sessionFiles := make([]audioSessionFile, 0, len(files))
	allChapters := []audioChapter{}
	offset := 0.0
	for index, file := range files {
		duration := float64(file.Duration)
		if duration < 0 {
			duration = 0
		}
		mode := audioDeliveryMode(file)
		streamURL := fmt.Sprintf("/api/v1/audio/playback/%s/tracks/%d", sessionID, index)
		if mode == "aac" {
			streamURL += "?mode=aac"
		}
		chapters := audioChaptersForTrack(index, offset, file.Chapters)
		allChapters = append(allChapters, chapters...)
		tracks = append(tracks, audioTrackResponse{
			Index:              index,
			FileID:             file.ID,
			FileName:           filepath.Base(file.FilePath),
			DurationSeconds:    duration,
			StartOffsetSeconds: offset,
			StreamURL:          streamURL,
			StreamType:         "progressive",
			PlayMethod:         mode,
			Codec:              file.CodecAudio,
			Container:          file.Container,
			Bitrate:            file.Bitrate,
			Chapters:           chapters,
		})
		sessionFiles = append(sessionFiles, audioSessionFile{
			FileID:   file.ID,
			FilePath: file.FilePath,
			Start:    offset,
			Duration: duration,
			Mode:     mode,
			TrackIdx: index,
		})
		offset += duration
	}
	return tracks, sessionFiles, allChapters, offset
}

func audioChaptersForTrack(trackIndex int, offset float64, chapters []models.MediaChapter) []audioChapter {
	out := make([]audioChapter, 0, len(chapters))
	for _, chapter := range chapters {
		start := offset + maxFloat(0, chapter.StartSeconds)
		end := 0.0
		if chapter.EndSeconds > 0 {
			end = offset + chapter.EndSeconds
		}
		out = append(out, audioChapter{
			Index:        len(out),
			Title:        chapter.Title,
			StartSeconds: start,
			EndSeconds:   end,
			TrackIndex:   trackIndex,
		})
	}
	return out
}

func sortAudioFiles(files []*models.MediaFile) {
	sort.SliceStable(files, func(i, j int) bool {
		left, right := files[i], files[j]
		if left.PresentationPartIndex != right.PresentationPartIndex {
			if left.PresentationPartIndex == 0 {
				return false
			}
			if right.PresentationPartIndex == 0 {
				return true
			}
			return left.PresentationPartIndex < right.PresentationPartIndex
		}
		if left.FilePath != right.FilePath {
			return left.FilePath < right.FilePath
		}
		return left.ID < right.ID
	})
}

func audioDeliveryMode(file *models.MediaFile) string {
	if file == nil {
		return "direct"
	}
	codec := strings.ToLower(strings.TrimSpace(file.CodecAudio))
	container := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(file.Container), "."))
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file.FilePath), "."))
	if container == "" {
		container = ext
	}
	switch codec {
	case "aac", "mp3", "alac", "flac":
		return "direct"
	case "":
		switch container {
		case "mp3", "m4a", "m4b", "mp4", "aac", "flac", "wav", "aiff", "aif":
			return "direct"
		}
	}
	return "aac"
}

func audioSubtitle(detail *catalog.ItemDetail) string {
	if detail == nil || detail.Audiobook == nil {
		return ""
	}
	parts := []string{}
	if len(detail.Audiobook.Authors) > 0 {
		names := make([]string, 0, len(detail.Audiobook.Authors))
		for _, author := range detail.Audiobook.Authors {
			if strings.TrimSpace(author.Name) != "" {
				names = append(names, author.Name)
			}
		}
		if len(names) > 0 {
			parts = append(parts, strings.Join(names, ", "))
		}
	}
	if len(detail.Audiobook.Narrators) > 0 {
		names := make([]string, 0, len(detail.Audiobook.Narrators))
		for _, narrator := range detail.Audiobook.Narrators {
			if strings.TrimSpace(narrator.Name) != "" {
				names = append(names, narrator.Name)
			}
		}
		if len(names) > 0 {
			parts = append(parts, "Narrated by "+strings.Join(names, ", "))
		}
	}
	return strings.Join(parts, " • ")
}

func resolveAudioResumePosition(ctx context.Context, provider userstore.UserStoreProvider, userID int, profileID, contentID string) float64 {
	if provider == nil || userID == 0 || profileID == "" {
		return 0
	}
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		return 0
	}
	progress, err := store.GetProgress(ctx, profileID, contentID)
	if err != nil || progress == nil || progress.Completed {
		return 0
	}
	if progress.PositionSeconds < 30 {
		return 0
	}
	if progress.DurationSeconds > 0 && progress.PositionSeconds >= progress.DurationSeconds-5 {
		return 0
	}
	return progress.PositionSeconds
}

func isFiniteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func clampFloat(value, lower, upper float64) float64 {
	if !isFiniteNonNegative(value) {
		return lower
	}
	if value < lower {
		return lower
	}
	if upper > lower && value > upper {
		return upper
	}
	return value
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

type PGAudioBookmarkStore struct {
	Pool *pgxpool.Pool
}

func NewPGAudioBookmarkStore(pool *pgxpool.Pool) *PGAudioBookmarkStore {
	if pool == nil {
		return nil
	}
	return &PGAudioBookmarkStore{Pool: pool}
}

func (s *PGAudioBookmarkStore) List(ctx context.Context, userID int, profileID, contentID string) ([]audioBookmark, error) {
	if s == nil || s.Pool == nil {
		return []audioBookmark{}, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, library_item_id, time_seconds, title, created_at, updated_at
		FROM abs_bookmarks
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND library_item_id = $3
		ORDER BY time_seconds ASC`,
		userID, profileArg(profileID), contentID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return []audioBookmark{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bookmarks := []audioBookmark{}
	for rows.Next() {
		var createdAt, updatedAt time.Time
		var bookmark audioBookmark
		if err := rows.Scan(&bookmark.ID, &bookmark.ContentID, &bookmark.TimeSeconds, &bookmark.Title, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		bookmark.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		bookmark.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		bookmarks = append(bookmarks, bookmark)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bookmarks, nil
}

func (s *PGAudioBookmarkStore) Upsert(ctx context.Context, userID int, profileID, contentID string, timeSeconds float64, title string) (audioBookmark, error) {
	if s == nil || s.Pool == nil {
		return audioBookmark{}, fmt.Errorf("bookmark store unavailable")
	}
	id := ulid.Make().String()
	var createdAt, updatedAt time.Time
	var bookmark audioBookmark
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO abs_bookmarks
		  (id, user_id, profile_id, library_item_id, time_seconds, title)
		VALUES ($1, $2, $3::uuid, $4, $5, $6)
		ON CONFLICT (
		    user_id,
		    COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid),
		    library_item_id,
		    time_seconds
		) DO UPDATE
		   SET title = EXCLUDED.title,
		       updated_at = now()
		RETURNING id, library_item_id, time_seconds, title, created_at, updated_at`,
		id, userID, profileArg(profileID), contentID, timeSeconds, strings.TrimSpace(title),
	).Scan(&bookmark.ID, &bookmark.ContentID, &bookmark.TimeSeconds, &bookmark.Title, &createdAt, &updatedAt)
	if err != nil {
		return audioBookmark{}, err
	}
	bookmark.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	bookmark.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return bookmark, nil
}

func (s *PGAudioBookmarkStore) Delete(ctx context.Context, userID int, profileID, contentID string, timeSeconds float64) error {
	if s == nil || s.Pool == nil {
		return fmt.Errorf("bookmark store unavailable")
	}
	_, err := s.Pool.Exec(ctx, `
		DELETE FROM abs_bookmarks
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND library_item_id = $3
		  AND time_seconds = $4`,
		userID, profileArg(profileID), contentID, timeSeconds,
	)
	return err
}

func profileArg(profileID string) any {
	if strings.TrimSpace(profileID) == "" {
		return nil
	}
	return profileID
}
