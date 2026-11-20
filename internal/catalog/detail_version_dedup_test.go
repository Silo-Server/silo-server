package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// virtualDupRow builds a virtual catalog row for the duplicate-collapse tests.
// probed controls whether the row carries probe evidence and an audio track
// whose language identifies it in the projected version.
func virtualDupRow(id int, size int64, path, language string, probed bool) *models.MediaFile {
	file := &models.MediaFile{
		ID:             id,
		ContentID:      "movie-dup",
		FilePath:       path,
		Container:      "virtual",
		FileSize:       size,
		Duration:       3600,
		Resolution:     "1080p",
		CodecVideo:     "h264",
		CodecAudio:     "aac",
		AudioTracks:    []models.AudioTrack{{Codec: "aac", Language: language}},
		SubtitleTracks: []models.SubtitleTrack{},
	}
	if probed {
		probedAt := time.Now()
		file.ProbeUpdatedAt = &probedAt
	}
	return file
}

// buildDupVersions projects the supplied rows through the shared version builder
// with no repositories, which is sufficient for the pure collapse logic.
func buildDupVersions(t *testing.T, files []*models.MediaFile) []FileVersion {
	t.Helper()
	svc := &DetailService{}
	versions, _, _, _, _, _, _ := svc.buildPlaybackInfo(context.Background(), files, AccessFilter{}, "movie-dup")
	return versions
}

// TestBuildPlaybackInfoCollapsesByteIdenticalDuplicateRows pins the primary
// dedup: two catalog rows for one release that share content, size and concrete
// candidate URI must list once. Distinct rows of one release otherwise appear as
// duplicate versions and let a later serve pick the wrong sibling.
func TestBuildPlaybackInfoCollapsesByteIdenticalDuplicateRows(t *testing.T) {
	uri := "virtual://movie/dup?result=a"
	files := []*models.MediaFile{
		virtualDupRow(11, 1000, uri, "eng", true),
		virtualDupRow(12, 1000, uri, "eng", true),
	}
	versions := buildDupVersions(t, files)
	if len(versions) != 1 {
		t.Fatalf("byte-identical duplicates projected %d versions, want 1", len(versions))
	}
	if versions[0].FileID != 11 {
		t.Fatalf("collapsed to file %d, want the first occurrence 11", versions[0].FileID)
	}
}

// TestBuildPlaybackInfoCollapsesUnprobedPlaceholderAgainstProbedCopy pins the
// second rule: an unprobed virtual placeholder for a release whose probed copy
// is present must not be listed. The probed copy is authoritative.
func TestBuildPlaybackInfoCollapsesUnprobedPlaceholderAgainstProbedCopy(t *testing.T) {
	// Same release, different concrete result= ids: the placeholder row and the
	// probed row share the provider-neutral key.
	probed := virtualDupRow(21, 1000, "virtual://movie/dup?result=probed", "eng", true)
	placeholder := virtualDupRow(22, 1000, "virtual://movie/dup?result=placeholder", "", false)
	if versions := buildDupVersions(t, []*models.MediaFile{probed, placeholder}); len(versions) != 1 {
		t.Fatalf("unprobed placeholder beside a probed copy projected %d versions, want 1", len(versions))
	}
	if versions := buildDupVersions(t, []*models.MediaFile{placeholder, probed}); len(versions) != 1 {
		t.Fatalf("placeholder-first ordering projected %d versions, want 1", len(versions))
	}

	// A collection placeholder is stored with size 0 while the probed copy of
	// the same release carries the real size; the placeholder must still collapse.
	sizeZeroPlaceholder := virtualDupRow(23, 0, "virtual://movie/dup?result=placeholder", "", false)
	if versions := buildDupVersions(t, []*models.MediaFile{probed, sizeZeroPlaceholder}); len(versions) != 1 {
		t.Fatalf("size-0 placeholder beside a probed copy projected %d versions, want 1", len(versions))
	}
}

// TestBuildPlaybackInfoKeepsUnprobedPlaceholderWithoutProbedCopy proves the
// collapse does not hide a release that has not been probed yet: a lone
// placeholder (or several placeholders of distinct releases) stays listed.
func TestBuildPlaybackInfoKeepsUnprobedPlaceholderWithoutProbedCopy(t *testing.T) {
	only := virtualDupRow(31, 1000, "virtual://movie/dup?result=only", "", false)
	if versions := buildDupVersions(t, []*models.MediaFile{only}); len(versions) != 1 {
		t.Fatalf("lone unprobed placeholder projected %d versions, want 1", len(versions))
	}

	// Distinct releases (different size) are not duplicates and must both stay.
	small := virtualDupRow(41, 1000, "virtual://movie/dup?result=small", "eng", true)
	large := virtualDupRow(42, 2000, "virtual://movie/dup?result=large", "eng", true)
	if versions := buildDupVersions(t, []*models.MediaFile{small, large}); len(versions) != 2 {
		t.Fatalf("distinct-size releases projected %d versions, want 2", len(versions))
	}

	// Same size and content but different neutral paths are distinct releases.
	other := virtualDupRow(51, 1000, "virtual://movie/other?result=a", "eng", true)
	sameRelease := virtualDupRow(52, 1000, "virtual://movie/dup?result=a", "eng", true)
	if versions := buildDupVersions(t, []*models.MediaFile{other, sameRelease}); len(versions) != 2 {
		t.Fatalf("distinct releases projected %d versions, want 2", len(versions))
	}
}

// TestBuildPlaybackInfoCollapsesProbedResultChurn pins the probed-entry
// neutral-path grouping: two probed rows for one release whose concrete result=
// picks differ are the same release and must collapse to one version, while a
// genuinely distinct version (a different declared size or a conflicting durable
// identity) stays listed.
func TestBuildPlaybackInfoCollapsesProbedResultChurn(t *testing.T) {
	a := virtualDupRow(81, 1000, "virtual://movie/churn?result=aaa", "eng", true)
	b := virtualDupRow(82, 1000, "virtual://movie/churn?result=bbb", "eng", true)
	versions := buildDupVersions(t, []*models.MediaFile{a, b})
	if len(versions) != 1 {
		t.Fatalf("probed result-churn duplicates projected %d versions, want 1", len(versions))
	}
	if versions[0].FileID != 81 {
		t.Fatalf("collapsed to file %d, want the first occurrence 81", versions[0].FileID)
	}

	// A distinct declared size is a genuine version distinction: it must stay.
	small := virtualDupRow(83, 1000, "virtual://movie/churn?result=small", "eng", true)
	large := virtualDupRow(84, 2000, "virtual://movie/churn?result=large", "eng", true)
	if versions := buildDupVersions(t, []*models.MediaFile{small, large}); len(versions) != 2 {
		t.Fatalf("distinct-size probed versions projected %d versions, want 2", len(versions))
	}

	// A conflicting durable identity is a genuine version distinction too.
	sameSize := virtualDupRow(85, 1000, "virtual://movie/churn?result=hash-a", "eng", true)
	sameSize.ProviderVideoHash = "hash-a"
	otherHash := virtualDupRow(86, 1000, "virtual://movie/churn?result=hash-b", "eng", true)
	otherHash.ProviderVideoHash = "hash-b"
	if versions := buildDupVersions(t, []*models.MediaFile{sameSize, otherHash}); len(versions) != 2 {
		t.Fatalf("conflicting-identity probed versions projected %d versions, want 2", len(versions))
	}
}

// TestBuildPlaybackInfoCollapsesOneCandidateAcrossNeutralPaths pins the
// concrete-candidate rule: one provider candidate registered under two virtual
// paths (two of the item's provider title ids) is one file, and the compat
// PlaybackInfo must not offer it twice with different stream lists. The measured
// payload listed the same file three times, one copy probed with 14 streams and the
// others declared with 2, so a TV client could pick the copy with no subtitles.
func TestBuildPlaybackInfoCollapsesOneCandidateAcrossNeutralPaths(t *testing.T) {
	probed := virtualDupRow(91, 20_000_000_000, "virtual://movie/1375646?result=71134e86", "eng", true)
	probed.AudioTracks = rotationlessTracks(2)
	probed.SubtitleTracks = append(probed.SubtitleTracks, virtualDupSubtitle(3), virtualDupSubtitle(4))
	// Same provider candidate, a different virtual path prefix, and no probe.
	declared := virtualDupRow(92, 20_000_000_000, "virtual://movie/tt34385135?result=71134e86", "", false)

	for _, order := range [][]*models.MediaFile{{probed, declared}, {declared, probed}} {
		versions := buildDupVersions(t, order)
		if len(versions) != 1 {
			t.Fatalf("one candidate under two neutral paths projected %d versions, want 1", len(versions))
		}
		kept := versions[0]
		if kept.FileID != probed.ID {
			t.Fatalf("survivor = file %d, want the probed copy %d", kept.FileID, probed.ID)
		}
		if len(kept.AudioTracks) != 2 || len(kept.SubtitleTracks) != 2 {
			t.Fatalf("survivor inventory = %d audio / %d subtitles, want the probed 2 / 2",
				len(kept.AudioTracks), len(kept.SubtitleTracks))
		}
	}

	// Distinct candidates stay distinct, even under one neutral path.
	other := virtualDupRow(93, 20_000_000_000, "virtual://movie/tt34385135?result=another", "", false)
	if versions := buildDupVersions(t, []*models.MediaFile{declared, other}); len(versions) != 2 {
		t.Fatalf("distinct candidates projected %d versions, want 2", len(versions))
	}
}

// TestBuildPlaybackInfoUnionsDeclaredTracksForDuplicateRows pins the consistency
// half: when two unprobed rows name one candidate, the surviving version lists the
// union of the declarations rather than whichever row happened to be first. Neither
// row is evidence, so the union is the honest superset; a probed copy always wins
// instead (see the test above).
func TestBuildPlaybackInfoUnionsDeclaredTracksForDuplicateRows(t *testing.T) {
	first := virtualDupRow(94, 20_000_000_000, "virtual://movie/dup?result=same", "", false)
	first.AudioTracks = []models.AudioTrack{{Codec: "truehd", Channels: 8, Language: "eng"}}
	first.SubtitleTracks = nil
	second := virtualDupRow(95, 20_000_000_000, "virtual://movie/dup?result=same", "", false)
	second.AudioTracks = []models.AudioTrack{
		{Codec: "truehd", Channels: 8, Language: "eng"},
		{Codec: "ac3", Channels: 6, Language: "eng"},
	}
	second.SubtitleTracks = []models.SubtitleTrack{virtualDupSubtitle(3)}

	versions := buildDupVersions(t, []*models.MediaFile{first, second})
	if len(versions) != 1 {
		t.Fatalf("duplicate declarations projected %d versions, want 1", len(versions))
	}
	if got := len(versions[0].AudioTracks); got != 2 {
		t.Fatalf("union audio tracks = %d, want 2", got)
	}
	if got := len(versions[0].SubtitleTracks); got != 1 {
		t.Fatalf("union subtitle tracks = %d, want 1", got)
	}
	if versions[0].ProbedAt != nil {
		t.Fatal("a union of declarations was reported as probe evidence")
	}
}

func rotationlessTracks(count int) []models.AudioTrack {
	tracks := make([]models.AudioTrack, count)
	for i := range tracks {
		tracks[i] = models.AudioTrack{Codec: "truehd", Channels: 8, Language: "eng", Index: i}
	}
	return tracks
}

func virtualDupSubtitle(index int) models.SubtitleTrack {
	return models.SubtitleTrack{Codec: "subrip", Language: "eng", Index: index}
}

// TestBuildPlaybackInfoDoesNotCollapseLocalFiles proves the collapse is scoped
// to virtual rows: two local rows with the same content/size but distinct paths
// are still listed (their paths are their identity).
func TestBuildPlaybackInfoDoesNotCollapseLocalFiles(t *testing.T) {
	a := &models.MediaFile{ID: 61, ContentID: "movie-dup", FilePath: "/media/a.mkv", Container: "mkv", FileSize: 1000, Duration: 3600}
	b := &models.MediaFile{ID: 62, ContentID: "movie-dup", FilePath: "/media/b.mkv", Container: "mkv", FileSize: 1000, Duration: 3600}
	if versions := buildDupVersions(t, []*models.MediaFile{a, b}); len(versions) != 2 {
		t.Fatalf("local files projected %d versions, want 2", len(versions))
	}
}

// TestBuildPlaybackInfoKeepsDistinctUnprobedCandidateBeneathProbedCopy pins the
// placeholder-identification requirement: an unprobed row that is a concrete
// alternate release — a distinct size or a distinct durable provider identity —
// must stay selectable beside the probed copy even when it shares the probed
// row's provider-neutral URI. Only a genuine placeholder (unknown size, no
// conflicting identity) collapses.
func TestBuildPlaybackInfoKeepsDistinctUnprobedCandidateBeneathProbedCopy(t *testing.T) {
	probed := virtualDupRow(71, 1000, "virtual://movie/dup?result=probed", "eng", true)
	probed.ProviderReleaseName = "Movie.2024.1080p"
	probed.ProviderVideoHash = "hash-probed"

	// A concrete alternate with a different advertised size must not vanish,
	// even though no provider identity tier conflicts (the alternate declares
	// none).
	distinctSize := virtualDupRow(72, 2000, "virtual://movie/dup?result=alt-size", "", false)
	if versions := buildDupVersions(t, []*models.MediaFile{probed, distinctSize}); len(versions) != 2 {
		t.Fatalf("distinct-size unprobed alternate beside a probed copy projected %d versions, want 2", len(versions))
	}

	// Same unknown stored size, but a provider-declared size that disagrees:
	// the durable size tier is ProviderReleaseSize, so a row whose size lives
	// only there is not a size-unknown placeholder.
	sizedProbed := virtualDupRow(75, 0, "virtual://movie/dup?result=probed-size", "", true)
	sizedProbed.ProviderReleaseSize = 1000
	distinctProviderSize := virtualDupRow(76, 0, "virtual://movie/dup?result=alt-provider-size", "", false)
	distinctProviderSize.ProviderReleaseSize = 2000
	if versions := buildDupVersions(t, []*models.MediaFile{sizedProbed, distinctProviderSize}); len(versions) != 2 {
		t.Fatalf("distinct provider-size unprobed alternate beside a probed copy projected %d versions, want 2", len(versions))
	}

	// Same unknown size, but a durable provider identity that disagrees.
	distinctHash := virtualDupRow(73, 1000, "virtual://movie/dup?result=alt-hash", "", false)
	distinctHash.ProviderVideoHash = "hash-other"
	if versions := buildDupVersions(t, []*models.MediaFile{probed, distinctHash}); len(versions) != 2 {
		t.Fatalf("distinct-identity unprobed alternate beside a probed copy projected %d versions, want 2", len(versions))
	}

	// A genuine placeholder (size 0, no identity) still collapses.
	sizeZeroPlaceholder := virtualDupRow(74, 0, "virtual://movie/dup?result=placeholder", "", false)
	if versions := buildDupVersions(t, []*models.MediaFile{probed, sizeZeroPlaceholder}); len(versions) != 1 {
		t.Fatalf("size-0 placeholder beside a probed copy projected %d versions, want 1", len(versions))
	}
}

// TestBuildPlaybackInfoPrefersALiveDuplicateOverAFailedCopy pins the liveness
// rule: two rows for one candidate are the same bytes, and the one that can play
// must be the one listed. A failed row carrying probe evidence used to displace
// the live unprobed row (and the compat filter then dropped the candidate
// entirely because the surviving row was dead).
func TestBuildPlaybackInfoPrefersALiveDuplicateOverAFailedCopy(t *testing.T) {
	const uri = "virtual://movie/dup?result=live-vs-dead"
	failedProbed := virtualDupRow(81, 1000, uri, "eng", true)
	failedAt := time.Now()
	failedProbed.FailedAt = &failedAt
	liveUnprobed := virtualDupRow(82, 1000, uri, "eng", false)

	for _, tc := range []struct {
		name  string
		files []*models.MediaFile
		want  int
	}{
		{name: "failed row first", files: []*models.MediaFile{failedProbed, liveUnprobed}, want: 82},
		{name: "live row first", files: []*models.MediaFile{liveUnprobed, failedProbed}, want: 82},
	} {
		t.Run(tc.name, func(t *testing.T) {
			versions := buildDupVersions(t, tc.files)
			if len(versions) != 1 {
				t.Fatalf("one candidate projected %d versions, want 1", len(versions))
			}
			if versions[0].FileID != tc.want {
				t.Fatalf("collapsed to file %d, want the live row %d", versions[0].FileID, tc.want)
			}
			if versions[0].Failed {
				t.Fatal("the listed row is the failed copy")
			}
		})
	}

	// Between two live rows the existing probe-evidence preference stands.
	liveProbed := virtualDupRow(83, 1000, uri, "eng", true)
	versions := buildDupVersions(t, []*models.MediaFile{liveUnprobed, liveProbed})
	if len(versions) != 1 || versions[0].FileID != 83 {
		t.Fatalf("two live duplicates collapsed to %+v, want the probed row 83", versions)
	}
}
