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
	filters []catalogpkg.AccessFilter
	marks   []fakeMark
	err     error
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
			Versions: []catalogpkg.FileVersion{{
				FileID: 42, Resolution: "1080p", CodecVideo: "h264", CodecAudio: "eac3", Container: "mkv", FileSize: 1024, Duration: 10200, Bitrate: 8000000,
				AddedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
				VideoTracks: []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
				AudioTracks: []models.AudioTrack{{Language: "eng", Codec: "eac3", Channels: 6, Default: true}},
				Chapters:    []catalogpkg.VersionChapter{{Index: 1, Title: "Opening", StartSeconds: 0, EndSeconds: 300, Source: "embedded"}},
				Intro:       &catalogpkg.Marker{Start: 0, End: 90},
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

func watchDeps(watch *fakeWatch) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.Watch = watch
	return deps
}

func TestGetWatchState(t *testing.T) {
	watch := &fakeWatch{}
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
	versions := body["versions"].([]any)
	v := versions[0].(map[string]any)
	if v["file_id"] != "42" || v["duration_seconds"].(float64) != 10200 || v["added_at"] != "2026-01-02T03:04:05.000Z" || v["intro"].(map[string]any)["end_seconds"].(float64) != 90 {
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
