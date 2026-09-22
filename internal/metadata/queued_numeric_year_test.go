package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestReusableQueuedIdentityClearsYearWhenRestoringNumericTitle(t *testing.T) {
	h := newTestHarness()
	const contentID = "numeric-title-skeleton"
	if err := h.itemRepo.Upsert(t.Context(), &models.MediaItem{ContentID: contentID, Status: "pending", Title: "Blade Runner", Year: 2049, Type: "movie"}); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{
		ContentID: contentID,
		FilePath:  "/movies/Blade Runner 2049/Blade Runner 2049.mkv",
		BaseTitle: "Blade Runner", BaseYear: 2049, BaseType: "movie",
	}
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	skeleton, ok := worker.reusableQueuedMovieSkeleton(t.Context(), file, false)
	if !ok || skeleton.Title != "Blade Runner 2049" || skeleton.Year != 0 {
		t.Fatalf("reused identity retained a guessed year: %+v", skeleton)
	}
}
