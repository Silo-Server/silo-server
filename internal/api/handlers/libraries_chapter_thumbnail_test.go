package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCreateLibrary_RejectsChapterThumbnailsWithoutArtworkStorage(t *testing.T) {
	handler := &LibraryHandler{}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/libraries",
		strings.NewReader(`{
			"name":"Movies",
			"type":"movies",
			"paths":["/mnt/media/movies"],
			"chapter_thumbnails_enabled":true
		}`),
	)
	rr := httptest.NewRecorder()

	handler.HandleCreateLibrary(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if body := rr.Body.String(); !strings.Contains(body, "Chapter thumbnails require configured artwork storage") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestHandleUpdateLibrary_RejectsChapterThumbnailsWithoutArtworkStorage(t *testing.T) {
	handler := &LibraryHandler{}
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/libraries/42",
		strings.NewReader(`{"chapter_thumbnails_enabled":true}`),
	)
	req = withPlaybackRouteParam(req, "id", "42")
	rr := httptest.NewRecorder()

	handler.HandleUpdateLibrary(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if body := rr.Body.String(); !strings.Contains(body, "Chapter thumbnails require configured artwork storage") {
		t.Fatalf("unexpected body: %s", body)
	}
}
