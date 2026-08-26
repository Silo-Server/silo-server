package catalog

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAppendEpisodeParentLibraryAccessRejectsDisabledSeriesMembership(t *testing.T) {
	var conditions []string
	var args []any
	argIdx := 2
	appendEpisodeParentLibraryAccess("e.content_id", AccessFilter{DisabledLibraryIDs: []int{9}}, &conditions, &args, &argIdx)

	sql := strings.Join(conditions, " AND ")
	assertEpisodeParentDisabledAccess(t, sql, "e.content_id")
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = "+episodeParentSeriesIDExpr("e.content_id")+")") {
		t.Fatalf("disabled-only episode listing must require positive series membership, got:\n%s", sql)
	}
	if len(args) != 1 || argIdx != 3 {
		t.Fatalf("args = %v, argIdx = %d; want one disabled-list arg and 3", args, argIdx)
	}
}

func assertEpisodeParentDisabledAccess(t *testing.T, sql, episodeIDExpr string) {
	t.Helper()
	seriesExpr := episodeParentSeriesIDExpr(episodeIDExpr)
	if !strings.Contains(sql, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = "+seriesExpr+" AND mil.media_folder_id = ANY(") {
		t.Fatalf("episode listing must reject disabled parent-series membership, got:\n%s", sql)
	}
}

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
