package catalog

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAppendEpisodeParentLibraryAccessByEpisodeIDRejectsDisabledSeriesMembership(t *testing.T) {
	var conditions []string
	var args []any
	argIdx := 2
	appendEpisodeParentLibraryAccessByEpisodeID("e.content_id", AccessFilter{DisabledLibraryIDs: []int{9}}, &conditions, &args, &argIdx)

	sql := strings.Join(conditions, " AND ")
	seriesExpr := episodeParentSeriesIDExpr("e.content_id")
	assertEpisodeParentDisabledAccess(t, sql, seriesExpr)
	if !strings.Contains(sql, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = "+seriesExpr+")") {
		t.Fatalf("disabled-only episode listing must require positive series membership, got:\n%s", sql)
	}
	if len(args) != 1 || argIdx != 3 {
		t.Fatalf("args = %v, argIdx = %d; want one disabled-list arg and 3", args, argIdx)
	}
}

func TestAppendEpisodeParentLibraryAccessUsesProjectedSeriesID(t *testing.T) {
	var conditions []string
	var args []any
	argIdx := 1
	appendEpisodeParentLibraryAccess("ece.series_id", AccessFilter{DisabledLibraryIDs: []int{9}}, &conditions, &args, &argIdx)

	sql := strings.Join(conditions, " AND ")
	assertEpisodeParentDisabledAccess(t, sql, "ece.series_id")
	if strings.Contains(sql, "SELECT e_parent.series_id") {
		t.Fatalf("projected series ID must not trigger an episode lookup, got:\n%s", sql)
	}
}

func assertEpisodeParentDisabledAccess(t *testing.T, sql, seriesExpr string) {
	t.Helper()
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

func TestFilterMediaFilesByAccessPresentationLibrary(t *testing.T) {
	first := &models.MediaFile{ID: 1, ContentID: "shared-movie", MediaFolderID: 1, Resolution: "1080p"}
	second := &models.MediaFile{ID: 2, ContentID: "shared-movie", MediaFolderID: 2, Resolution: "2160p"}
	files := []*models.MediaFile{first, second}
	libraryID := 2

	for _, tt := range []struct {
		name   string
		filter AccessFilter
		want   []*models.MediaFile
	}{
		{
			name:   "unset returns both accessible libraries",
			filter: AccessFilter{AllowedLibraryIDs: []int{1, 2}},
			want:   files,
		},
		{
			name:   "selected library without the scope keeps every version",
			filter: AccessFilter{AllowedLibraryIDs: []int{1, 2}, PresentationLibraryID: &libraryID},
			want:   files,
		},
		{
			name:   "scope without a selected library keeps every version",
			filter: AccessFilter{AllowedLibraryIDs: []int{1, 2}, ScopeFilesToLibrary: true},
			want:   files,
		},
		{
			name:   "selected library narrows accessible files",
			filter: AccessFilter{AllowedLibraryIDs: []int{1, 2}, PresentationLibraryID: &libraryID, ScopeFilesToLibrary: true},
			want:   []*models.MediaFile{second},
		},
		{
			name:   "selected library without access restrictions",
			filter: AccessFilter{PresentationLibraryID: &libraryID, ScopeFilesToLibrary: true},
			want:   []*models.MediaFile{second},
		},
		{
			name:   "selected library cannot bypass allowlist",
			filter: AccessFilter{AllowedLibraryIDs: []int{1}, PresentationLibraryID: &libraryID, ScopeFilesToLibrary: true},
		},
		{
			name:   "selected library cannot bypass disabled libraries",
			filter: AccessFilter{DisabledLibraryIDs: []int{2}, PresentationLibraryID: &libraryID, ScopeFilesToLibrary: true},
		},
		{
			name:   "selected library cannot bypass quality ceiling",
			filter: AccessFilter{MaxPlaybackQuality: "1080p", PresentationLibraryID: &libraryID, ScopeFilesToLibrary: true},
		},
		{
			name:   "selected file cannot bypass library scope",
			filter: AccessFilter{AllowedLibraryIDs: []int{1, 2}, PresentationLibraryID: &libraryID, ScopeFilesToLibrary: true, SelectedFileID: first.ID},
			want:   []*models.MediaFile{second},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterMediaFilesByAccess(files, tt.filter)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("files = %v, want %v", got, tt.want)
			}
		})
	}
}
