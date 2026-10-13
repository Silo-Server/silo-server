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

func TestAccessFilterLibraryScope(t *testing.T) {
	cases := []struct {
		name      string
		requested []int
		allowed   []int
		disabled  []int
		wantIDs   []int
		wantNone  bool
	}{
		{name: "nil allowlist, no request is unrestricted"},
		{name: "nil allowlist, request kept", requested: []int{1, 2}, wantIDs: []int{1, 2}},
		{name: "nil allowlist, disabled only, no request leaves disabled to the caller", disabled: []int{2}},
		{name: "nil allowlist, request minus disabled", requested: []int{1, 2}, disabled: []int{2}, wantIDs: []int{1}},
		{name: "nil allowlist, only a disabled library requested", requested: []int{2}, disabled: []int{2}, wantNone: true},
		{name: "nil allowlist, nonexistent library requested", requested: []int{999}, wantIDs: []int{999}},
		{name: "empty allowlist, no request", allowed: []int{}, wantNone: true},
		{name: "empty allowlist, request", requested: []int{1}, allowed: []int{}, wantNone: true},
		{name: "allowlist, no request is the allowlist", allowed: []int{3, 4}, wantIDs: []int{3, 4}},
		{name: "allowlist, no request, minus disabled", allowed: []int{3, 4}, disabled: []int{4}, wantIDs: []int{3}},
		{name: "allowlist, no request, all disabled", allowed: []int{3}, disabled: []int{3}, wantNone: true},
		{name: "allowlist, overlapping request keeps request order", requested: []int{4, 1, 3}, allowed: []int{3, 4}, wantIDs: []int{4, 3}},
		{name: "allowlist, overlapping request deduplicates", requested: []int{3, 3}, allowed: []int{3}, wantIDs: []int{3}},
		{name: "allowlist, disjoint request", requested: []int{1}, allowed: []int{3}, wantNone: true},
		{name: "allowlist, nonexistent library requested", requested: []int{999}, allowed: []int{3}, wantNone: true},
		{name: "allowlist, overlap removed by disabled", requested: []int{3, 4}, allowed: []int{3, 4}, disabled: []int{3, 4}, wantNone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filter := AccessFilter{AllowedLibraryIDs: tc.allowed, DisabledLibraryIDs: tc.disabled}
			ids, none := filter.LibraryScope(tc.requested)
			if !slices.Equal(ids, tc.wantIDs) || (ids == nil) != (tc.wantIDs == nil) || none != tc.wantNone {
				t.Fatalf("LibraryScope(%v) = (%v, none=%v), want (%v, none=%v)", tc.requested, ids, none, tc.wantIDs, tc.wantNone)
			}
		})
	}
}

func TestAccessFilterLibraryScopeDoesNotAliasInputs(t *testing.T) {
	requested := []int{1, 2}
	allowed := []int{1, 2}
	filter := AccessFilter{AllowedLibraryIDs: allowed}

	fromRequest, _ := AccessFilter{}.LibraryScope(requested)
	fromRequest[0] = 99
	fromAllowlist, _ := filter.LibraryScope(nil)
	fromAllowlist[0] = 99

	if requested[0] != 1 || allowed[0] != 1 {
		t.Fatalf("LibraryScope aliased its inputs: requested=%v allowed=%v", requested, allowed)
	}
}
