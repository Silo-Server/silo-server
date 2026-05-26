package abs

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/models"
)

// handlePlayStart handles POST /abs/api/items/{libraryItemId}/play.
//
// Real ABS clients hit this endpoint to start a playback session and get back
// a manifest of audio tracks with signed contentUrls. The response includes
// the full playbackSession shape the mobile player reads to seed the audio
// element: currentTime (resume position), audioTracks, mediaMetadata,
// libraryItem, chapters, etc.
//
// Silo's implementation:
//   - Looks up the audiobook via MediaStore.GetAudiobookByID.
//   - Loads all media files via MediaStore.GetMediaFiles (sorted by ID ASC).
//   - Synthesises a ULID session ID (in-memory; no play-session table yet).
//   - Builds contentUrls that point at this handler's sibling handleFileStream
//     endpoint, qualified by the ABS bearer token in ?token= so the iOS audio
//     element can load the URL without extra request headers.
//   - Returns a JSON playbackSession matching the shape real ABS emits, with
//     enough fields populated that the official mobile client plays without
//     entering "spinner forever" mode.
func (h *Handler) handlePlayStart(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	contentID := chi.URLParam(r, "libraryItemId")

	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), contentID)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	files, err := h.deps.MediaStore.GetMediaFiles(r.Context(), contentID)
	if err != nil {
		http.Error(w, "load files failed", http.StatusInternalServerError)
		return
	}

	// The {ino} parameter used by handleFileStream is a 0-based file index, but
	// we want iOS clients to resolve it via the stable MD5 derivation. Emit inos
	// that handleFileStream can reverse without a database lookup.
	baseURL := h.absBaseURL(r)
	// The bearer token from the ABS JWT is re-embedded in the content URL so
	// that iOS's AVPlayer (which can't add Authorization headers on audio
	// subrequests) can authenticate via ?token=.
	accessToken := a.Token

	sessionID := ulid.Make().String()

	// Persist the session row so subsequent PATCH /session/{sid} heartbeats
	// and POST /session/{sid}/close can find it. Without this, the session
	// ID is returned to the client but every sync/close lookup 404s.
	if h.deps.PlaybackSessionStore != nil {
		sess := ABSPlaybackSession{
			ID:        sessionID,
			UserID:    a.UserID,
			ProfileID: a.ProfileID,
			ContentID: contentID,
		}
		if len(files) > 0 {
			fid := files[0].ID
			sess.MediaFileID = &fid
		}
		if err := h.deps.PlaybackSessionStore.InsertPlaybackSession(r.Context(), sess); err != nil {
			// Non-fatal: log but still return the manifest so the client
			// can play. Heartbeat/close calls will fail with 404 until the
			// next play_start lands cleanly.
			slog.Warn("abs play: persist session failed",
				"session_id", sessionID, "content_id", contentID, "error", err)
		}
	}

	audioTracks := buildSiloAudioTracks(contentID, files, baseURL, sessionID, accessToken)

	totalDuration := float64(0)
	for _, t := range audioTracks {
		totalDuration += t.Duration
	}

	chapters := buildSiloChapters(files)

	mediaMetadata := buildSiloPlayMediaMetadata(item)
	libraryItem := buildSiloPlayLibraryItem(item, contentID, mediaMetadata, audioTracks, chapters, totalDuration, baseURL)

	displayTitle := item.Title
	displayAuthor := ""
	if v, ok := mediaMetadata["authorName"].(string); ok {
		displayAuthor = v
	}

	now := time.Now()
	nowMs := now.UnixMilli()
	dateStr := now.UTC().Format("2006-01-02")
	dayOfWeek := now.UTC().Weekday().String()

	// currentTime seeds the audio element's initial position so cross-device
	// resume works. Lookup is best-effort: any error returns position 0,
	// which is always correct for a first listen.
	var currentTime float64
	currentTime, err = resolveResumeTime(r.Context(), h.deps.ProgressStore, a.UserID, a.ProfileID, contentID)
	if err != nil {
		slog.Debug("play: progress lookup failed", "user", a.UserID, "item", contentID, "err", err)
		// currentTime is already 0 on error path; safe to continue.
	}

	playbackSession := map[string]any{
		"id":            sessionID,
		"userId":        a.UserID,
		"libraryId":     VirtualLibraryID,
		"libraryItemId": contentID,
		"bookId":        contentID,
		"episodeId":     nil,
		"mediaType":     LibraryMediaType,
		"mediaMetadata": mediaMetadata,
		"chapters":      chapters,
		"displayTitle":  displayTitle,
		"displayAuthor": displayAuthor,
		"coverPath":     baseURL + "/api/items/" + contentID + "/cover",
		"duration":      totalDuration,
		"playMethod":    0, // DIRECTPLAY
		"mediaPlayer":   "exo-player",
		"deviceInfo": map[string]any{
			"deviceId":      "unknown",
			"manufacturer":  "Unknown",
			"model":         "Unknown",
			"sdkVersion":    0,
			"clientVersion": "0.0.0",
		},
		"serverVersion": ServerVersion,
		"date":          dateStr,
		"dayOfWeek":     dayOfWeek,
		"timeListening": 0,
		"startTime":     currentTime,
		"currentTime":   currentTime,
		"startedAt":     nowMs,
		"updatedAt":     nowMs,
		"audioTracks":   audioTracks,
		"libraryItem":   libraryItem,
	}

	writeJSON(w, http.StatusOK, playbackSession)
}

// buildSiloAudioTracks converts silo media_files into the ABS AudioTrack
// slice. Each track's contentUrl points at handleFileStream with the ABS
// bearer token appended so the iOS audio element can load without extra
// headers.
func buildSiloAudioTracks(
	contentID string,
	files []*models.MediaFile,
	baseURL string,
	_ string, // sessionID reserved for future /public/session/ path
	accessToken string,
) []AudioTrack {
	tracks := make([]AudioTrack, 0, len(files))

	startOffset := float64(0)
	for i, f := range files {
		ino := trackInoFor(contentID, i)
		ext := strings.ToLower(filepath.Ext(f.FilePath))
		format := strings.TrimPrefix(ext, ".")
		mimeType := audioContentType(ext)
		if mimeType == "" {
			mimeType = "audio/mpeg"
		}

		filename := filepath.Base(f.FilePath)

		// ABS wire index is 1-based to match the real server's convention.
		// Our ino uses 0-based internally; handleFileStream resolves via ino.
		wireIndex := i + 1

		// contentUrl: use /abs/api path so both standalone and host-proxied
		// mounts resolve correctly. The ?token= appended here lets iOS
		// AVPlayer (which cannot set Authorization headers on media loads)
		// authenticate via the query param fallback in bearerAuth.
		contentURL := baseURL + "/abs/api/items/" + contentID + "/file/" + ino
		if accessToken != "" {
			contentURL += "?token=" + accessToken
		}

		duration := float64(f.Duration) // f.Duration is in seconds (int)

		var bitRate int
		if f.Bitrate > 0 {
			bitRate = f.Bitrate * 1000 // kbps → bps
		} else {
			bitRate = 128000
		}

		channels := f.AudioChannels
		if channels == 0 {
			channels = 2
		}

		codec := f.CodecAudio
		channelLayout := "stereo"
		if channels > 2 {
			channelLayout = "surround"
		}

		nowMs := time.Now().UnixMilli()

		track := AudioTrack{
			Index: wireIndex,
			Ino:   ino,
			Metadata: &AudioTrackMetadata{
				Filename:    filename,
				Ext:         ext,
				Path:        f.FilePath,
				RelPath:     filename,
				Size:        f.FileSize,
				MtimeMs:     0,
				CtimeMs:     0,
				BirthtimeMs: 0,
			},
			AddedAt:              nowMs,
			UpdatedAt:            nowMs,
			TrackNumFromMeta:     nil,
			DiscNumFromMeta:      nil,
			TrackNumFromFilename: nil,
			DiscNumFromFilename:  nil,
			ManuallyVerified:     false,
			Exclude:              false,
			Error:                nil,
			Format:               format,
			Duration:             duration,
			BitRate:              bitRate,
			Language:             nil,
			Codec:                codec,
			TimeBase:             "1/14112000",
			Channels:             channels,
			ChannelLayout:        channelLayout,
			Chapters:             nil,
			EmbeddedCoverArt:     nil,
			MimeType:             mimeType,
			Title:                filename,
			StartOffset:          startOffset,
			ContentURL:           contentURL,
		}

		tracks = append(tracks, track)
		startOffset += duration
	}

	return tracks
}

// buildSiloChapters extracts chapters from the first media file that has them.
// ABS expects chapters as a flat list spanning the whole book; for multi-file
// audiobooks we only use the first file's chapters (most single-file M4B
// audiobooks have embedded chapters; multi-MP3 sets rarely do).
func buildSiloChapters(files []*models.MediaFile) []map[string]any {
	for _, f := range files {
		if len(f.Chapters) == 0 {
			continue
		}
		chapters := make([]map[string]any, 0, len(f.Chapters))
		for i, c := range f.Chapters {
			chapters = append(chapters, map[string]any{
				"id":    i,
				"start": c.StartSeconds,
				"end":   c.EndSeconds,
				"title": c.Title,
			})
		}
		return chapters
	}
	return []map[string]any{}
}

// buildSiloPlayMediaMetadata builds the playbackSession.mediaMetadata object
// from a silo MediaItem. The mobile player reads this for the "Now Playing"
// widget and playback history; missing keys cause the audio loader to abort.
func buildSiloPlayMediaMetadata(item *models.MediaItem) map[string]any {
	title := item.Title

	authors := make([]map[string]any, 0)
	authorNames := make([]string, 0)
	narrators := make([]string, 0)
	for _, p := range item.People {
		switch p.Kind {
		case models.PersonKindAuthor:
			authors = append(authors, map[string]any{
				"id":   strconv.FormatInt(p.ID, 10),
				"name": p.Name,
			})
			authorNames = append(authorNames, p.Name)
		case models.PersonKindNarrator:
			narrators = append(narrators, p.Name)
		}
	}

	authorName := strings.Join(authorNames, ", ")
	lastFirsts := make([]string, len(authorNames))
	for i, n := range authorNames {
		lastFirsts[i] = toLastFirst(n)
	}
	authorNameLF := strings.Join(lastFirsts, ", ")

	publishedYear := ""
	if item.Year > 0 {
		publishedYear = strconv.Itoa(item.Year)
	}

	genres := item.Genres
	if genres == nil {
		genres = []string{}
	}

	return map[string]any{
		"title":             title,
		"titleIgnorePrefix": titleIgnorePrefix(title),
		"subtitle":          nil,
		"authors":           authors,
		"authorName":        authorName,
		"authorNameLF":      authorNameLF,
		"narrators":         narrators,
		"narratorName":      strings.Join(narrators, ", "),
		"series":            []any{},
		"seriesName":        "",
		"genres":            genres,
		"tags":              []string{},
		"publishedYear":     publishedYear,
		"publishedDate":     nil,
		"publisher":         nil,
		"description":       nilIfEmpty(item.Overview),
		"descriptionPlain":  nilIfEmpty(stripHTML(item.Overview)),
		"isbn":              nil,
		"asin":              nil,
		"language":          "en",
		"explicit":          false,
		"abridged":          false,
	}
}

// buildSiloPlayLibraryItem builds the playbackSession.libraryItem nested
// object. The mobile player reads libraryItem.media.tracks /
// libraryItem.media.metadata / libraryItem.libraryFiles for offline download
// decisions and UI rendering.
func buildSiloPlayLibraryItem(
	item *models.MediaItem,
	contentID string,
	mediaMetadata map[string]any,
	audioTracks []AudioTrack,
	chapters []map[string]any,
	totalDuration float64,
	baseURL string,
) map[string]any {
	firstIno := contentID
	if len(audioTracks) > 0 {
		firstIno = audioTracks[0].Ino
	}

	totalSize := int64(0)
	libraryFiles := make([]map[string]any, 0, len(audioTracks))
	for _, t := range audioTracks {
		nowMs := time.Now().UnixMilli()
		libraryFiles = append(libraryFiles, map[string]any{
			"ino":             t.Ino,
			"metadata":        t.Metadata,
			"isSupplementary": false,
			"addedAt":         nowMs,
			"updatedAt":       nowMs,
			"fileType":        "audio",
		})
		if t.Metadata != nil {
			totalSize += t.Metadata.Size
		}
	}

	addedAtMs := int64(0)
	if item.AddedAt != nil {
		addedAtMs = item.AddedAt.UnixMilli()
	}
	updatedAtMs := item.UpdatedAt.UnixMilli()

	return map[string]any{
		"id":               contentID,
		"ino":              firstIno,
		"oldLibraryItemId": nil,
		"libraryId":        VirtualLibraryID,
		"folderId":         VirtualFolderID,
		"path":             contentID,
		"relPath":          contentID,
		"isFile":           true,
		"mtimeMs":          nil,
		"ctimeMs":          nil,
		"birthtimeMs":      nil,
		"addedAt":          addedAtMs,
		"updatedAt":        updatedAtMs,
		"lastScan":         addedAtMs,
		"scanVersion":      ServerVersion,
		"isMissing":        false,
		"isInvalid":        false,
		"mediaType":        LibraryMediaType,
		"media": map[string]any{
			"id":            contentID,
			"libraryItemId": contentID,
			"metadata":      mediaMetadata,
			"coverPath":     baseURL + "/api/items/" + contentID + "/cover",
			"tags":          []any{},
			"audioFiles":    audioTracks,
			"chapters":      chapters,
			"ebookFile":     nil,
			"duration":      totalDuration,
			"size":          totalSize,
			"tracks":        audioTracks,
		},
		"libraryFiles": libraryFiles,
		// size on the outer libraryItem is a STRING in real ABS wire format —
		// mobile sort comparators string-compare it.
		"size": strconv.FormatInt(totalSize, 10),
	}
}

// ---------------------------------------------------------------------------
// Play response helpers
// ---------------------------------------------------------------------------

// nilIfEmpty returns nil when s is blank, otherwise the string itself.
// This mirrors the plugin's pattern: ABS clients treat null and ""
// differently on some fields (publisher, description, isbn, etc.).
func nilIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// titleIgnorePrefix strips leading articles for sort-key purposes,
// matching real ABS LibraryItemController behaviour.
func titleIgnorePrefix(title string) string {
	lower := strings.ToLower(title)
	for _, p := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, p) {
			return title[len(p):]
		}
	}
	return title
}

// toLastFirst converts "First Last" → "Last, First" for the authorNameLF
// field the mobile "Now Playing" widget renders.
func toLastFirst(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	return parts[len(parts)-1] + ", " + strings.Join(parts[:len(parts)-1], " ")
}

// stripHTML removes HTML angle-bracket tags from a description so the mobile
// "Now Playing" body renderer doesn't have to. Cheap and good enough for
// the descriptions silo metadata sources produce.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			if !unicode.IsControl(r) {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// resolveResumeTime returns the persisted currentTime for (userID, profileID,
// contentID) from the progress store, or 0 when no row exists / store is nil.
// Returned error is propagated so callers can log it; the caller is expected
// to fall back to 0 on error (a fresh-listen start is always correct).
func resolveResumeTime(ctx context.Context, store ProgressStore, userID, profileID, contentID string) (float64, error) {
	if store == nil {
		return 0, nil
	}
	row, err := store.GetProgress(ctx, userID, profileID, contentID)
	if err != nil {
		return 0, err
	}
	if row == nil {
		return 0, nil
	}
	return row.CurrentSeconds, nil
}
