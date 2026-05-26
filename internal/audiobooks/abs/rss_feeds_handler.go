package abs

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/models"
)

var slugRe = regexp.MustCompile(`^[a-z0-9-]{4,64}$`)

type feedOpenBody struct {
	Slug     string `json:"slug"`
	Minified bool   `json:"minified"`
}

func (h *Handler) handleListRSSFeeds(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"feeds": []any{}})
		return
	}
	rows, err := h.deps.RSSFeedStore.ListUserFeeds(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs feed list failed", "err", err, "user", a.UserID)
		http.Error(w, "feed list failed", http.StatusInternalServerError)
		return
	}
	base := h.absBaseURL(r)
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		out = append(out, rssFeedToABS(f, base))
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": out})
}

func (h *Handler) handleOpenItemFeed(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed store unavailable", http.StatusServiceUnavailable)
		return
	}
	itemID := chi.URLParam(r, "itemId")
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	var body feedOpenBody
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)

	slug := strings.ToLower(strings.TrimSpace(body.Slug))
	if slug == "" {
		slug = randomSlug()
	} else if !slugRe.MatchString(slug) {
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}

	f := RSSFeed{
		ID:            ulid.Make().String(),
		UserID:        a.UserID,
		ProfileID:     a.ProfileID,
		LibraryItemID: itemID,
		Slug:          slug,
		Minified:      body.Minified,
	}
	if err := h.deps.RSSFeedStore.CreateFeed(r.Context(), f); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique") {
			http.Error(w, "slug taken", http.StatusConflict)
			return
		}
		slog.Error("abs feed create failed", "err", err, "user", a.UserID)
		http.Error(w, "feed persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.RSSFeedStore.GetFeed(r.Context(), f.ID)
	if errors.Is(err, ErrNotFound) || err != nil {
		f.CreatedAt = time.Now()
		persisted = f
	}
	writeJSON(w, http.StatusOK, rssFeedToABS(persisted, h.absBaseURL(r)))
}

func (h *Handler) handleCloseFeed(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	id := chi.URLParam(r, "id")
	f, err := h.deps.RSSFeedStore.GetFeed(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && f.UserID != a.UserID) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs feed get-for-close failed", "err", err, "id", id)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.RSSFeedStore.CloseFeed(r.Context(), id); err != nil {
		slog.Error("abs feed close failed", "err", err, "id", id)
		http.Error(w, "feed close failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func randomSlug() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf)
}

// handlePublicFeed — GET /feed/{slug}.xml and GET /feed/{slug}.
// Public, no auth. The slug is the capability token.
func (h *Handler) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	slug := strings.TrimSuffix(chi.URLParam(r, "slug"), ".xml")
	f, err := h.deps.RSSFeedStore.GetFeedBySlug(r.Context(), slug)
	if errors.Is(err, ErrNotFound) || (err == nil && f.ClosedAt != nil) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs public feed get failed", "err", err, "slug", slug)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}

	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), f.LibraryItemID)
	if err != nil || item == nil {
		http.Error(w, "feed item not found", http.StatusNotFound)
		return
	}
	files, _ := h.deps.MediaStore.GetMediaFiles(r.Context(), f.LibraryItemID)

	base := h.absBaseURL(r)
	xml := renderFeedXML(f, item, files, base)
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml))
}

// renderFeedXML builds a minimal RSS 2.0 + iTunes document.
func renderFeedXML(f RSSFeed, item *models.MediaItem, files []*models.MediaFile, baseURL string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">` + "\n")
	b.WriteString("<channel>\n")
	b.WriteString("<title>" + xmlEscape(item.Title) + "</title>\n")
	b.WriteString("<link>" + xmlEscape(baseURL+"/feed/"+f.Slug+".xml") + "</link>\n")
	b.WriteString("<description>silo audiobook feed</description>\n")
	for _, mf := range files {
		enc := baseURL + "/feed/" + f.Slug + "/file/" + strconv.Itoa(mf.ID)
		b.WriteString("<item>\n")
		b.WriteString("<title>" + xmlEscape(item.Title) + "</title>\n")
		b.WriteString(`<enclosure url="` + xmlEscape(enc) + `" type="audio/mpeg" length="0"/>` + "\n")
		b.WriteString("<guid>" + xmlEscape(f.Slug+"-"+strconv.Itoa(mf.ID)) + "</guid>\n")
		b.WriteString("</item>\n")
	}
	b.WriteString("</channel>\n")
	b.WriteString("</rss>\n")
	return b.String()
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// handlePublicFeedFile — GET /feed/{slug}/file/{ino}. Streams the
// media file when the slug is valid + open and the ino belongs to
// the underlying library item.
func (h *Handler) handlePublicFeedFile(w http.ResponseWriter, r *http.Request) {
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	slug := chi.URLParam(r, "slug")
	f, err := h.deps.RSSFeedStore.GetFeedBySlug(r.Context(), slug)
	if errors.Is(err, ErrNotFound) || (err == nil && f.ClosedAt != nil) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}
	inoStr := chi.URLParam(r, "ino")
	ino, parseErr := strconv.Atoi(inoStr)
	if parseErr != nil {
		http.Error(w, "invalid ino", http.StatusBadRequest)
		return
	}
	mf, mfErr := h.deps.MediaStore.GetMediaFileByID(r.Context(), ino)
	if mfErr != nil || mf == nil || mf.ContentID != f.LibraryItemID {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, mf.FilePath)
}
