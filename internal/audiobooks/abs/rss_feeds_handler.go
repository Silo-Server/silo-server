package abs

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/catalog"
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
		slog.ErrorContext(r.Context(), "abs feed list failed", "component", "audiobooks", "err", err, "user", a.UserID)
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
	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID, access)
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
		slog.ErrorContext(r.Context(), "abs feed create failed", "component", "audiobooks", "err", err, "user", a.UserID)
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
	if errors.Is(err, ErrNotFound) || (err == nil && !sameABSPrincipal(a, f.UserID, f.ProfileID)) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "abs feed get-for-close failed", "component", "audiobooks", "err", err, "id", id)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.RSSFeedStore.CloseFeed(r.Context(), id); err != nil {
		slog.ErrorContext(r.Context(), "abs feed close failed", "component", "audiobooks", "err", err, "id", id)
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
	slug := strings.TrimSuffix(chi.URLParam(r, "slug"), ".xml")
	f, access, ok := h.resolvePublicFeed(w, r, slug)
	if !ok {
		return
	}
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), f.LibraryItemID, access)
	if err != nil {
		slog.ErrorContext(r.Context(), "abs public feed item failed", "component", "audiobooks", "err", err, "feed", f.ID)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}
	if item == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	files, ok := h.publicFeedFiles(w, r, f, access)
	if !ok {
		return
	}
	xml := renderFeedXML(f, item, files, h.absBaseURL(r))
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml))
}

// resolvePublicFeed loads the open feed for slug and its owner's current
// access. The slug grants no access of its own: the feed serves only what its
// stored owner and profile could fetch through the authenticated routes today.
// A closed or unknown feed, an unavailable owner, and an item the owner can no
// longer see all answer the same 404, so the response does not reveal which
// applies. On false the response is written.
func (h *Handler) resolvePublicFeed(w http.ResponseWriter, r *http.Request, slug string) (RSSFeed, catalog.AccessFilter, bool) {
	ctx := r.Context()
	if h.deps.RSSFeedStore == nil || h.deps.AccessResolver == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return RSSFeed{}, catalog.AccessFilter{}, false
	}
	f, err := h.deps.RSSFeedStore.GetFeedBySlug(ctx, slug)
	if errors.Is(err, ErrNotFound) || (err == nil && f.ClosedAt != nil) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return RSSFeed{}, catalog.AccessFilter{}, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "abs public feed get failed", "component", "audiobooks", "err", err, "slug", slug)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return RSSFeed{}, catalog.AccessFilter{}, false
	}

	access, err := h.deps.AccessResolver.ResolveABSOwnerAccess(ctx, f.UserID, f.ProfileID)
	if errors.Is(err, ErrOwnerUnavailable) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return RSSFeed{}, catalog.AccessFilter{}, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "abs public feed owner access failed", "component", "audiobooks", "err", err, "feed", f.ID)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return RSSFeed{}, catalog.AccessFilter{}, false
	}
	return f, access, true
}

// publicFeedFiles returns the feed item's files that pass the owner's access.
// On false the response is written.
func (h *Handler) publicFeedFiles(w http.ResponseWriter, r *http.Request, f RSSFeed, access catalog.AccessFilter) ([]*models.MediaFile, bool) {
	files, err := h.deps.MediaStore.GetMediaFiles(r.Context(), f.LibraryItemID, access)
	if err != nil {
		slog.ErrorContext(r.Context(), "abs public feed files failed", "component", "audiobooks", "err", err, "feed", f.ID)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return nil, false
	}
	return files, true
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

// handlePublicFeedFile — GET /feed/{slug}/file/{ino}. Streams the media file
// when the feed is open and the ino is one of the files the feed lists, so
// the file passes the owner's current access.
func (h *Handler) handlePublicFeedFile(w http.ResponseWriter, r *http.Request) {
	f, access, ok := h.resolvePublicFeed(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	ino, parseErr := strconv.Atoi(chi.URLParam(r, "ino"))
	if parseErr != nil {
		http.Error(w, "invalid ino", http.StatusBadRequest)
		return
	}
	// GetMediaFiles returns nothing for an item the owner can no longer see,
	// which answers like a closed feed.
	files, ok := h.publicFeedFiles(w, r, f, access)
	if !ok {
		return
	}
	if len(files) == 0 {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	idx := slices.IndexFunc(files, func(mf *models.MediaFile) bool { return mf.ID == ino })
	if idx < 0 {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	mf := files[idx]
	// §4.2b: "the RSS feed route must resolve the feed owner". The slug is the
	// capability and there is no authenticated caller, so the subject is the
	// feed's owner rather than whoever fetched it.
	attachABSTransfer(r.Context(), f.UserID, f.ProfileID, mf.ID)
	http.ServeFile(w, r, mf.FilePath)
}
