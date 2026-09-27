package metadata

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSkeletonIDsExcludeConfiguredLibraryRoot(t *testing.T) {
	const root = "/Library [tvdbid=123]"
	for _, tt := range []struct {
		name, filePath, observed, canonical, tmdb, tvdb string
		roots                                           []string
	}{
		{"configured root", root + "/Example.Show.S01E01.mkv", root, root, "", "", []string{root}},
		{"cleaned root", root + "/Example.Show.S01E01.mkv", root + "/.", root + "/", "", "", []string{root + "/"}},
		{"filename ID", root + "/Example.Show.S01E01 {tmdb-789}.mkv", root, root, "789", "", []string{root}},
		{"nested content ID", root + "/Example Show {tvdb-456}/S01E01.mkv", root + "/Example Show {tvdb-456}", root, "", "456", []string{root}},
		{"canonical content ID", root + "/Example Show {tvdb-456}/S01E01.mkv", root, root + "/Example Show {tvdb-456}", "", "456", []string{root}},
		{"legacy caller", root + "/Example.Show.S01E01.mkv", root, root, "", "123", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			file := &models.MediaFile{
				ID: 1, MediaFolderID: 10, FilePath: tt.filePath, BaseTitle: "Example Show", BaseType: "series",
				ObservedRootPath: tt.observed, CanonicalRootPath: tt.canonical,
			}
			skeleton, err := h.service.createOrFindSkeleton(t.Context(), file, 10, tt.roots...)
			if err != nil {
				t.Fatal(err)
			}
			if skeleton.TmdbID != tt.tmdb || skeleton.TvdbID != tt.tvdb {
				t.Fatalf("skeleton IDs tmdb=%q tvdb=%q, want %q/%q", skeleton.TmdbID, skeleton.TvdbID, tt.tmdb, tt.tvdb)
			}
		})
	}
}

func TestConfiguredLibraryRootIDCannotHideStaleScannedIdentity(t *testing.T) {
	const root = "/Library [tvdbid=123]"
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: root + "/Example.Show.S01E01.mkv", BaseTitle: "Example Show", BaseType: "series",
		ObservedRootPath: root, CanonicalRootPath: root, GroupKeyVersion: 1, ContentGroupKey: "old-library-root",
	}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: file.ContentGroupKey,
		BaseTitle: "Library", InferredType: "series", TvdbID: "123", OverrideSource: "none",
	})
	_, err := h.service.createOrFindSkeleton(t.Context(), file, 10, root)
	if err == nil || !strings.Contains(err.Error(), "rescan") {
		t.Fatalf("configured root's ID bypassed stale identity check: %v", err)
	}
	if len(h.itemRepo.items) != 0 || len(h.fileRepo.contentIDs) != 0 {
		t.Fatal("stale library-root identity created or linked a catalog item")
	}
}
