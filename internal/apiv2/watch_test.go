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
	userID        int
	profileID     string
	contentID     string
	played        bool
	recordRewatch bool
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

func (f *fakeWatch) SetWatchedState(_ context.Context, userID int, profileID, contentID string, played, recordRewatch bool, _ catalogpkg.AccessFilter) (handlers.WatchedStateView, error) {
	if f.err != nil {
		return handlers.WatchedStateView{}, f.err
	}
	if contentID != "movie:heat-1995" && contentID != "series:heat" {
		return handlers.WatchedStateView{}, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	f.marks = append(f.marks, fakeMark{userID: userID, profileID: profileID, contentID: contentID, played: played, recordRewatch: recordRewatch})
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

// The player labels a game of a date-labeled series by its air date; a
// numbered episode's watch detail carries neither member.
func TestWatchDetailEpisodeLabels(t *testing.T) {
	game := &catalogpkg.WatchDetail{ContentID: "ep-1", Type: "episode", Title: "Chicago Bulls vs Washington Wizards",
		SeriesTitle: "NBA", SeasonNumber: 2026, EpisodeNumber: 296, EpisodeLabels: "air_date", AirDate: "2026-11-20"}
	numbered := *game
	numbered.EpisodeLabels = ""

	for _, tt := range []struct {
		name   string
		detail *catalogpkg.WatchDetail
		want   map[string]any
	}{
		{"date-labeled", game, map[string]any{"episode_labels": "air_date", "air_date": "2026-11-20"}},
		{"numbered", &numbered, map[string]any{"episode_labels": nil, "air_date": nil}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(watchDetailOf(tt.detail))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Errorf("%s = %v, want %v", key, got[key], want)
				}
			}
		})
	}
}
