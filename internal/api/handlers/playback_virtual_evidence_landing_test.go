package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

// Probe evidence must land on the row it was measured for. These tests pin the
// three dispositions that decide that: a row whose own candidate was probed
// writes its inventory, a row whose candidate path is already owned by a
// same-release duplicate writes it on its own pin (it can never adopt the
// duplicate's path), and a genuinely different release's bytes still never
// reach the requested row.

func evidenceLandingProbedFile(uri string) *models.MediaFile {
	return &models.MediaFile{
		FilePath: uri,
		Duration: 7368,
		AudioTracks: []models.AudioTrack{
			{Index: 0, Codec: "truehd", Channels: 8, Language: "ko"},
			{Index: 1, Codec: "ac3", Channels: 6, Language: "ko"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "eng"},
			{Index: 1, Codec: "subrip", Language: "fr"},
			{Index: 2, Codec: "subrip", Language: "ar"},
		},
	}
}

func decodeTrackCount[T any](t *testing.T, raw []byte) int {
	t.Helper()
	if len(raw) == 0 {
		return 0
	}
	var out []T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode tracks %q: %v", string(raw), err)
	}
	return len(out)
}

func TestProbeEvidenceArgsPersistTheProbedInventoryForAVirtualRow(t *testing.T) {
	const uri = "virtual://movie/tmdb/1375646?result=71134e86517fa2541fd32c66"
	stamped := time.Now().Add(-time.Minute)
	row := &models.MediaFile{
		ID: 41256, ContentID: "movie-tmdb-1375646", FilePath: uri,
		MediaFolderID: 3, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		UpdatedAt: time.Now(), ProbeUpdatedAt: &stamped,
	}
	probed := evidenceLandingProbedFile(uri)
	h := &PlaybackHandler{VirtualFileMetadataSaver: func(context.Context, models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
		return VirtualFileMetadataUpdateResult{RowsAffected: 1, MetadataUpdated: true}, nil
	}}

	args, origin, ok := h.virtualProbeEvidenceArgs(context.Background(), row, uri, probed, resolver.PersistedIdentityTiers{}, true, true)
	if !ok {
		t.Fatal("evidence for the row's own probed candidate was refused")
	}
	if origin != row.ID || args.FileID != row.ID {
		t.Fatalf("write target = %d (origin %d), want the requested row %d", args.FileID, origin, row.ID)
	}
	if args.ExpectedFilePath != row.FilePath {
		t.Fatalf("expected path = %q, want the row's own pin %q", args.ExpectedFilePath, row.FilePath)
	}
	if args.AdoptPath != "" || args.RequireAdopt {
		t.Fatalf("write adopted %q (require=%v); the row already owns its own candidate", args.AdoptPath, args.RequireAdopt)
	}
	if got := decodeTrackCount[models.AudioTrack](t, args.AudioTracks); got != 2 {
		t.Fatalf("audio tracks written = %d, want the 2 the probe measured", got)
	}
	if got := decodeTrackCount[models.SubtitleTrack](t, args.SubtitleTracks); got != 3 {
		t.Fatalf("subtitle tracks written = %d, want the 3 the probe measured", got)
	}
	if !args.StampProbe {
		t.Fatal("probe stamp not written; the row would re-probe on every start")
	}
}

// TestProbeEvidenceLandsOnTheRequestedRowWhenASameReleaseDuplicateOwnsTheCandidate
// reproduces the production shape: the provider renumbered the release, the new
// candidate id is already owned by a duplicate row of the same release (the
// version list groups the two as one), and the row therefore can never adopt
// that path. The measured inventory must still be stored on the row the viewer
// picked — refusing it is what left the played 4K row with empty track arrays
// while its duplicate owned the bytes.
func TestProbeEvidenceLandsOnTheRequestedRowWhenASameReleaseDuplicateOwnsTheCandidate(t *testing.T) {
	const (
		neutral = "virtual://movie/tmdb/1375646"
		oldURI  = neutral + "?result=71134e86517fa2541fd32c66"
		newURI  = neutral + "?result=700c4631c02f31e69a7eda57"
	)
	stamped := time.Now().Add(-time.Minute)
	requested := &models.MediaFile{
		ID: 41256, ContentID: "movie-tmdb-1375646", FilePath: oldURI,
		MediaFolderID: 3, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		FileSize: 75_000_000_000, UpdatedAt: time.Now(), ProbeUpdatedAt: &stamped,
		// The provider re-list churned the ?result= token. This row declares the
		// same size and no conflicting identity, so the version list groups it
		// with the duplicate below as one release.
	}
	duplicate := &models.MediaFile{
		ID: 41257, ContentID: "movie-tmdb-1375646", FilePath: newURI,
		MediaFolderID: 3, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		FileSize: 75_000_000_000,
		FailedAt: func() *time.Time { at := time.Now().Add(-2 * time.Hour); return &at }(),
	}
	probed := evidenceLandingProbedFile(newURI)
	h := &PlaybackHandler{
		fileResolver: byPathPlaybackFileResolver{byPath: map[string]*models.MediaFile{newURI: duplicate}},
		VirtualFileMetadataSaver: func(context.Context, models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			return VirtualFileMetadataUpdateResult{RowsAffected: 1, MetadataUpdated: true}, nil
		},
	}

	args, origin, ok := h.virtualProbeEvidenceArgs(context.Background(), requested, newURI, probed, resolver.PersistedIdentityTiers{}, true, true)
	if !ok {
		t.Fatal("evidence was dropped because a same-release duplicate owns the candidate path")
	}
	if origin != requested.ID || args.FileID != requested.ID {
		t.Fatalf("write target = %d (origin %d), want the requested row %d", args.FileID, origin, requested.ID)
	}
	if args.ExpectedFilePath != requested.FilePath {
		t.Fatalf("expected path = %q, want the requested row's pin %q", args.ExpectedFilePath, requested.FilePath)
	}
	if args.AdoptPath != "" || args.RequireAdopt {
		t.Fatalf("write adopted %q (require=%v); the duplicate owns that path and the adoption can never match", args.AdoptPath, args.RequireAdopt)
	}
	if got := decodeTrackCount[models.AudioTrack](t, args.AudioTracks); got != 2 {
		t.Fatalf("audio tracks written = %d, want 2", got)
	}
	if got := decodeTrackCount[models.SubtitleTrack](t, args.SubtitleTracks); got != 3 {
		t.Fatalf("subtitle tracks written = %d, want 3", got)
	}
}

// TestProbeEvidenceStillRotatesAGenuinelyDifferentRelease proves the landing
// rule is scoped to duplicates of one release: a candidate whose owning row is a
// different release still rotates to that owner (or is refused), never onto the
// requested row.
func TestProbeEvidenceStillRotatesAGenuinelyDifferentRelease(t *testing.T) {
	const (
		neutral   = "virtual://movie/tt-alt-evidence"
		oldURI    = neutral + "?result=old"
		otherURI  = neutral + "?result=other"
		contentID = "movie-alt-evidence"
	)
	requested := &models.MediaFile{
		ID: 501, ContentID: contentID, FilePath: oldURI,
		MediaFolderID: 3, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		FileSize: 40_000_000_000, ProviderVideoHash: "hash-a",
		UpdatedAt: time.Now(),
	}
	owner := &models.MediaFile{
		ID: 502, ContentID: contentID, FilePath: otherURI,
		MediaFolderID: 3, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		FileSize: 75_000_000_000, ProviderVideoHash: "hash-b",
	}
	probed := evidenceLandingProbedFile(otherURI)
	h := &PlaybackHandler{
		fileResolver: byPathPlaybackFileResolver{byPath: map[string]*models.MediaFile{otherURI: owner}},
		VirtualFileMetadataSaver: func(context.Context, models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			return VirtualFileMetadataUpdateResult{RowsAffected: 1, MetadataUpdated: true}, nil
		},
	}

	if ownerRow := h.virtualProbeEvidenceSiblingOwner(context.Background(), requested, otherURI); ownerRow != nil {
		t.Fatalf("a different release's row was treated as a same-release duplicate: %d", ownerRow.ID)
	}
	if args, _, ok := h.virtualProbeEvidenceArgs(context.Background(), requested, otherURI, probed, resolver.PersistedIdentityTiers{}, true, true); ok && args.FileID == requested.ID {
		t.Fatal("a different release's probed inventory was written onto the requested row")
	}
}
