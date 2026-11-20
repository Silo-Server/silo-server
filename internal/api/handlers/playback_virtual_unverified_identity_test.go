package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestUnverifiedForeignEvidenceDoesNotRotateOntoTheOwnerRow proves the persist
// seam refuses to move unverified bytes onto a foreign owner row. A served-size
// gap the probe could not resolve is not identity: writing its inventory to the
// row that happens to own the candidate path would present one release's tracks
// as another's, which is how a 720p row acquired 2160p Dolby Vision fields.
func TestUnverifiedForeignEvidenceDoesNotRotateOntoTheOwnerRow(t *testing.T) {
	const (
		neutral   = "virtual://movie/tt-unverified-owner"
		pinnedURI = neutral + "?result=pinned"
		ownerURI  = neutral + "?result=owner"
	)
	requested := rotationTrackFixtureRow(700, 2_360_000_000, pinnedURI, "Movie.2024.720p.H.264", 2, 1)
	requested.ProbeUpdatedAt = timePtr(time.Now().Add(-time.Hour))
	owner := rotationTrackFixtureRow(701, 19_270_000_000, ownerURI, "Movie.2024.2160p.DV.HEVC", 2, 1)
	rows := map[string]*models.MediaFile{ownerURI: owner}
	catalog := rotationTracksResolver{
		byID:   map[int]*models.MediaFile{requested.ID: requested, owner.ID: owner},
		byPath: rows,
	}
	var writes []models.VirtualFilePersistArgs
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), catalog)
	h.VirtualFileMetadataSaver = func(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
		writes = append(writes, args)
		return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
	}
	h.VirtualFileSaver = func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil }
	t.Cleanup(h.StopVirtualEvidence)

	probed := rotationTrackFixtureRow(0, 16_961_343_519, ownerURI, "Movie.2024.2160p.DV.HEVC", 2, 1)
	probed.ProbedFormatSize = 16_961_343_519
	probed.Duration = 90 * 60
	logs := captureVirtualProbeLogs(t)

	h.persistVirtualProbeEvidence(context.Background(), requested, ownerURI, probed, noCandidateIdentity, false, true)
	h.StopVirtualEvidence()

	// The only permitted write is the invalidation of the requested row's own
	// stale evidence. Nothing may write the foreign inventory, here or onto the
	// owner row.
	if len(writes) != 1 {
		t.Fatalf("persisted %d writes from unverified foreign bytes, want one invalidation: %+v", len(writes), writes)
	}
	invalidation := writes[0]
	if invalidation.FileID != requested.ID || !invalidation.ClearProbe {
		t.Fatalf("write = %+v, want a ClearProbe invalidation of the requested row %d", invalidation, requested.ID)
	}
	if string(invalidation.AudioTracks) != "[]" || string(invalidation.VideoTracks) != "[]" || string(invalidation.SubtitleTracks) != "[]" {
		t.Fatalf("invalidation = %+v, want empty track inventory", invalidation)
	}
	logged := logs.String()
	if !strings.Contains(logged, "unverified bytes belong to a different release") {
		t.Fatalf("missing the unverified-foreign refusal log:\n%s", logged)
	}
	if !strings.Contains(logged, `"file_id":700`) || !strings.Contains(logged, `"candidate_uri":"virtual://movie/tt-unverified-owner?result=owner"`) {
		t.Fatalf("refusal log does not name the row and candidate:\n%s", logged)
	}
}

// TestUnverifiedExplicitPickIsNotFulfilledByAnotherRelease proves an explicit
// version pick is never planned from a different release's bytes on unverified
// evidence. The request names the 720p row; the only offered candidate is owned
// by the 2160p Dolby Vision row and serves 16.96 GB for its 19.27 GB declaration.
// The size gap is unverified, so the resolve must fail naming the mismatch and
// must not write either row's inventory.
func TestUnverifiedExplicitPickIsNotFulfilledByAnotherRelease(t *testing.T) {
	const (
		neutral      = "virtual://movie/tt-unverified-explicit"
		requestedURI = neutral + "?result=requested720"
		foreignURI   = neutral + "?result=foreign2160"
	)
	scannerResetVirtualProbeState(t, requestedURI, foreignURI)

	requested := rotationTrackFixtureRow(710, 2_360_000_000, requestedURI, "Movie.2024.720p.H.264", 2, 1)
	requested.ProbeUpdatedAt = timePtr(time.Now().Add(-time.Hour))
	foreign := rotationTrackFixtureRow(711, 19_270_000_000, foreignURI, "Movie.2024.2160p.DV.HEVC", 2, 1)
	rows := map[string]*models.MediaFile{foreignURI: foreign}
	candidates := []VirtualPlaybackStream{
		{ID: "foreign2160", URI: foreignURI, FileSize: 19_270_000_000, OwnerInstallationID: 5},
	}
	fx := newRotationTrackFixture(t, requested, rows, candidates, func(_ string, f *models.MediaFile) error {
		f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
		f.AudioTracks = []models.AudioTrack{
			{Codec: "eac3", Channels: 6, Language: "eng", Index: 0},
			{Codec: "aac", Channels: 2, Language: "eng", Index: 1},
		}
		f.SubtitleTracks = []models.SubtitleTrack{{Codec: "hdmv_pgs_subtitle", Language: "eng", Index: 0}}
		f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
		f.ProbedFormatSize = 16_961_343_519
		f.Duration = 90 * 60
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	resolved, err := fx.handler.resolveVirtualPlaybackSource(req, requested, "profile-1", false, nil, "", "", 0, true)
	if err == nil {
		t.Fatalf("resolve succeeded with %+v, want an honest mismatch failure", resolved)
	}
	if !errors.Is(err, errVirtualSelectedReleaseUnverified) {
		t.Fatalf("resolve error = %v, want errVirtualSelectedReleaseUnverified", err)
	}
	terminal := virtualStartUnresolvedTerminalV3(err)
	if terminal.Terminal == nil || !terminal.Terminal.Retryable {
		t.Fatalf("terminal = %+v, want a retryable terminal", terminal.Terminal)
	}
	// The bytes contradicted the row, so the terminal names that verdict with
	// the version list's own vocabulary instead of a generic unavailable.
	if terminal.Terminal.Reason != sourceMismatchReasonV3 {
		t.Fatalf("terminal reason = %q, want %q", terminal.Terminal.Reason, sourceMismatchReasonV3)
	}
	// The release is alive: the refusal must not have armed the dead-pin
	// damper, which would stop the row from being retried while the provider
	// serves it correctly again.
	if virtualProbeFailures.recent(virtualProbeFailureKey(requestedURI, requested.VirtualOwnerInstallationID)) {
		t.Fatal("the source-mismatch refusal armed the dead-candidate damper")
	}
	// The refusal left the row's own wrong-source verdict behind, so the
	// version list can report why the pick failed.
	if _, _, ok := virtualProbeFailures.sizeMismatchVerdict(virtualProbeFailureKey(requestedURI, requested.VirtualOwnerInstallationID)); !ok {
		t.Fatal("the source-mismatch refusal left no wrong-source verdict for the row")
	}
	if resolved.File != nil {
		t.Fatalf("resolve returned an effective file %+v, want none", resolved.File)
	}
	// The versions check reports the same verdict, so the version list can say
	// "source mismatch" instead of a generic unavailable/failed row. It reads
	// the same process-local marker the refusal armed; the row stays available
	// because the release itself is alive.
	checks := &CatalogResourceHandler{
		FileResolver: versionsCheckTestResolver{file: requested},
		ItemAccess:   permissiveItemAccess{},
	}
	results := checks.CheckVersions(context.Background(), []int{requested.ID})
	if len(results) != 1 {
		t.Fatalf("version check results = %d, want 1", len(results))
	}
	if !results[0].Available || results[0].Failed {
		t.Fatalf("version check = %+v, want available and not failed", results[0])
	}
	if results[0].SourceMismatch == nil {
		t.Fatal("version check did not report the refused pick as a source mismatch")
	}
	// The pair is the row's own declaration against what the provider served for
	// its pick: 2.36 GB declared by the 720p row, 16.96 GB served.
	if results[0].SourceMismatch.ServedBytes != 16_961_343_519 || results[0].SourceMismatch.DeclaredBytes != 2_360_000_000 {
		t.Fatalf("source_mismatch = %+v, want the served/declared pair", *results[0].SourceMismatch)
	}
	fx.handler.StopVirtualEvidence()
}

// TestUnverifiedSizeGapOnOwnBytesStillInventoriesItsRow keeps #447's real
// intent: a row whose own candidate is posted short of the indexer's size still
// plays, and its measured inventory lands on that row metadata-only instead of
// being refused as foreign evidence.
func TestUnverifiedSizeGapOnOwnBytesStillInventoriesItsRow(t *testing.T) {
	const (
		release  = "Movie.2024.2160p.WEB-DL"
		declared = int64(6_330_000_000)
		served   = int64(5_570_000_000)
		runtime  = 90
	)
	uri := "virtual://movie/tt-own-short?result=pin-1"
	file := &models.MediaFile{
		ID: 720, ContentID: "movie-tt-own-short", FilePath: uri,
		Container: "virtual", FileSize: declared,
		ProviderReleaseName: release, ProviderReleaseSize: declared,
		VirtualOwnerInstallationID: 5, MediaFolderID: 9, ProbeSource: "virtual",
	}
	rows := map[string]*models.MediaFile{uri: sizeMismatchFallbackRow(721, uri, release, declared)}
	candidates := []VirtualPlaybackStream{sizeMismatchFallbackCandidate("pin-1", uri, release, declared)}
	scannerResetVirtualProbeState(t, uri)
	h, _, saved := sizeMismatchFallbackHandler(t, file, candidates, rows, func(*models.MediaFile) (int64, error) {
		return served, nil
	})
	h.ItemLookup = staticRuntimeItemLookup{runtime: runtime}

	resolved, err := h.resolveVirtualPlaybackSource(
		httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil), file, "profile-1", false, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if resolved.URI != uri {
		t.Fatalf("resolved URI = %q, want the row's own candidate %q", resolved.URI, uri)
	}
	h.StopVirtualEvidence()
	if len(*saved) != 1 {
		t.Fatalf("persisted %d writes, want one metadata-only inventory on the row's own bytes: %+v", len(*saved), *saved)
	}
	write := (*saved)[0]
	if write.FileID != file.ID || write.StampProbe {
		t.Fatalf("write = %+v, want a metadata-only write on the requested row %d", write, file.ID)
	}
	if write.Duration != runtime*60 {
		t.Fatalf("write duration = %d, want the measured %d", write.Duration, runtime*60)
	}
}
