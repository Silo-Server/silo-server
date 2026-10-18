package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeWatch is the watch seam: one playable movie, one series (not directly
// playable), everything else unknown.
type fakeWatch struct {
	filters   []catalogpkg.AccessFilter
	marks     []fakeMark
	clears    []fakeMark
	err       error
	trickplay *catalogpkg.TrickplayGrid
}

type fakeMark struct {
	userID    int
	profileID string
	contentID string
	played    bool
}

func (f *fakeWatch) ContextAccessFilter(_ context.Context, opts handlers.AccessFilterOptions) (catalogpkg.AccessFilter, error) {
	filter := catalogpkg.AccessFilter{SelectedFileID: opts.SelectedFileID, PresentationLibraryID: opts.PresentationLibraryID, ImageSize: opts.ImageSize, DeviceID: opts.DeviceID}
	f.filters = append(f.filters, filter)
	return filter, nil
}

func (f *fakeWatch) WatchDetail(_ context.Context, userID int, profileID, contentID string, _ catalogpkg.AccessFilter) (*catalogpkg.WatchDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	switch contentID {
	case "series:heat":
		return nil, &handlers.APIError{Status: http.StatusBadRequest, Code: "invalid_watch_target", Message: "Content is not directly playable"}
	case "movie:heat-1995":
		three := 3
		detail := &catalogpkg.WatchDetail{
			ContentID: contentID, Type: "movie", Title: "Heat", Year: 1995,
			EffectiveSubtitleLanguage: "eng", HasEffectiveSubtitleLang: true,
			PosterURL: "/api/images/poster/heat.jpg?size=medium", PosterThumbhash: "poster-hash",
			BackdropURL: "/api/images/backdrop/heat.jpg?size=medium", BackdropThumbhash: "backdrop-hash",
			Versions: []catalogpkg.FileVersion{{
				FileID: 42, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "eac3", Container: "mkv", FileSize: 1024, Duration: 10200, Bitrate: 8000000,
				AddedAt:        time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				VideoTracks:    []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
				AudioTracks:    []models.AudioTrack{{Language: "eng", Codec: "eac3", Channels: 6, Default: true}},
				Chapters:       []catalogpkg.VersionChapter{{Index: 1, Title: "Opening", StartSeconds: 0, EndSeconds: 300, Source: "embedded"}},
				Intro:          &catalogpkg.Marker{Start: 0, End: 90},
				MarkerPreviews: []catalogpkg.VersionMarkerPreview{{MarkerSegment: models.MarkerSegment{Kind: "intro", StartSeconds: 0, EndSeconds: 90}, ThumbnailURL: "https://images.example.test/intro.webp", ThumbnailThumbhash: "AQID", ThumbnailCaptureSeconds: 0}},
				Trickplay:      f.trickplay,
			}},
			PlaybackVariants: []catalogpkg.PlaybackVariant{{VariantID: "v1", PartCount: 1, DefaultFileID: 42, Parts: []catalogpkg.PlaybackVariantPart{{PartIndex: 0, DefaultFileID: 42}}}},
			Subtitles:        []catalogpkg.SubtitleInfo{{Source: "embedded", Language: "eng"}},
			Credits:          &catalogpkg.Marker{Start: 10000, End: 10200},
		}
		if profileID != "" {
			detail.UserData = &catalogpkg.SeasonUserData{PositionSeconds: 1325.5, DurationSeconds: 10200, IsInProgress: true, LastFileID: &three}
		}
		_ = userID
		return detail, nil
	}
	return nil, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Watch target not found"}
}

func (f *fakeWatch) SetWatchedState(_ context.Context, userID int, profileID, contentID string, played bool, _ catalogpkg.AccessFilter) (handlers.WatchedStateView, error) {
	if f.err != nil {
		return handlers.WatchedStateView{}, f.err
	}
	if contentID != "movie:heat-1995" && contentID != "series:heat" {
		return handlers.WatchedStateView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	f.marks = append(f.marks, fakeMark{userID: userID, profileID: profileID, contentID: contentID, played: played})
	return handlers.WatchedStateView{ContentID: contentID, Type: "movie", AffectedCount: 1, Played: played}, nil
}

func (f *fakeWatch) ClearResumePosition(_ context.Context, userID int, profileID, contentID string, _ catalogpkg.AccessFilter) error {
	if f.err != nil {
		return f.err
	}
	switch contentID {
	case "series:heat":
		return &handlers.APIError{Status: http.StatusBadRequest, Code: "invalid_watch_target", Message: "Only a movie or an episode has a resume position"}
	case "movie:heat-1995":
		f.clears = append(f.clears, fakeMark{userID: userID, profileID: profileID, contentID: contentID})
		return nil
	}
	return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
}

func watchDeps(watch *fakeWatch) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.Watch = watch
	return deps
}

func TestGetWatchState(t *testing.T) {
	watch := &fakeWatch{trickplay: &catalogpkg.TrickplayGrid{Width: 300}}
	h := newTestHandler(t, watchDeps(watch))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995?file_id=42&library_id=1&image_size=medium", "", owner)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["content_id"] != "movie:heat-1995" || body["effective_subtitle_language"] != "eng" || body["effective_subtitle_mode"] != nil {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// The artwork system media controls show (#803).
	if body["poster_url"] != "/api/images/poster/heat.jpg?size=medium" || body["poster_thumbhash"] != "poster-hash" ||
		body["backdrop_url"] != "/api/images/backdrop/heat.jpg?size=medium" || body["backdrop_thumbhash"] != "backdrop-hash" {
		t.Fatalf("artwork in body = %s", rec.Body.String())
	}
	versions := body["versions"].([]any)
	v := versions[0].(map[string]any)
	if v["file_id"] != "42" || v["duration_seconds"].(float64) != 10200 || v["added_at"] != "2026-01-02T03:04:05.000Z" || v["intro"].(map[string]any)["end_seconds"].(float64) != 90 || v["trickplay_available"] != true {
		t.Fatalf("version = %v", v)
	}
	if variant := body["playback_variants"].([]any)[0].(map[string]any); variant["default_file_id"] != "42" || variant["parts"].([]any)[0].(map[string]any)["versions"] == nil {
		t.Fatalf("variant = %v", variant)
	}
	if ud := body["user_data"].(map[string]any); ud["last_file_id"] != "3" || ud["position_seconds"].(float64) != 1325.5 || ud["played"] != false {
		t.Fatalf("user_data = %v", ud)
	}
	// The query reached the access filter.
	last := watch.filters[len(watch.filters)-1]
	if last.SelectedFileID != 42 || last.PresentationLibraryID == nil || *last.PresentationLibraryID != 1 || string(last.ImageSize) != "medium" || last.DeviceID != "" {
		t.Fatalf("filter = %+v", last)
	}

	// The profile header is optional: an account-only caller gets the
	// catalog answer without user_data.
	watch.trickplay = nil
	rec = do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	body = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["user_data"]; ok {
		t.Fatalf("user_data present without a profile: %s", rec.Body.String())
	}
	if v := body["versions"].([]any)[0].(map[string]any); v["trickplay_available"] != false {
		t.Fatalf("version without previews = %v", v)
	}
	if subs, ok := body["subtitles"].([]any); !ok || len(subs) != 1 {
		t.Fatalf("subtitles = %v", body["subtitles"])
	}
}

func TestGetWatchStateRejects(t *testing.T) {
	h := newTestHandler(t, watchDeps(&fakeWatch{}))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/series:heat", "", owner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "path.id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:missing", "", owner), TypeNotFound)
	p = requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995?file_id=abc", "", owner), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.file_id" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995?image_size=huge", "", owner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995?fileId=1", "", owner), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995", "", nil), TypeAuthenticationRequired)

	off := newTestHandler(t, parityDeps(false))
	requireProblem(t, do(t, off, http.MethodGet, "/api/v2/watch/movie:heat-1995", "", owner), TypeDependencyUnavailable)
}

func TestMarkWatched(t *testing.T) {
	watch := &fakeWatch{}
	h := newTestHandler(t, watchDeps(watch))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")

	rec := do(t, h, http.MethodPost, "/api/v2/watched/series:heat", "", owner)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodDelete, "/api/v2/watched/movie:heat-1995", "", owner)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(watch.marks) != 2 || watch.marks[0] != (fakeMark{userID: 1, profileID: "p-owner", contentID: "series:heat", played: true}) ||
		watch.marks[1] != (fakeMark{userID: 1, profileID: "p-owner", contentID: "movie:heat-1995", played: false}) {
		t.Fatalf("marks = %+v", watch.marks)
	}

	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/watched/movie:missing", "", owner), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/watched/movie:missing", "", owner), TypeNotFound)
	// The mark needs a profile, as v1's RequireProfile group.
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/watched/movie:heat-1995", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodDelete, "/api/v2/watched/movie:heat-1995", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/watched/movie:heat-1995", "", nil), TypeAuthenticationRequired)

	watch.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to update watched state"}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/watched/movie:heat-1995", "", owner), TypeInternalError)

	off := newTestHandler(t, parityDeps(false))
	requireProblem(t, do(t, off, http.MethodPost, "/api/v2/watched/movie:heat-1995", "", owner), TypeDependencyUnavailable)
}

func TestClearResumePosition(t *testing.T) {
	watch := &fakeWatch{}
	h := newTestHandler(t, watchDeps(watch))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	path := Prefix + "/progress/movie:heat-1995/position"

	rec := do(t, h, http.MethodDelete, path, "", owner)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(watch.clears) != 1 || watch.clears[0] != (fakeMark{userID: 1, profileID: "p-owner", contentID: "movie:heat-1995"}) {
		t.Fatalf("clears = %+v", watch.clears)
	}

	// A season or series has no single resume position to clear.
	problem := requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/progress/series:heat/position", "", owner), TypeValidationFailed)
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "path.media_item_id" {
		t.Fatalf("errors = %+v", problem.Errors)
	}
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/progress/movie:missing/position", "", owner), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	if len(watch.clears) != 1 {
		t.Fatalf("rejected requests cleared progress: %+v", watch.clears)
	}

	watch.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to clear resume position"}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", owner), TypeInternalError)

	off := newTestHandler(t, parityDeps(false))
	requireProblem(t, do(t, off, http.MethodDelete, path, "", owner), TypeDependencyUnavailable)
}

func TestGetProgressCapabilities(t *testing.T) {
	path := Prefix + "/progress/capabilities"
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	h := newTestHandler(t, watchDeps(&fakeWatch{}))
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeAuthenticationRequired)

	rec := do(t, h, http.MethodGet, path, "", owner)
	var out ProgressCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || out.State != StateAvailable || out.Allowed == nil || !*out.Allowed || out.Revision == "" || !out.ClearResumePosition {
		t.Fatalf("capabilities: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != cachePrivateNoCache {
		t.Fatalf("Cache-Control = %q", got)
	}
	tag := rec.Header().Get("ETag")
	if cached := do(t, h, http.MethodGet, path, "", with(owner, "If-None-Match", tag)); tag == "" || cached.Code != http.StatusNotModified {
		t.Fatalf("revalidation with %q: %d", tag, cached.Code)
	}

	deps := watchDeps(&fakeWatch{})
	deps.Watch = nil
	rec = do(t, newTestHandler(t, deps), http.MethodGet, path, "", owner)
	out = ProgressCapabilities{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || out.State != StateNotConfigured || out.Allowed == nil || *out.Allowed || out.ClearResumePosition {
		t.Fatalf("unconfigured: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGetWatchStatePreservesDevicePreferenceContext(t *testing.T) {
	watch := &fakeWatch{}
	h := newTestHandler(t, watchDeps(watch))
	owner := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	for _, tc := range []struct{ header, want string }{{"tv-1", "tv-1"}, {" tablet-2 ", "tablet-2"}, {"", ""}} {
		rec := do(t, h, http.MethodGet, "/api/v2/watch/movie:heat-1995", "", with(owner, deviceIDHeader, tc.header))
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		filter := watch.filters[len(watch.filters)-1]
		if filter.DeviceID != tc.want {
			t.Fatalf("device context = %q, want %q", filter.DeviceID, tc.want)
		}
	}
}
