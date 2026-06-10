package handlers

import (
	"net/http/httptest"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestGetLeafUserDataUsesEbookReaderProgress(t *testing.T) {
	handler := &ItemsHandler{
		ebookProgressStore: &fakeEbookReaderProgressLister{
			progress: map[string]EbookReaderProgress{
				"ebook-progress": {
					UserID:    7,
					ProfileID: "profile-1",
					ContentID: "ebook-progress",
					Progress:  0.42,
					UpdatedAt: time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC),
				},
				"ebook-complete": {
					UserID:    7,
					ProfileID: "profile-1",
					ContentID: "ebook-complete",
					Progress:  0.93,
					UpdatedAt: time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC),
				},
			},
		},
	}
	req := httptest.NewRequest("GET", "/items/ebook-progress", nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)

	progress := handler.getLeafUserData(req, "ebook-progress", "ebook")
	if progress == nil || progress.Played || !progress.IsInProgress || progress.PositionSeconds != 0.42 || progress.DurationSeconds != 1 {
		t.Fatalf("partial ebook user data = %#v", progress)
	}

	complete := handler.getLeafUserData(req, "ebook-complete", "ebook")
	if complete == nil || !complete.Played || complete.IsInProgress {
		t.Fatalf("completed ebook user data = %#v", complete)
	}
}
