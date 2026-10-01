package abs

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPlayStart_ChaptersMatchItemDetail(t *testing.T) {
	cases := []struct {
		name  string
		files []*models.MediaFile
		want  []ChapterABS
	}{
		{name: "empty", want: []ChapterABS{}},
		{name: "single file", files: []*models.MediaFile{
			{FilePath: "book.m4b", Duration: 60, Chapters: []models.MediaChapter{{StartSeconds: 1.5, EndSeconds: 59.5, Title: "One"}}},
		}, want: []ChapterABS{{ID: 0, Start: 1.5, End: 59.5, Title: "One"}}},
		{name: "multiple files with chapterless gap", files: []*models.MediaFile{
			{FilePath: "01.mp3", Duration: 120, Chapters: []models.MediaChapter{
				{Index: 8, StartSeconds: 0, EndSeconds: 60, Title: "One"},
				{Index: 9, StartSeconds: 60, EndSeconds: 120, Title: "Two"},
			}},
			{FilePath: "02.mp3", Duration: 30},
			{FilePath: "03.mp3", Duration: 90, Chapters: []models.MediaChapter{{Index: 0, StartSeconds: 2.5, EndSeconds: 89.5, Title: "Three"}}},
		}, want: []ChapterABS{
			{ID: 0, Start: 0, End: 60, Title: "One"},
			{ID: 1, Start: 60, End: 120, Title: "Two"},
			{ID: 2, Start: 152.5, End: 239.5, Title: "Three"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := &models.MediaItem{ContentID: "book-1", Type: "audiobook"}
			h := New(Dependencies{MediaStore: &playStartMediaStore{item: item, files: tc.files}})
			detailRec := dispatchABSWithParams(http.MethodGet, "/api/items/book-1", map[string]string{"id": "book-1"}, nil, "1", "", h.handleItem)
			if detailRec.Code != http.StatusOK {
				t.Fatalf("detail status=%d: %s", detailRec.Code, detailRec.Body.String())
			}
			var detail LibraryItem
			if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			playRec := dispatchABSWithParams(http.MethodPost, "/api/items/book-1/play", map[string]string{"libraryItemId": "book-1"}, nil, "1", "", h.handlePlayStart)
			if playRec.Code != http.StatusOK {
				t.Fatalf("play status=%d: %s", playRec.Code, playRec.Body.String())
			}
			var play struct {
				Chapters    []ChapterABS `json:"chapters"`
				LibraryItem struct {
					Media struct {
						Chapters []ChapterABS `json:"chapters"`
					} `json:"media"`
				} `json:"libraryItem"`
			}
			if err := json.Unmarshal(playRec.Body.Bytes(), &play); err != nil {
				t.Fatal(err)
			}
			for name, got := range map[string][]ChapterABS{"detail": detail.Media.Chapters, "session": play.Chapters, "session item": play.LibraryItem.Media.Chapters} {
				if got == nil || !slices.Equal(got, tc.want) {
					t.Errorf("%s chapters=%+v, want %+v (non-null)", name, got, tc.want)
				}
			}
		})
	}
}
