package catalog

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFilterMediaFilesByAccess(t *testing.T) {
	allowed := &models.MediaFile{ID: 1, MediaFolderID: 1, Resolution: "1080p"}
	otherLibrary := &models.MediaFile{ID: 2, MediaFolderID: 2, Resolution: "2160p"}
	files := []*models.MediaFile{allowed, otherLibrary}

	t.Run("no restrictions returns all files", func(t *testing.T) {
		got := FilterMediaFilesByAccess(files, AccessFilter{})
		if len(got) != 2 {
			t.Fatalf("expected 2 files, got %d", len(got))
		}
	})

	t.Run("allowed library ids filter without quality ceiling", func(t *testing.T) {
		got := FilterMediaFilesByAccess(files, AccessFilter{AllowedLibraryIDs: []int{1}})
		if len(got) != 1 || got[0].ID != allowed.ID {
			t.Fatalf("expected only file %d, got %v", allowed.ID, got)
		}
	})

	t.Run("disabled library ids filter without quality ceiling", func(t *testing.T) {
		got := FilterMediaFilesByAccess(files, AccessFilter{DisabledLibraryIDs: []int{2}})
		if len(got) != 1 || got[0].ID != allowed.ID {
			t.Fatalf("expected only file %d, got %v", allowed.ID, got)
		}
	})

	t.Run("quality ceiling filters", func(t *testing.T) {
		got := FilterMediaFilesByAccess(files, AccessFilter{MaxPlaybackQuality: "1080p"})
		if len(got) != 1 || got[0].ID != allowed.ID {
			t.Fatalf("expected only file %d, got %v", allowed.ID, got)
		}
	})

	t.Run("matches FileAllowedByAccess predicate", func(t *testing.T) {
		filter := AccessFilter{AllowedLibraryIDs: []int{1}, MaxPlaybackQuality: "1080p"}
		got := FilterMediaFilesByAccess(files, filter)
		for _, f := range got {
			if !FileAllowedByAccess(f, filter) {
				t.Fatalf("file %d returned despite failing FileAllowedByAccess", f.ID)
			}
		}
	})
}
