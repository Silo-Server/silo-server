package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// rotationTracksResolver resolves catalog rows by id and by exact path, so one
// resolver serves the candidate lookup and the rotation owner lookup.
type rotationTracksResolver struct {
	byID   map[int]*models.MediaFile
	byPath map[string]*models.MediaFile
}

func (r rotationTracksResolver) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return r.byID[id], nil
}

func (r rotationTracksResolver) GetByPath(_ context.Context, path string) (*models.MediaFile, error) {
	return r.byPath[path], nil
}

func rotationTrackFixtureTracks(audio, subtitles int) ([]models.AudioTrack, []models.SubtitleTrack) {
	audioTracks := make([]models.AudioTrack, audio)
	for i := range audioTracks {
		audioTracks[i] = models.AudioTrack{Codec: "eac3", Channels: 6, Language: "eng", Index: i}
	}
	subtitleTracks := make([]models.SubtitleTrack, subtitles)
	for i := range subtitleTracks {
		subtitleTracks[i] = models.SubtitleTrack{Codec: "hdmv_pgs_subtitle", Language: "eng", Index: i}
	}
	return audioTracks, subtitleTracks
}

func rotationTrackFixtureRow(id, size int, uri, release string, audio, subtitles int) *models.MediaFile {
	audioTracks, subtitleTracks := rotationTrackFixtureTracks(audio, subtitles)
	return &models.MediaFile{
		ID: id, ContentID: "movie-rotation-tracks", FilePath: uri,
		Container: "virtual", FileSize: int64(size), ProviderReleaseName: release,
		ProviderReleaseSize: int64(size), VirtualOwnerInstallationID: 5, MediaFolderID: 9,
		ProbeSource: "virtual",
		AudioTracks: audioTracks, SubtitleTracks: subtitleTracks,
	}
}

// rotationTrackFixture is one auto start whose listed candidates belong to
// different catalog rows. The prober is keyed by candidate URI so each
// candidate can serve its own bytes and inventory.
type rotationTrackFixture struct {
	handler *PlaybackHandler
	file    *models.MediaFile
	probes  *[]string
}

func newRotationTrackFixture(
	t *testing.T,
	file *models.MediaFile,
	rows map[string]*models.MediaFile,
	candidates []VirtualPlaybackStream,
	probe func(path string, f *models.MediaFile) error,
) *rotationTrackFixture {
	t.Helper()
	byID := make(map[int]*models.MediaFile, len(rows))
	for _, row := range rows {
		if row != nil && row.ID > 0 {
			byID[row.ID] = row
		}
	}
	resolver := rotationTracksResolver{byID: byID, byPath: rows}
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), resolver)
	h.BestResultCache = nil
	h.ItemLookup = staticRuntimeItemLookup{runtime: 90}
	h.VirtualPlaybackStreamLister = VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
		return candidates, nil
	})
	h.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
		return "https://93.184.216.34/stream/" + virtualResultCandidateID(path), nil
	})
	h.VirtualFileLookup = func(_ context.Context, path string) (*models.MediaFile, error) {
		return rows[path], nil
	}
	var mu sync.Mutex
	probes := make([]string, 0, len(candidates))
	h.VirtualPlaybackSourceProber = func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
		mu.Lock()
		probes = append(probes, f.FilePath)
		mu.Unlock()
		if err := probe(f.FilePath, f); err != nil {
			return nil, err
		}
		return f, nil
	}
	h.VirtualFileMetadataSaver = func(context.Context, models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
		return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
	}
	h.VirtualFileSaver = func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil }
	t.Cleanup(h.StopVirtualEvidence)
	return &rotationTrackFixture{handler: h, file: file, probes: &probes}
}

func (f *rotationTrackFixture) resolve(t *testing.T) resolvedVirtualPlaybackSource {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	resolved, err := f.handler.resolveVirtualPlaybackSource(req, f.file, "profile-1", false, nil, "", "", 0, true)
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	return resolved
}

// A requested row whose own bytes are short of the indexer's declaration still
// plays as itself with its own inventory. The listing also offers an unrelated
// release first; the old behavior rotated the plan onto that row and replaced
// the selected 2 audio + 4 PGS tracks with its single E-AC3 + SRT track. The
// track-safe rule rejects that candidate and lands on the requested row.
func TestResolveVirtualPlaybackSizeGappedRowStaysOnItsOwnInventory(t *testing.T) {
	const (
		neutral      = "virtual://movie/tt-rotation-tracks"
		foreignURI   = neutral + "?result=foreign"
		requestedURI = neutral + "?result=requested"
	)
	scannerResetVirtualProbeState(t, foreignURI, requestedURI)

	requested := rotationTrackFixtureRow(100, 34_000_000_000, neutral, "Selected.Release.2160p", 2, 4)
	requested.ProbeUpdatedAt = timePtr(time.Now().Add(-time.Hour))
	foreign := rotationTrackFixtureRow(200, 34_000_000_000, foreignURI, "Foreign.Release.2160p", 1, 1)
	rows := map[string]*models.MediaFile{foreignURI: foreign, requestedURI: requested}
	candidates := []VirtualPlaybackStream{
		{ID: "foreign", URI: foreignURI, FileSize: 34_000_000_000, OwnerInstallationID: 5},
		{ID: "requested", URI: requestedURI, FileSize: 34_000_000_000, OwnerInstallationID: 5},
	}
	fx := newRotationTrackFixture(t, requested, rows, candidates, func(path string, f *models.MediaFile) error {
		f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
		f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
		switch path {
		case foreignURI:
			// A different file: 6.28 GB for a 34 GB declaration, with the
			// foreign row's single audio and subtitle track.
			f.ProbedFormatSize = 6_287_332_084
			f.Duration = 90 * 60
			f.AudioTracks, f.SubtitleTracks = rotationTrackFixtureTracks(1, 1)
		default:
			// The selected release, posted short of its advertised size.
			f.ProbedFormatSize = 28_000_000_000
			f.Duration = 90 * 60
			f.AudioTracks, f.SubtitleTracks = rotationTrackFixtureTracks(2, 4)
		}
		return nil
	})

	resolved := fx.resolve(t)
	if resolved.File == nil {
		t.Fatal("resolve returned no file")
	}
	if resolved.File.ID != requested.ID {
		t.Fatalf("effective file id = %d, want the selected row %d", resolved.File.ID, requested.ID)
	}
	if got := len(resolved.File.AudioTracks); got != 2 {
		t.Fatalf("effective audio tracks = %d, want the selected row's 2", got)
	}
	if got := len(resolved.File.SubtitleTracks); got != 4 {
		t.Fatalf("effective subtitle tracks = %d, want the selected row's 4", got)
	}
	if resolved.URI != requestedURI {
		t.Fatalf("resolved URI = %q, want %q", resolved.URI, requestedURI)
	}
	if resolved.SubstitutedFromFileID != 0 {
		t.Fatalf("same-version serve reported a substitution from %d", resolved.SubstitutedFromFileID)
	}
}

// A rotation is a substitution, so it says so. When the selected release's own
// candidate cannot serve, the resolver may land on another concrete row, but
// the result must name the requested row as the substituted source instead of
// silently playing a different file.
func TestResolveVirtualPlaybackGenuineRotationIsReported(t *testing.T) {
	const (
		neutral    = "virtual://movie/tt-rotation-reported"
		brokenURI  = neutral + "?result=broken"
		substitute = neutral + "?result=substitute"
	)
	scannerResetVirtualProbeState(t, brokenURI, substitute)

	requested := rotationTrackFixtureRow(110, 34_000_000_000, neutral, "Selected.Release.2160p", 2, 4)
	requested.ProbeUpdatedAt = timePtr(time.Now().Add(-time.Hour))
	broken := rotationTrackFixtureRow(111, 34_000_000_000, brokenURI, "Selected.Release.2160p", 2, 4)
	alternate := rotationTrackFixtureRow(210, 34_000_000_000, substitute, "Alternate.Release.2160p", 2, 4)
	rows := map[string]*models.MediaFile{brokenURI: broken, substitute: alternate}
	candidates := []VirtualPlaybackStream{
		{ID: "broken", URI: brokenURI, FileSize: 34_000_000_000, OwnerInstallationID: 5},
		{ID: "substitute", URI: substitute, FileSize: 34_000_000_000, OwnerInstallationID: 5},
	}
	fx := newRotationTrackFixture(t, requested, rows, candidates, func(path string, f *models.MediaFile) error {
		if path == brokenURI {
			return context.DeadlineExceeded
		}
		f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
		f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
		f.ProbedFormatSize = 34_000_000_000
		f.Duration = 90 * 60
		f.AudioTracks, f.SubtitleTracks = rotationTrackFixtureTracks(2, 4)
		return nil
	})

	resolved := fx.resolve(t)
	if resolved.File == nil {
		t.Fatal("resolve returned no file")
	}
	if resolved.File.ID != alternate.ID {
		t.Fatalf("effective file id = %d, want the alternate row %d", resolved.File.ID, alternate.ID)
	}
	if resolved.SubstitutedFromFileID != requested.ID {
		t.Fatalf("substituted_from_file_id = %d, want the requested row %d", resolved.SubstitutedFromFileID, requested.ID)
	}
	if resolved.SubstitutionReason != substitutionReasonVersionNotPlayableV3 {
		t.Fatalf("substitution reason = %q, want %q", resolved.SubstitutionReason, substitutionReasonVersionNotPlayableV3)
	}
}

// A probe-evidence rotation must not land on an owner row whose measured
// inventory has fewer audio or subtitle tracks than the selected row. The live
// session keeps its own inventory; the catalog write is refused rather than
// repainting a poorer release as the selected one.
func TestPersistProbeEvidenceRefusesOwnerRotationThatLosesTracks(t *testing.T) {
	const (
		neutral   = "virtual://movie/tt-rotation-persist"
		pinnedURI = neutral + "?result=pinned"
		ownerURI  = neutral + "?result=owner"
	)
	selected := rotationTrackFixtureRow(601, 34_000_000_000, pinnedURI, "Selected.Release.2160p", 2, 4)
	owner := rotationTrackFixtureRow(602, 6_000_000_000, ownerURI, "Owner.Release.2160p", 1, 1)
	resolver := rotationTracksResolver{
		byID:   map[int]*models.MediaFile{selected.ID: selected, owner.ID: owner},
		byPath: map[string]*models.MediaFile{ownerURI: owner},
	}
	var writes []models.VirtualFilePersistArgs
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), resolver)
	h.VirtualFileMetadataSaver = func(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
		writes = append(writes, args)
		return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
	}
	h.VirtualFileSaver = func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil }
	t.Cleanup(h.StopVirtualEvidence)

	probed := rotationTrackFixtureRow(0, 6_000_000_000, ownerURI, "Owner.Release.2160p", 1, 1)
	logs := captureVirtualProbeLogs(t)
	h.persistVirtualProbeEvidence(context.Background(), selected, ownerURI, probed, noCandidateIdentity, true, true)
	h.StopVirtualEvidence()

	if len(writes) != 0 {
		t.Fatalf("persisted %d writes onto the poorer owner row: %+v", len(writes), writes)
	}
	logged := logs.String()
	if !strings.Contains(logged, "rotation refused: owner row would lose audio or subtitle tracks") {
		t.Fatalf("missing the track-loss refusal log:\n%s", logged)
	}
	if !strings.Contains(logged, `"requested_file_id":601`) || !strings.Contains(logged, `"owner_file_id":602`) {
		t.Fatalf("refusal log does not name both rows:\n%s", logged)
	}
}

func scannerResetVirtualProbeState(t *testing.T, uris ...string) {
	t.Helper()
	scanner.ResetVirtualServedSizeSnapshots()
	t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
	for _, uri := range uris {
		key := virtualProbeFailureKey(uri, 5)
		virtualProbeFailures.clear(key)
		virtualProbeFailures.clearSizeMismatch(key)
		t.Cleanup(func() {
			virtualProbeFailures.clear(key)
			virtualProbeFailures.clearSizeMismatch(key)
		})
	}
}
