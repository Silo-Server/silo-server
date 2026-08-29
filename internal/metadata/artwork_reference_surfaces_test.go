package metadata

import (
	"strings"
	"testing"
)

// TestArtworkReferenceSurfacesCoverEverySweepSurface keeps the collector's view
// of "still in use" at least as wide as the reconciler's. Losing a surface here
// means deleting artwork a row still points at.
func TestArtworkReferenceSurfacesCoverEverySweepSurface(t *testing.T) {
	references := artworkReferenceSurfaces()
	byName := make(map[string]artworkReferenceSurface, len(references))
	for _, surface := range references {
		byName[surface.name] = surface
	}

	for _, sweep := range artworkSweepSurfaces() {
		reference, ok := byName[sweep.name]
		if !ok {
			t.Fatalf("sweep surface %q has no reference surface", sweep.name)
		}
		if reference.table != sweep.table || reference.pathExpr != sweep.pathCol {
			t.Fatalf("reference surface %q reads %s.%s, sweep reads %s.%s",
				sweep.name, reference.table, reference.pathExpr, sweep.table, sweep.pathCol)
		}
		if reference.clearSet != sweep.clearSet {
			t.Fatalf("reference surface %q clears with %q, sweep clears with %q",
				sweep.name, reference.clearSet, sweep.clearSet)
		}
	}
}

func TestSeriesChildRepairTargetsUseNaturalCatalogKeys(t *testing.T) {
	wants := map[string]artworkRepairTargetSpec{
		artworkSurfaceSeasonPosters: {
			targetType: ImageCacheTargetSeason,
			targetExpr: "series_id",
			seasonExpr: "season_number",
		},
		artworkSurfaceLocalizedSeasonPosters: {
			targetType:   ImageCacheTargetSeasonLocalization,
			targetExpr:   "(SELECT series_id FROM seasons WHERE content_id = season_content_id)",
			languageExpr: "language",
			seasonExpr:   "(SELECT season_number FROM seasons WHERE content_id = season_content_id)",
		},
		artworkSurfaceEpisodeStills: {
			targetType:  ImageCacheTargetEpisode,
			targetExpr:  "series_id",
			seasonExpr:  "season_number",
			episodeExpr: "episode_number",
		},
	}

	for _, surface := range artworkSweepSurfaces() {
		want, ok := wants[surface.name]
		if !ok {
			continue
		}
		if surface.repairTarget != want {
			t.Fatalf("surface %q repair target = %#v, want %#v", surface.name, surface.repairTarget, want)
		}
		delete(wants, surface.name)
	}
	if len(wants) != 0 {
		t.Fatalf("repair target metadata missing surfaces: %#v", wants)
	}
}

// TestUploadOwningSurfacesAreReferenced pins the surfaces this branch started
// tracking upload revisions for. An upload has no re-downloadable source, so a
// revision collected while a row still points at it is unrecoverable.
func TestUploadOwningSurfacesAreReferenced(t *testing.T) {
	union := artworkReferenceUnionSQL()
	for _, want := range []string{
		"FROM media_folders",             // library posters
		"FROM library_collections",       // admin collection posters and backdrops
		"FROM user_personal_collections", // user collection posters
		"FROM user_profiles",             // uploaded profile avatars
	} {
		if !strings.Contains(union, want) {
			t.Fatalf("reference union does not read %s:\n%s", want, union)
		}
	}
}

// TestProfileAvatarReferenceSurfaceStripsTheUploadPrefix pins the one surface
// whose column does not hold a bare object key.
func TestProfileAvatarReferenceSurfaceStripsTheUploadPrefix(t *testing.T) {
	surface := profileAvatarReferenceSurface()

	// "upload:" is seven bytes, so the key starts at SQL offset 8.
	if surface.pathExpr != "substr(avatar, 8)" {
		t.Fatalf("path expression = %q", surface.pathExpr)
	}
	if surface.filter != "avatar LIKE 'upload:%'" {
		t.Fatalf("filter = %q", surface.filter)
	}
	if got := surface.matchPredicate(); got != "substr(avatar, 8) = $1 AND avatar LIKE 'upload:%'" {
		t.Fatalf("match predicate = %q", got)
	}
	// Presets and generated avatars must not be mistaken for object keys.
	if surface.remoteSource != "FALSE" {
		t.Fatalf("uploaded avatars have no re-downloadable source, got %q", surface.remoteSource)
	}
}

// TestArtworkReferenceUnionIsSingleParameter guards the query shape both
// callers depend on: one text[] parameter, one "path" column per branch.
func TestArtworkReferenceUnionIsSingleParameter(t *testing.T) {
	union := artworkReferenceUnionSQL()
	branches := strings.Split(union, " UNION ALL ")
	if len(branches) != len(artworkReferenceSurfaces()) {
		t.Fatalf("union has %d branches for %d surfaces", len(branches), len(artworkReferenceSurfaces()))
	}
	for _, branch := range branches {
		if !strings.HasPrefix(branch, "SELECT ") || !strings.Contains(branch, " AS path FROM ") {
			t.Fatalf("branch is not a path projection: %q", branch)
		}
		if !strings.Contains(branch, "= ANY($1)") {
			t.Fatalf("branch does not filter on $1: %q", branch)
		}
		if strings.Contains(branch, "$2") {
			t.Fatalf("branch uses a second parameter: %q", branch)
		}
	}
}

func TestArtworkRebuildStatusCountsOnlyCurrentReferences(t *testing.T) {
	lossReferences := artworkLossReferenceUnionSQL()
	if branches := strings.Split(lossReferences, " UNION ALL "); len(branches) != len(artworkReferenceSurfaces()) {
		t.Fatalf("loss-reference union has %d branches for %d surfaces", len(branches), len(artworkReferenceSurfaces()))
	}
	if got := strings.Count(lossReferences, "IN (SELECT original_path FROM loss_paths)"); got != len(artworkReferenceSurfaces()) {
		t.Fatalf("loss-reference union scopes %d branches to candidates, want %d", got, len(artworkReferenceSurfaces()))
	}
	query := artworkRebuildStatusSQL()
	if !strings.Contains(query, "referenced_paths AS") {
		t.Fatalf("rebuild status has no reference snapshot:\n%s", query)
	}
	if got := strings.Count(query, "i.original_path IN (SELECT path FROM referenced_paths)"); got != 2 {
		t.Fatalf("rebuild status scopes %d loss counters to live references, want 2:\n%s", got, query)
	}
}
