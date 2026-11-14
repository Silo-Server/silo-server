package handlers

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// captureVirtualProbeLogs installs a JSON slog handler for the duration of the
// test and returns the buffer it writes to.
func captureVirtualProbeLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// A release can be posted short of the size the indexer advertised while still
// being the right content: production served 5.57 GB for a release declared at
// 6.33 GB, and the provider's own completed download was short too. One serve
// cannot prove which release it was — a wrong file that carries the item's
// runtime looks the same — so the first duration-proven serve records the pin's
// observation and writes nothing, and the second serve that lands on those same
// bytes is admitted, written, and clears the wrong-source damper. A warning
// keeps the posting discrepancy visible on both.
func TestBackgroundProbeConfirmsServedSizeMismatchOnSecondServe(t *testing.T) {
	const (
		declaredBytes = int64(6_330_000_000)
		servedBytes   = int64(5_570_000_000) // 12% short: the posting itself is short
		expectedMins  = 51
		probedSeconds = 3072 // 51.2 minutes
	)
	fx := newServedSizeMismatchFixture(t, declaredBytes, servedBytes, probedSeconds)
	logs := captureVirtualProbeLogs(t)

	// First serve: the observation. The bytes are usable, so their measured
	// inventory is written metadata-only; the release identity stays unproven.
	virtualProbeFailures.markSizeMismatch(fx.key, 1, 1) // a previous stale fill armed the wrong-source damper
	assertServedSizeMismatchObservation(t, fx, expectedMins, logs)
	if saved := *fx.saved; len(saved) != 1 {
		t.Fatalf("persisted %d inventories on the recording serve, want one metadata-only write: %+v", len(saved), saved)
	}

	// Second serve on the same bytes: the confirmation. Only now is the pin's
	// identity established, so the inventory is stamped and the pin kept.
	logs.Reset()
	outcome, carried := fx.probe(t, expectedMins)
	if outcome != probeOutcomeVerified {
		t.Fatalf("outcome = %q, want verified for the confirming serve", outcome)
	}
	if !carried.committed() || carried.fileID != fx.file.ID || !carried.written {
		t.Fatalf("carried inventory = %+v, want the committed row %d", carried, fx.file.ID)
	}
	saved := *fx.saved
	if len(saved) != 2 {
		t.Fatalf("persisted %d inventories, want the observation then the confirming write: %+v", len(saved), saved)
	}
	if saved[1].FileID != fx.file.ID || !saved[1].StampProbe {
		t.Fatalf("confirming write = %+v, want a stamped write on file %d", saved[1], fx.file.ID)
	}
	if saved[1].Duration != probedSeconds {
		t.Fatalf("persisted duration = %d, want the measured %d", saved[1].Duration, probedSeconds)
	}
	if *fx.failStamped || fx.file.FailedAt != nil {
		t.Fatal("the confirming serve stamped a failed_at verdict")
	}

	logged := logs.String()
	if !strings.Contains(logged, "accepted despite size mismatch") {
		t.Fatalf("missing the short-posting acceptance warning:\n%s", logged)
	}
	if strings.Contains(logged, "rejected: served size mismatch") {
		t.Fatalf("confirmed short posting was logged as a rejection:\n%s", logged)
	}
	for _, attr := range []string{`"file_id":601`, `"served_bytes":5570000000`, `"declared_bytes":6330000000`, `"probed_duration_seconds":3072`, `"expected_runtime_minutes":51`} {
		if !strings.Contains(logged, attr) {
			t.Fatalf("acceptance warning is missing %s:\n%s", attr, logged)
		}
	}
}

// servedSizeMismatchFixture is the pinned short-posting case: one candidate
// whose served bytes are short of the indexer's declaration while the measured
// duration is plausible for the item's runtime.
type servedSizeMismatchFixture struct {
	handler     *PlaybackHandler
	file        *models.MediaFile
	cand        VirtualPlaybackStream
	key         string
	saved       *[]models.VirtualFilePersistArgs
	failStamped *bool
}

func newServedSizeMismatchFixture(t *testing.T, declaredBytes, servedBytes int64, probedSeconds int) *servedSizeMismatchFixture {
	t.Helper()
	uri := "virtual://movie/tt-size-mismatch?result=cand-1"
	key := virtualProbeFailureKey(uri, 5)
	virtualProbeFailures.clear(key)
	virtualProbeFailures.clearSizeMismatch(key)
	scanner.ResetVirtualServedSizeSnapshots()
	t.Cleanup(func() {
		virtualProbeFailures.clear(key)
		virtualProbeFailures.clearSizeMismatch(key)
		scanner.ResetVirtualServedSizeSnapshots()
	})
	var saved []models.VirtualFilePersistArgs
	failStamped := false
	h := &PlaybackHandler{
		VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
			f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
			f.ProbedFormatSize = servedBytes
			f.Duration = probedSeconds
			return f, nil
		},
		VirtualFileMetadataSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			saved = append(saved, args)
			return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
		},
		VirtualCandidateFailMarker: func(context.Context, int, string, *time.Time) error {
			failStamped = true
			return nil
		},
	}
	h.pinVirtualSticky("sticky-size-mismatch", uri)
	return &servedSizeMismatchFixture{
		handler: h,
		file: &models.MediaFile{
			ID:                         601,
			ContentID:                  "movie-tt",
			FilePath:                   uri,
			FileSize:                   declaredBytes,
			VirtualOwnerInstallationID: 5,
		},
		cand:        VirtualPlaybackStream{ID: "cand-1", URI: uri},
		key:         key,
		saved:       &saved,
		failStamped: &failStamped,
	}
}

// probe runs one durable background serve of the fixture's candidate.
func (f *servedSizeMismatchFixture) probe(t *testing.T, expectedMins int) (string, virtualCommittedProbeInventory) {
	t.Helper()
	return f.handler.probeVirtualSourceAndPersistWith(
		context.Background(), "sticky-size-mismatch", f.file, "http://provider.example/stream", *f.file, f.cand,
		noCandidateIdentity, expectedMins, 5, true, nil,
	)
}

// assertServedSizeMismatchObservation proves the recording serve wrote the
// measured inventory metadata-only: the session gets the probed tracks and
// duration, the release identity is not stamped or pinned, no failed stamp is
// recorded, the dampers are clear, and the log says the inventory was kept.
func assertServedSizeMismatchObservation(t *testing.T, fx *servedSizeMismatchFixture, expectedMins int, logs *bytes.Buffer) {
	t.Helper()
	outcome, carried := fx.probe(t, expectedMins)
	if outcome != probeOutcomeVerified {
		t.Fatalf("outcome = %q, want verified so the session leaves loading for a playable size gap", outcome)
	}
	if !carried.committed() || carried.fileID != fx.file.ID {
		t.Fatalf("carried inventory = %+v, want the served row %d", carried, fx.file.ID)
	}
	if !carried.written {
		t.Fatal("the recording serve did not report its metadata-only catalog write")
	}
	saved := *fx.saved
	if len(saved) != 1 {
		t.Fatalf("the recording serve persisted %d inventories, want one metadata-only write: %+v", len(saved), saved)
	}
	if saved[0].StampProbe {
		t.Fatal("the recording serve stamped unconfirmed bytes as the row's identity")
	}
	if saved[0].FileID != fx.file.ID {
		t.Fatalf("inventory persisted on file %d, want %d", saved[0].FileID, fx.file.ID)
	}
	if saved[0].Duration <= 0 {
		t.Fatal("the metadata-only write carried no measured duration")
	}
	if *fx.failStamped || fx.file.FailedAt != nil {
		t.Fatal("the recording serve stamped a failed_at verdict")
	}
	if virtualProbeFailures.recent(fx.key) {
		t.Fatal("the recording serve fed the dead-candidate failure damper")
	}
	if virtualProbeFailures.recent(virtualProbeSizeMismatchKey(fx.key)) {
		t.Fatal("the recording serve kept the wrong-source damper armed")
	}
	logged := logs.String()
	if !strings.Contains(logged, "probe inventory written, release identity unverified") {
		t.Fatalf("missing the inventory-written warning:\n%s", logged)
	}
	if strings.Contains(logged, "accepted despite size mismatch") {
		t.Fatalf("the recording serve was logged as an acceptance:\n%s", logged)
	}
}

// The wrong file must still be rejected: the production remux of another title
// served 7.43 GB for a row declaring 8.45 GB AND ran 3072s against a 2340s
// expectation (+31%). Its duration fails plausibility independently of size, so
// the historical rejection keeps its exact behavior: no persist, no failed_at,
// short wrong-source damper, pin released.
func TestBackgroundProbeRejectsServedSizeMismatchWithImplausibleDuration(t *testing.T) {
	const (
		declaredBytes = int64(8_449_999_999)
		servedBytes   = int64(7_427_636_945) // 12% short
		expectedMins  = 39                   // 2340s expected
		probedSeconds = 3072                 // +31%: a different title's runtime
	)
	uri := "virtual://movie/tt-wrong-file?result=cand-1"
	key := virtualProbeFailureKey(uri, 5)
	virtualProbeFailures.clear(key)
	virtualProbeFailures.clearSizeMismatch(key)
	t.Cleanup(func() {
		virtualProbeFailures.clear(key)
		virtualProbeFailures.clearSizeMismatch(key)
	})
	logs := captureVirtualProbeLogs(t)

	persisted := false
	failStamped := false
	h := &PlaybackHandler{
		VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
			f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
			f.ProbedFormatSize = servedBytes
			f.Duration = probedSeconds
			return f, nil
		},
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			persisted = true
			return 1, nil
		},
		VirtualCandidateFailMarker: func(context.Context, int, string, *time.Time) error {
			failStamped = true
			return nil
		},
	}
	stickyKey := "sticky-wrong-file"
	h.pinVirtualSticky(stickyKey, uri)
	file := &models.MediaFile{
		ID:                         603,
		ContentID:                  "movie-tt",
		FilePath:                   uri,
		FileSize:                   declaredBytes,
		VirtualOwnerInstallationID: 5,
	}
	cand := VirtualPlaybackStream{ID: "cand-1", URI: uri}

	outcome, committed := h.probeVirtualSourceAndPersistWith(
		context.Background(), stickyKey, file, "http://provider.example/stream", *file, cand,
		noCandidateIdentity, expectedMins, 5, true, nil,
	)
	if outcome != probeOutcomeFailed {
		t.Fatalf("outcome = %q, want failed for a wrong file", outcome)
	}
	if len(committed.audio) != 0 || len(committed.subtitles) != 0 {
		t.Fatal("a wrong file must not carry a committed inventory")
	}
	if persisted {
		t.Fatal("a wrong file must not persist probe evidence")
	}
	if failStamped || file.FailedAt != nil {
		t.Fatal("a wrong file must not stamp failed_at: the release is not dead")
	}
	if got := h.peekVirtualSticky(stickyKey); got != "" {
		t.Fatalf("wrong file kept the sticky pin: %q", got)
	}
	if virtualProbeFailures.recent(key) {
		t.Fatal("wrong file fed the dead-candidate failure damper")
	}
	if !virtualProbeFailures.recent(virtualProbeSizeMismatchKey(key)) {
		t.Fatal("wrong file did not arm the short wrong-source damper")
	}

	logged := logs.String()
	if !strings.Contains(logged, "background virtual probe rejected: measured identity contradicts the declared release") {
		t.Fatalf("missing distinct identity-contradiction warning:\n%s", logged)
	}
	if strings.Contains(logged, "accepted despite size mismatch") {
		t.Fatalf("a wrong file was accepted:\n%s", logged)
	}
}

// A missing duration on either side is no identity proof, but it is also no
// contradiction. The bytes stay usable: the probe inventory is written
// metadata-only and played, while the release identity stays unverified.
func TestBackgroundProbeKeepsServedSizeMismatchUsableWithoutDurationEvidence(t *testing.T) {
	const (
		declaredBytes = int64(6_330_000_000)
		servedBytes   = int64(5_570_000_000)
	)
	tests := []struct {
		name          string
		expectedMins  int
		probedSeconds int
	}{
		{name: "expected runtime unknown", expectedMins: 0, probedSeconds: 3072},
		{name: "probed duration unknown", expectedMins: 51, probedSeconds: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri := "virtual://movie/tt-no-duration?result=cand-1"
			key := virtualProbeFailureKey(uri, 5)
			virtualProbeFailures.clear(key)
			virtualProbeFailures.clearSizeMismatch(key)
			t.Cleanup(func() {
				virtualProbeFailures.clear(key)
				virtualProbeFailures.clearSizeMismatch(key)
			})
			logs := captureVirtualProbeLogs(t)

			var saved []models.VirtualFilePersistArgs
			h := &PlaybackHandler{
				VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
					f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
					f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
					f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
					f.ProbedFormatSize = servedBytes
					f.Duration = tt.probedSeconds
					return f, nil
				},
				VirtualFileMetadataSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
					saved = append(saved, args)
					return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
				},
			}
			stickyKey := "sticky-no-duration"
			file := &models.MediaFile{
				ID:                         604,
				ContentID:                  "movie-tt",
				FilePath:                   uri,
				FileSize:                   declaredBytes,
				VirtualOwnerInstallationID: 5,
			}
			cand := VirtualPlaybackStream{ID: "cand-1", URI: uri}

			outcome, carried := h.probeVirtualSourceAndPersistWith(
				context.Background(), stickyKey, file, "http://provider.example/stream", *file, cand,
				noCandidateIdentity, tt.expectedMins, 5, true, nil,
			)
			if outcome != probeOutcomeVerified {
				t.Fatalf("outcome = %q, want verified so the playable bytes are served", outcome)
			}
			if len(saved) != 1 {
				t.Fatalf("persisted %d inventories, want one metadata-only write: %+v", len(saved), saved)
			}
			if saved[0].StampProbe {
				t.Fatal("a size gap without a duration proof stamped the row's identity")
			}
			if len(carried.audio) == 0 || carried.written == false {
				t.Fatalf("carried inventory = %+v, want the measured audio tracks", carried)
			}
			if got := h.peekVirtualSticky(stickyKey); got != "" {
				t.Fatalf("unverified bytes were pinned: %q", got)
			}
			if virtualProbeFailures.recent(key) {
				t.Fatal("a size gap fed the dead-candidate failure damper")
			}
			if virtualProbeFailures.recent(virtualProbeSizeMismatchKey(key)) {
				t.Fatal("a size gap armed the wrong-source damper")
			}
			if logged := logs.String(); !strings.Contains(logged, "probe inventory written, release identity unverified") {
				t.Fatalf("missing the inventory-written warning:\n%s", logged)
			}
		})
	}
}

// The control: when the served size matches the declared release, the probe
// proceeds to the existing duration guard. The duration rejection must keep its
// own reason and its own dead-candidate damper, proving the size gate did not
// absorb it.
func TestBackgroundProbeMatchingServedSizeStillChecksDuration(t *testing.T) {
	const declaredBytes = int64(8_449_999_999)
	uri := "virtual://movie/tt-size-match?result=cand-1"
	key := virtualProbeFailureKey(uri, 5)
	virtualProbeFailures.clear(key)
	virtualProbeFailures.clearSizeMismatch(key)
	t.Cleanup(func() {
		virtualProbeFailures.clear(key)
		virtualProbeFailures.clearSizeMismatch(key)
	})
	logs := captureVirtualProbeLogs(t)

	persisted := false
	h := &PlaybackHandler{
		VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
			f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
			f.ProbedFormatSize = declaredBytes
			f.Duration = 300 // 5 minutes against a 51-minute expectation
			return f, nil
		},
		VirtualFileSaver: func(context.Context, models.VirtualFilePersistArgs) (int64, error) {
			persisted = true
			return 1, nil
		},
	}
	stickyKey := "sticky-size-match"
	h.pinVirtualSticky(stickyKey, uri)
	file := &models.MediaFile{
		ID:                         602,
		ContentID:                  "movie-tt",
		FilePath:                   uri,
		FileSize:                   declaredBytes,
		VirtualOwnerInstallationID: 5,
	}
	cand := VirtualPlaybackStream{ID: "cand-1", URI: uri}

	outcome, _ := h.probeVirtualSourceAndPersistWith(
		context.Background(), stickyKey, file, "http://provider.example/stream", *file, cand,
		noCandidateIdentity, 51, 5, true, nil,
	)
	if outcome != probeOutcomeFailed {
		t.Fatalf("outcome = %q, want failed from the duration guard", outcome)
	}
	if persisted {
		t.Fatal("an implausible duration must not persist probe evidence")
	}
	if got := h.peekVirtualSticky(stickyKey); got != "" {
		t.Fatalf("duration rejection kept the sticky pin: %q", got)
	}
	if !virtualProbeFailures.recent(key) {
		t.Fatal("duration rejection did not arm the dead-candidate damper")
	}
	if virtualProbeFailures.recent(virtualProbeSizeMismatchKey(key)) {
		t.Fatal("a matching served size armed the wrong-source damper")
	}

	logged := logs.String()
	if !strings.Contains(logged, "probed duration implausible") {
		t.Fatalf("matching size did not reach the duration guard:\n%s", logged)
	}
	if strings.Contains(logged, "served size mismatch") {
		t.Fatalf("matching size was rejected by the size gate:\n%s", logged)
	}
}

// sizeMismatchFallbackHandler builds a synchronous resolve whose listed
// candidates are A, B, and C under one neutral row. A and B are two indexer
// offers of the same release; C is a different release. The prober's behavior
// is supplied per candidate file path.
func sizeMismatchFallbackHandler(t *testing.T, file *models.MediaFile, candidates []VirtualPlaybackStream, rows map[string]*models.MediaFile, probe func(*models.MediaFile) (int64, error)) (*PlaybackHandler, *[]int, *[]models.VirtualFilePersistArgs) {
	t.Helper()
	var mu sync.Mutex
	var probes []int
	var saved []models.VirtualFilePersistArgs
	orderedRows := make([]*models.MediaFile, 0, len(candidates))
	for _, candidate := range candidates {
		if row := rows[candidate.URI]; row != nil {
			orderedRows = append(orderedRows, row)
		}
	}
	h := &PlaybackHandler{
		ServiceContext: context.Background(),
		FileVersionFetcher: testPlaybackFileVersionFetcher{
			byContent: map[string][]*models.MediaFile{file.ContentID: orderedRows},
		},
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return candidates, nil
		}),
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
			return "https://93.184.216.34/stream/" + virtualResultCandidateID(path), nil
		}),
		VirtualFileLookup: func(_ context.Context, path string) (*models.MediaFile, error) {
			return rows[path], nil
		},
		VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			mu.Lock()
			probes = append(probes, f.ID)
			mu.Unlock()
			served, err := probe(f)
			if err != nil {
				return nil, err
			}
			f.ProbedFormatSize = served
			f.Duration = 5400
			f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
			f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
			return f, nil
		},
		VirtualFileSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (int64, error) {
			mu.Lock()
			saved = append(saved, args)
			mu.Unlock()
			return 1, nil
		},
	}
	t.Cleanup(h.StopVirtualEvidence)
	_ = file
	return h, &probes, &saved
}

func sizeMismatchFallbackRow(id int, uri, release string, size int64) *models.MediaFile {
	return &models.MediaFile{
		ID: id, ContentID: "movie-tt-size-fallback", FilePath: uri, Container: "virtual",
		FileSize: size, ProviderReleaseName: release, ProviderReleaseSize: size,
		VirtualOwnerInstallationID: 5, MediaFolderID: 9, ProbeSource: "virtual",
	}
}

func sizeMismatchFallbackCandidate(id, uri, release string, size int64) VirtualPlaybackStream {
	// Deliberately no declared codecs/container: that keeps the synchronous
	// probe path (the one this test exercises) instead of the declared-metadata
	// fast path that hands the probe to a background worker.
	return VirtualPlaybackStream{
		ID: id, URI: uri, FileSize: size, ProviderReleaseName: release,
		OwnerInstallationID: 5,
	}
}

// TestResolveVirtualPlaybackMatchingFirstProbeProbesNoSiblings pins the
// no-op-when-the-provider-is-fixed property: when the selected row's first
// probe serves bytes matching its declared size, the sibling fallback must not
// run a single extra probe. The sibling path exists only after a mismatch.
func TestResolveVirtualPlaybackMatchingFirstProbeProbesNoSiblings(t *testing.T) {
	const (
		release = "Movie.2024.2160p.WEB-DL"
		size    = int64(8_000_000_000)
		uriA    = "virtual://movie/tt-size-fallback?result=treasure"
		uriB    = "virtual://movie/tt-size-fallback?result=althub"
	)
	file := &models.MediaFile{
		ID: 600, ContentID: "movie-tt-size-fallback", FilePath: "virtual://movie/tt-size-fallback",
		Container: "virtual", VirtualOwnerInstallationID: 5, MediaFolderID: 9,
	}
	rows := map[string]*models.MediaFile{
		uriA: sizeMismatchFallbackRow(601, uriA, release, size),
		uriB: sizeMismatchFallbackRow(602, uriB, release, size),
	}
	candidates := []VirtualPlaybackStream{
		sizeMismatchFallbackCandidate("treasure", uriA, release, size),
		sizeMismatchFallbackCandidate("althub", uriB, release, size),
	}
	h, probes, _ := sizeMismatchFallbackHandler(t, file, candidates, rows, func(*models.MediaFile) (int64, error) {
		return size, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	resolved, err := h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if resolved.URI != uriA {
		t.Fatalf("resolved URI = %q, want the first candidate %q", resolved.URI, uriA)
	}
	if len(*probes) != 1 {
		t.Fatalf("probe calls = %v, want exactly the first candidate; a matching first probe must not probe siblings", *probes)
	}
}

// TestResolveVirtualPlaybackObservationServeWritesInventoryWithoutStamping
// pins the split between usability and identity. The recording serve writes the
// measured inventory metadata-only so the versions list and plan see the real
// tracks, but it does not stamp the probe or pin the candidate; only the
// confirming serve that lands on the same bytes establishes the pin.
func TestResolveVirtualPlaybackObservationServeWritesInventoryWithoutStamping(t *testing.T) {
	const (
		release  = "Movie.2024.2160p.WEB-DL"
		declared = int64(6_330_000_000)
		served   = int64(5_570_000_000)
		runtime  = 90
	)
	uri := "virtual://movie/tt-observation?result=pin-1"
	file := &models.MediaFile{
		ID: 620, ContentID: "movie-tt-observation", FilePath: "virtual://movie/tt-observation",
		Container: "virtual", VirtualOwnerInstallationID: 5, MediaFolderID: 9,
	}
	rows := map[string]*models.MediaFile{
		uri: sizeMismatchFallbackRow(621, uri, release, declared),
	}
	candidates := []VirtualPlaybackStream{
		sizeMismatchFallbackCandidate("pin-1", uri, release, declared),
	}
	scanner.ResetVirtualServedSizeSnapshots()
	t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
	h, probes, saved := sizeMismatchFallbackHandler(t, file, candidates, rows, func(*models.MediaFile) (int64, error) {
		return served, nil
	})
	h.ItemLookup = staticRuntimeItemLookup{runtime: runtime}
	stickyKey := bestResultCacheKey(file.ContentID, virtualPlaybackNeutralKey(file.FilePath), file.VirtualOwnerInstallationID, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	resolved, err := h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if resolved.URI != uri {
		t.Fatalf("resolved URI = %q, want the served pin %q", resolved.URI, uri)
	}
	h.StopVirtualEvidence()
	if len(*saved) == 0 {
		t.Fatal("the recording serve persisted nothing, want its measured inventory")
	}
	for _, write := range *saved {
		if write.FileID != file.ID || write.StampProbe {
			t.Fatalf("first write = %+v, want the served row %d without a probe stamp", write, file.ID)
		}
		if write.Duration != runtime*60 {
			t.Fatalf("first write duration = %d, want the measured %d", write.Duration, runtime*60)
		}
	}
	if got := h.peekVirtualSticky(stickyKey); got != "" {
		t.Fatalf("the recording serve pinned %q, want no sticky pin before confirmation", got)
	}
	if len(*probes) != 1 {
		t.Fatalf("probes = %v, want one synchronous probe of the candidate", *probes)
	}

	// The next start re-probes, lands on the recorded size, and admits the pin.
	resolved, err = h.resolveVirtualPlaybackSource(
		httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil), file, "profile-1", false, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("second resolve error: %v", err)
	}
	if resolved.URI != uri {
		t.Fatalf("second resolved URI = %q, want %q", resolved.URI, uri)
	}
	h.StopVirtualEvidence()
	stamped := false
	for _, write := range *saved {
		if write.FileID == file.ID && write.StampProbe {
			stamped = true
		}
	}
	if !stamped {
		t.Fatalf("saved = %+v, want the confirming serve to stamp the probe evidence", *saved)
	}
	if got := h.peekVirtualSticky(stickyKey); got != uri {
		t.Fatalf("sticky pin = %q, want the confirmed candidate %q", got, uri)
	}
}

// TestResolveVirtualPlaybackImplausibleGapNeverAdmits pins the size plausibility
// cap at the identity seam. The production failure served 17.7 GB for a 3.78 GB
// declaration (4.7x) on a duration match. Such a gap is not a rounded listing,
// so no serve may ever establish identity for it. The bytes stay usable: each
// serve writes its measured inventory metadata-only, which is what lets the
// client report and play the file, but no serve stamps or pins it. The normal
// rounded band is covered by
// TestResolveVirtualPlaybackObservationServeWritesInventoryWithoutStamping
// above.
func TestResolveVirtualPlaybackImplausibleGapNeverAdmits(t *testing.T) {
	const (
		release  = "Movie.2023.2160p.WEB-DL"
		declared = int64(3_780_000_000)
		served   = int64(17_700_000_000) // 4.7x
		runtime  = 90
	)
	uri := "virtual://movie/tt-implausible?result=pin-1"
	file := &models.MediaFile{
		ID: 640, ContentID: "movie-tt-implausible", FilePath: "virtual://movie/tt-implausible",
		Container: "virtual", VirtualOwnerInstallationID: 5, MediaFolderID: 9,
	}
	rows := map[string]*models.MediaFile{
		uri: sizeMismatchFallbackRow(641, uri, release, declared),
	}
	candidates := []VirtualPlaybackStream{
		sizeMismatchFallbackCandidate("pin-1", uri, release, declared),
	}
	scanner.ResetVirtualServedSizeSnapshots()
	t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
	h, probes, saved := sizeMismatchFallbackHandler(t, file, candidates, rows, func(*models.MediaFile) (int64, error) {
		return served, nil
	})
	h.ItemLookup = staticRuntimeItemLookup{runtime: runtime}
	stickyKey := bestResultCacheKey(file.ContentID, virtualPlaybackNeutralKey(file.FilePath), file.VirtualOwnerInstallationID, "")

	for attempt := 1; attempt <= 2; attempt++ {
		resolved, err := h.resolveVirtualPlaybackSource(
			httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil), file, "profile-1", false, nil, "", "", 0, false)
		if err != nil {
			t.Fatalf("resolve %d error: %v", attempt, err)
		}
		if resolved.URI != uri {
			t.Fatalf("resolve %d URI = %q, want the served pin %q", attempt, resolved.URI, uri)
		}
	}
	h.StopVirtualEvidence()
	if len(*probes) != 2 {
		t.Fatalf("probes = %v, want one per start: an implausible serve must never become cache-confirmed", *probes)
	}
	// The bounded evidence buffer may coalesce identical metadata-only writes,
	// so the count is not the contract; the absence of any stamped write is.
	if len(*saved) == 0 {
		t.Fatal("implausible serves persisted nothing, want the measured inventory written")
	}
	for _, write := range *saved {
		if write.StampProbe {
			t.Fatalf("implausible serve stamped the row's identity: %+v", write)
		}
	}
	if got := h.peekVirtualSticky(stickyKey); got != "" {
		t.Fatalf("implausible serves pinned %q, want no sticky pin", got)
	}
}

// TestResolveVirtualPlaybackCacheFirstObservationWritesInventoryWithoutStamping
// pins the cache-first seam of the shared resolve. A probe-cache hit is a
// completed probe, but the classification deciding what the hit may be used for
// is not read-only: it records the pin's first served-size observation.
// Dropping that verdict and classifying the same bytes again in the outer
// finalization let the request read its own recording as the confirming serve,
// so the first-ever size gap became the pin's identity by persistence. The hit
// now carries its verdict to the finalization, which writes the measured
// inventory metadata-only; only a later serve landing on the recorded size
// stamps and pins.
func TestResolveVirtualPlaybackCacheFirstObservationWritesInventoryWithoutStamping(t *testing.T) { //nolint:funlen // one fixture drives the observation and confirming serve end to end
	const (
		release      = "Movie.2024.2160p.WEB-DL"
		declared     = int64(6_330_000_000)
		served       = int64(5_570_000_000)
		runtime      = 90
		contentID    = "movie-tt-cache-observation"
		neutralPath  = "virtual://movie/tt-cache-observation"
		candidateURI = neutralPath + "?result=pin-1"
	)
	file := &models.MediaFile{
		ID: 630, ContentID: contentID, FilePath: neutralPath,
		Container: "virtual", VirtualOwnerInstallationID: 5, MediaFolderID: 9,
	}
	rows := map[string]*models.MediaFile{
		candidateURI: sizeMismatchFallbackRow(631, candidateURI, release, declared),
	}
	candidates := []VirtualPlaybackStream{
		sizeMismatchFallbackCandidate("pin-1", candidateURI, release, declared),
	}
	scanner.ResetVirtualServedSizeSnapshots()
	t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
	h, probes, saved := sizeMismatchFallbackHandler(t, file, candidates, rows, func(*models.MediaFile) (int64, error) {
		return declared, nil
	})
	h.ItemLookup = staticRuntimeItemLookup{runtime: runtime}
	// A cache hit that records the pin's first observation: short of the
	// declaration, with a duration that proves the content.
	cacheHits := 0
	h.VirtualProbeCacheLookup = func(_ string, probeFile *models.MediaFile) *models.MediaFile {
		cacheHits++
		return &models.MediaFile{
			FilePath:         probeFile.FilePath,
			FileSize:         probeFile.FileSize,
			ProbedFormatSize: served,
			Duration:         runtime * 60,
			VideoTracks:      []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}},
			AudioTracks:      []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}},
		}
	}
	stickyKey := bestResultCacheKey(contentID, virtualPlaybackNeutralKey(neutralPath), file.VirtualOwnerInstallationID, "")

	first, err := h.resolveVirtualPlaybackSource(
		httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil), file, "profile-1", false, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if first.URI != candidateURI {
		t.Fatalf("resolved URI = %q, want the cached candidate %q", first.URI, candidateURI)
	}
	if cacheHits == 0 {
		t.Fatal("the probe cache was never consulted")
	}
	if len(*probes) != 0 {
		t.Fatalf("probes = %v, want none: the cache hit must skip the ffprobe", *probes)
	}
	// Check the write and pin first: they are what would turn one serve into identity.
	h.StopVirtualEvidence()
	if len(*saved) == 0 {
		t.Fatal("the recording cache replay persisted nothing, want its measured inventory")
	}
	for _, write := range *saved {
		if write.StampProbe {
			t.Fatal("the recording cache replay stamped unconfirmed bytes as identity")
		}
	}
	if got := h.peekVirtualSticky(stickyKey); got != "" {
		t.Fatalf("the recording cache replay pinned %q, want no sticky pin before confirmation", got)
	}
	if first.Provenance != ProbeProvenanceVerified {
		t.Fatalf("provenance = %q, want %q (served with the cached inventory)",
			first.Provenance, ProbeProvenanceVerified)
	}
	if !first.EvidenceObservation {
		t.Fatal("the cache hit that recorded the pin's first observation was served as identity")
	}
	if first.File == nil || first.File.ProbedFormatSize != served {
		t.Fatalf("file = %+v, want the cached bytes %d", first.File, served)
	}

	// The confirming serve lands on the size the first one recorded, so the pin
	// is admitted and this request persists and pins it.
	second, err := h.resolveVirtualPlaybackSource(
		httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil), file, "profile-1", false, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("second resolve error: %v", err)
	}
	if second.URI != candidateURI {
		t.Fatalf("second resolved URI = %q, want %q", second.URI, candidateURI)
	}
	if second.EvidenceObservation {
		t.Fatal("the confirming serve was still an observation")
	}
	h.StopVirtualEvidence()
	stamped := false
	for _, write := range *saved {
		if write.FileID != file.ID {
			t.Fatalf("saved = %+v, want every write on the resolved row %d", *saved, file.ID)
		}
		if write.StampProbe {
			stamped = true
		}
	}
	if !stamped {
		t.Fatalf("saved = %+v, want the confirming serve to stamp the probe evidence", *saved)
	}
	if got := h.peekVirtualSticky(stickyKey); got != candidateURI {
		t.Fatalf("sticky pin = %q, want the confirmed candidate %q", got, candidateURI)
	}
}

// TestFallbackSubstituteProbeGatesOnTheSubstitutesOwnDeclaration pins the
// stale-source fallback's evidence gate. The fallback adopts a release other
// than the stale row, so the declared-size baseline must be that substitute's
// listed size: the stale row's FileSize describes the bytes the session is
// leaving. Gating against it would admit served bytes that merely match the
// stale release; gating against the substitute's declaration applies the
// two-observation rule instead. A first duration-proven size gap serves without
// persist/adopt/stamp, the confirming serve adopts it, and a genuine mismatch
// is refused rather than written.
func TestFallbackSubstituteProbeGatesOnTheSubstitutesOwnDeclaration(t *testing.T) { //nolint:funlen,gocyclo // one fixture drives the five evidence outcomes the gate must distinguish; splitting it duplicates the substitute setup
	const (
		staleURI           = "virtual://movie/tt-fallback-gate?result=stale"
		substituteURI      = "virtual://movie/tt-fallback-gate?result=substitute"
		release            = "Movie.2024.2160p.WEB-DL"
		substituteDeclared = int64(6_330_000_000)
		served             = int64(5_570_000_000)
		// The stale row declares exactly the served byte count, so a gate that
		// wrongly used it as the baseline would admit the substitute at once.
		staleDeclared = served
		runtime       = 90
	)
	newFixture := func(t *testing.T, probedDuration int) (*PlaybackHandler, *[]models.VirtualFilePersistArgs) {
		t.Helper()
		var saved []models.VirtualFilePersistArgs
		h := &PlaybackHandler{
			VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
				return []VirtualPlaybackStream{{
					ID: "substitute", URI: substituteURI, FileSize: substituteDeclared,
					ProviderReleaseName: release, OwnerInstallationID: 5,
				}}, nil
			}),
			VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
				return "https://93.184.216.34/stream/" + virtualResultCandidateID(path), nil
			}),
			VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
				f.ProbedFormatSize = served
				f.Duration = probedDuration
				f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
				f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
				return f, nil
			},
			VirtualFileSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (int64, error) {
				saved = append(saved, args)
				return 1, nil
			},
			ItemLookup: staticRuntimeItemLookup{runtime: runtime},
		}
		t.Cleanup(h.StopVirtualEvidence)
		return h, &saved
	}
	staleRow := func() *models.MediaFile {
		return &models.MediaFile{
			ID: 700, ContentID: "movie-tt-fallback-gate", FilePath: staleURI,
			Container: "virtual", VirtualOwnerInstallationID: 5, MediaFolderID: 9,
			FileSize: staleDeclared,
		}
	}
	run := func(t *testing.T, h *PlaybackHandler) *resolvedVirtualPlaybackSource {
		t.Helper()
		return h.fallbackResolveStaleVirtualSource(
			context.Background(), staleRow(), 1, "profile-1", virtualFallbackEligibility{})
	}

	t.Run("observation serves without writing", func(t *testing.T) {
		scanner.ResetVirtualServedSizeSnapshots()
		t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
		h, saved := newFixture(t, 90*60)

		first := run(t, h)
		if first == nil {
			t.Fatal("fallback returned nil, want the substitute served")
		}
		if !first.EvidenceObservation {
			t.Fatalf("first serve observation = false, want the pin's first evidence observation")
		}
		if first.Provenance != ProbeProvenanceVerified {
			t.Fatalf("first serve provenance = %q, want %q (served with the probed inventory)",
				first.Provenance, ProbeProvenanceVerified)
		}
		if first.File == nil || first.File.ProbedFormatSize != served {
			t.Fatalf("first serve file = %+v, want the probed bytes %d", first.File, served)
		}
		h.StopVirtualEvidence()
		if len(*saved) != 0 {
			t.Fatalf("the recording serve persisted %+v, want no catalog write", *saved)
		}

		second := run(t, h)
		if second == nil {
			t.Fatal("second fallback returned nil, want the confirmed substitute")
		}
		if second.EvidenceObservation {
			t.Fatal("the confirming serve was still an observation")
		}
		h.StopVirtualEvidence()
		if len(*saved) != 1 {
			t.Fatalf("saved = %+v, want one inventory on the confirming serve", *saved)
		}
		if (*saved)[0].AdoptPath != substituteURI || !(*saved)[0].StampProbe {
			t.Fatalf("saved = %+v, want the substitute adopted and stamped", (*saved)[0])
		}
	})

	t.Run("cache hit that diverges from the observation writes inventory without identity", func(t *testing.T) {
		scanner.ResetVirtualServedSizeSnapshots()
		t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
		h, saved := newFixture(t, 90*60)

		// The probe cache holds bytes measured before an observation was
		// recorded for this pin. Against the pin's recorded observation those
		// bytes are unverified, so the cache replay must serve them as an
		// observation rather than let the caller adopt and stamp them.
		observed := served
		cacheHits := 0
		h.VirtualProbeCacheLookup = func(_ string, probeFile *models.MediaFile) *models.MediaFile {
			cacheHits++
			return &models.MediaFile{
				FilePath: probeFile.FilePath,
				FileSize: probeFile.FileSize,
				// These bytes match the substitute's declaration, so the cache
				// would have admitted them when they were measured.
				ProbedFormatSize: substituteDeclared,
				Duration:         90 * 60,
				VideoTracks:      []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}},
				AudioTracks:      []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}},
			}
		}
		proberCalls := 0
		h.VirtualPlaybackSourceProber = func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			proberCalls++
			return f, nil
		}
		pin := scanner.VirtualServedSizePin(substituteURI, 5)
		// The cached inventory carries no subtitle tracks, so its snapshot key
		// is the empty-inventory token, not a blank string.
		inventoryToken := scanner.VirtualServedSizeSubtitleToken(nil)
		if !scanner.RecordVirtualServedSizeSnapshotIfProven(pin, substituteDeclared, observed, 90*60, runtime, inventoryToken) {
			t.Fatal("failed to record the pin's served-size observation")
		}

		got := run(t, h)
		if got == nil {
			t.Fatal("fallback returned nil, want the cached substitute served")
		}
		if cacheHits == 0 {
			t.Fatal("the probe cache was never consulted")
		}
		if proberCalls != 0 {
			t.Fatalf("synchronous prober calls = %d, want 0: the cache hit must skip the ffprobe", proberCalls)
		}
		if !got.EvidenceObservation {
			t.Fatal("a cached probe that diverges from the observation was served as identity")
		}
		if got.Provenance != ProbeProvenanceVerified {
			t.Fatalf("provenance = %q, want %q (served with the cached inventory)",
				got.Provenance, ProbeProvenanceVerified)
		}
		if got.File == nil || got.File.ProbedFormatSize != substituteDeclared {
			t.Fatalf("file = %+v, want the cached bytes %d", got.File, substituteDeclared)
		}
		h.StopVirtualEvidence()
		if len(*saved) == 0 {
			t.Fatal("the unconfirmed cache replay persisted no inventory")
		}
		for _, write := range *saved {
			if write.StampProbe {
				t.Fatal("the unconfirmed cache replay stamped the bytes as identity")
			}
		}
	})

	t.Run("cache hit that records the first observation does not confirm itself", func(t *testing.T) {
		scanner.ResetVirtualServedSizeSnapshots()
		t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
		h, saved := newFixture(t, 90*60)

		// No snapshot exists yet, so the cache seam's classification records the
		// pin's first observation and reports the hit as an observation. Gating
		// the same bytes a second time would compare them against that
		// just-recorded snapshot, read the recording as a confirming serve, and
		// persist one unproven serve as the pin's identity.
		cacheHits := 0
		h.VirtualProbeCacheLookup = func(_ string, probeFile *models.MediaFile) *models.MediaFile {
			cacheHits++
			return &models.MediaFile{
				FilePath:         probeFile.FilePath,
				FileSize:         probeFile.FileSize,
				ProbedFormatSize: served,
				Duration:         90 * 60,
				VideoTracks:      []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}},
				AudioTracks:      []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}},
			}
		}
		proberCalls := 0
		h.VirtualPlaybackSourceProber = func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			proberCalls++
			return f, nil
		}

		got := run(t, h)
		if got == nil {
			t.Fatal("fallback returned nil, want the cached substitute served")
		}
		if cacheHits == 0 {
			t.Fatal("the probe cache was never consulted")
		}
		if proberCalls != 0 {
			t.Fatalf("synchronous prober calls = %d, want 0: the cache hit must skip the ffprobe", proberCalls)
		}
		if !got.EvidenceObservation {
			t.Fatal("the serve that recorded the pin's first observation was served as identity")
		}
		if got.Provenance != ProbeProvenanceVerified {
			t.Fatalf("provenance = %q, want %q (served with the cached inventory)",
				got.Provenance, ProbeProvenanceVerified)
		}
		if got.File == nil || got.File.ProbedFormatSize != served {
			t.Fatalf("file = %+v, want the cached bytes %d", got.File, served)
		}
		// The one classification that ran is the cache seam's, and it recorded
		// the observation exactly once: the replayed bytes are the snapshot's
		// baseline, not a second measurement that agrees with it.
		pin := scanner.VirtualServedSizePin(substituteURI, 5)
		observation, ok := scanner.VirtualServedSizeSnapshotFor(pin, substituteDeclared, scanner.VirtualServedSizeSubtitleToken(got.File.SubtitleTracks))
		if !ok || observation.ObservedBytes != served {
			t.Fatalf("recorded observation = %+v (present %v), want the served bytes %d", observation, ok, served)
		}
		h.StopVirtualEvidence()
		if len(*saved) == 0 {
			t.Fatal("the recording cache replay persisted no inventory")
		}
		for _, write := range *saved {
			if write.StampProbe {
				t.Fatal("the recording cache replay stamped the bytes as identity")
			}
		}
	})

	t.Run("mismatch is refused", func(t *testing.T) {
		scanner.ResetVirtualServedSizeSnapshots()
		t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
		h, saved := newFixture(t, 10*60) // wrong runtime: the size gap is not excused

		if got := run(t, h); got != nil {
			t.Fatalf("fallback returned %+v, want nil for a mismatched substitute", got)
		}
		h.StopVirtualEvidence()
		if len(*saved) != 0 {
			t.Fatalf("the mismatched serve persisted %+v, want no catalog write", *saved)
		}
	})

	t.Run("matching declaration still writes", func(t *testing.T) {
		scanner.ResetVirtualServedSizeSnapshots()
		t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
		h, saved := newFixture(t, 90*60)
		h.VirtualPlaybackSourceProber = func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			f.ProbedFormatSize = substituteDeclared
			f.Duration = 90 * 60
			f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
			f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
			return f, nil
		}

		got := run(t, h)
		if got == nil {
			t.Fatal("fallback returned nil, want the matching substitute")
		}
		if got.EvidenceObservation {
			t.Fatal("a size-matching serve was treated as an observation")
		}
		h.StopVirtualEvidence()
		if len(*saved) != 1 || (*saved)[0].AdoptPath != substituteURI {
			t.Fatalf("saved = %+v, want the substitute adopted", *saved)
		}
	})
}

// TestResolveVirtualPlaybackIdentityContradictionAdoptsSameReleaseSibling pins
// the fallback contract: the selected row's probe measured a different release
// (a runtime contradiction), so the same release's second indexer row is probed
// and adopted, and its verified inventory is persisted onto that sibling row
// only. The selected row keeps the short wrong-source damper, and the different
// release (C) is never probed.
func TestResolveVirtualPlaybackIdentityContradictionAdoptsSameReleaseSibling(t *testing.T) {
	const (
		release = "Movie.2024.2160p.WEB-DL"
		size    = int64(8_000_000_000)
		uriA    = "virtual://movie/tt-size-fallback?result=treasure"
		uriB    = "virtual://movie/tt-size-fallback?result=althub"
		uriC    = "virtual://movie/tt-size-fallback?result=ninja"
		other   = "Movie.2024.720p.WEB-DL"
		otherSz = int64(2_000_000_000)
	)
	keyA := virtualProbeFailureKey(uriA, 5)
	keyB := virtualProbeFailureKey(uriB, 5)
	for _, key := range []string{keyA, keyB, virtualProbeFailureKey(uriC, 5)} {
		virtualProbeFailures.clear(key)
		virtualProbeFailures.clearSizeMismatch(key)
	}
	t.Cleanup(func() {
		for _, key := range []string{keyA, keyB, virtualProbeFailureKey(uriC, 5)} {
			virtualProbeFailures.clear(key)
			virtualProbeFailures.clearSizeMismatch(key)
		}
	})

	file := &models.MediaFile{
		ID: 610, ContentID: "movie-tt-size-fallback", FilePath: "virtual://movie/tt-size-fallback",
		Container: "virtual", VirtualOwnerInstallationID: 5, MediaFolderID: 9,
	}
	rows := map[string]*models.MediaFile{
		uriA: sizeMismatchFallbackRow(611, uriA, release, size),
		uriB: sizeMismatchFallbackRow(612, uriB, release, size),
		uriC: sizeMismatchFallbackRow(613, uriC, other, otherSz),
	}
	candidates := []VirtualPlaybackStream{
		sizeMismatchFallbackCandidate("treasure", uriA, release, size),
		sizeMismatchFallbackCandidate("althub", uriB, release, size),
		sizeMismatchFallbackCandidate("ninja", uriC, other, otherSz),
	}
	h, probes, saved := sizeMismatchFallbackHandler(t, file, candidates, rows, func(f *models.MediaFile) (int64, error) {
		if f.FilePath == uriA {
			// A stale cache fill of a shorter different release.
			return size * 88 / 100, nil
		}
		return size, nil
	})
	// A is a genuine different file, not merely a size gap: its measured
	// runtime contradicts the item's catalog runtime. B serves the declared
	// release and is adopted.
	h.ItemLookup = staticRuntimeItemLookup{runtime: 51}
	baseProbe := h.VirtualPlaybackSourceProber
	h.VirtualPlaybackSourceProber = func(ctx context.Context, sourceURL string, f *models.MediaFile) (*models.MediaFile, error) {
		probed, err := baseProbe(ctx, sourceURL, f)
		if err != nil || probed == nil {
			return probed, err
		}
		if f.FilePath == uriA {
			probed.Duration = 600 // 10 minutes against a 51-minute runtime
		} else {
			probed.Duration = 51 * 60
		}
		return probed, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	resolved, err := h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "", 0, false)
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if resolved.URI != uriB {
		t.Fatalf("resolved URI = %q, want the matching same-release sibling %q", resolved.URI, uriB)
	}
	if resolved.File == nil || resolved.File.ID != 612 {
		t.Fatalf("resolved file = %+v, want the sibling row 612", resolved.File)
	}
	if len(*probes) != 2 || (*probes)[0] != 611 || (*probes)[1] != 612 {
		t.Fatalf("probe order = %v, want [611 612]: only the selected row and its same-release sibling", *probes)
	}
	if !virtualProbeFailures.recent(virtualProbeSizeMismatchKey(keyA)) {
		t.Fatal("the selected row did not keep its short wrong-source damper")
	}
	if virtualProbeFailures.recent(virtualProbeSizeMismatchKey(keyB)) {
		t.Fatal("the adopted sibling kept a wrong-source damper after matching bytes")
	}
	if virtualProbeFailures.recent(keyA) {
		t.Fatal("a served-size mismatch must not feed the dead-candidate damper")
	}

	h.StopVirtualEvidence()
	if len(*saved) != 1 {
		t.Fatalf("persisted %d inventories, want exactly one on the sibling row: %+v", len(*saved), *saved)
	}
	write := (*saved)[0]
	if write.FileID != 612 {
		t.Fatalf("inventory persisted on file %d, want only the sibling 612", write.FileID)
	}
	if write.ExpectedFilePath != uriB {
		t.Fatalf("inventory expected path = %q, want the sibling's own path %q", write.ExpectedFilePath, uriB)
	}
}

// TestBackgroundProbeIdentityContradictionAdoptsSameReleaseSibling pins the
// shared probe tail's fallback: the deferred/background probe measured a runtime
// that contradicts the selected row's declared release, so the release's sibling
// row (second in the version list's order) is probed and adopted. The durable
// write commits on the sibling's row only, so the session's committed inventory
// belongs to the bytes it verified.
func TestBackgroundProbeIdentityContradictionAdoptsSameReleaseSibling(t *testing.T) {
	const (
		release = "Movie.2024.2160p.WEB-DL"
		size    = int64(8_000_000_000)
		uriA    = "virtual://movie/tt-background-sibling?result=treasure"
		uriB    = "virtual://movie/tt-background-sibling?result=althub"
		uriC    = "virtual://movie/tt-background-sibling?result=ninja"
		urlA    = "https://93.184.216.34/stream/treasure"
		urlB    = "https://93.184.216.34/stream/althub"
		urlC    = "https://93.184.216.34/stream/ninja"
	)
	rowA := sizeMismatchFallbackRow(811, uriA, release, size)
	rowA.ContentID = "movie-tt-background-sibling"
	rowA.ResolvedURL = urlA
	rowB := sizeMismatchFallbackRow(812, uriB, release, size)
	rowB.ContentID = rowA.ContentID
	rowB.ResolvedURL = urlB
	rowC := sizeMismatchFallbackRow(813, uriC, "Movie.2024.720p.WEB-DL", 2_000_000_000)
	rowC.ContentID = rowA.ContentID
	rowC.ResolvedURL = urlC

	keyA := virtualProbeFailureKey(uriA, 5)
	virtualProbeFailures.clear(keyA)
	virtualProbeFailures.clearSizeMismatch(keyA)
	t.Cleanup(func() {
		virtualProbeFailures.clear(keyA)
		virtualProbeFailures.clearSizeMismatch(keyA)
	})

	var mu sync.Mutex
	var probes []string
	var saved []models.VirtualFilePersistArgs
	h := &PlaybackHandler{
		ServiceContext: context.Background(),
		FileVersionFetcher: testPlaybackFileVersionFetcher{
			byContent: map[string][]*models.MediaFile{rowA.ContentID: {rowA, rowB, rowC}},
		},
		VirtualPlaybackSourceProber: func(_ context.Context, sourceURL string, f *models.MediaFile) (*models.MediaFile, error) {
			mu.Lock()
			probes = append(probes, sourceURL)
			mu.Unlock()
			f.ProbedFormatSize = size
			if sourceURL == urlA {
				// A stale cache fill of a different release: the size gap and a
				// runtime that contradicts the 51-minute catalog runtime.
				f.ProbedFormatSize = size * 88 / 100
				f.Duration = 600
			} else {
				f.Duration = 51 * 60
			}
			f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
			f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
			return f, nil
		},
		VirtualFileMetadataSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			mu.Lock()
			saved = append(saved, args)
			mu.Unlock()
			return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
		},
	}

	candA := virtualPlaybackStreamFromFile(rowA)
	outcome, committed := h.probeVirtualSourceAndPersistWith(
		context.Background(), "sticky-background-sibling", rowA, urlA, *rowA, *candA,
		noCandidateIdentity, 51, 5, true, nil,
	)
	if outcome != probeOutcomeVerified {
		t.Fatalf("outcome = %q, want verified from the adopted sibling", outcome)
	}
	if !committed.committed() || committed.fileID != rowB.ID {
		t.Fatalf("committed inventory = %+v, want the sibling row %d", committed, rowB.ID)
	}
	if len(probes) != 2 || probes[0] != urlA || probes[1] != urlB {
		t.Fatalf("probes = %v, want [selected sibling-url]: only the selected row and its same-release sibling", probes)
	}
	if !virtualProbeFailures.recent(virtualProbeSizeMismatchKey(keyA)) {
		t.Fatal("the selected row did not keep its short wrong-source damper")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saved) != 1 || saved[0].FileID != rowB.ID {
		t.Fatalf("saved = %+v, want exactly one inventory on the sibling row %d", saved, rowB.ID)
	}
}

// staticRuntimeItemLookup answers the catalog runtime the probe path compares a
// measured duration against.
type staticRuntimeItemLookup struct{ runtime int }

func (s staticRuntimeItemLookup) GetByID(context.Context, string) (*models.MediaItem, error) {
	return &models.MediaItem{Runtime: s.runtime}, nil
}

// A served-size gap is usable inventory immediately: the pre-play probe chain
// writes its measured tracks metadata-only and publishes versions_updated, so an
// open detail page can show the real file. The second serve of the same bytes
// adds the probe stamp and confirms the release identity.
func TestScheduleDefaultFileProbePublishesServedSizeGapInventory(t *testing.T) {
	const (
		declaredBytes = int64(6_330_000_000)
		servedBytes   = int64(5_570_000_000)
	)
	scanner.ResetVirtualServedSizeSnapshots()
	t.Cleanup(scanner.ResetVirtualServedSizeSnapshots)
	row := probeChainRow(740, "virtual://movie/probe-chain?result=short", nil)
	row.FileSize = declaredBytes
	reader := &probeChainVersionReader{
		files: []*models.MediaFile{row},
		rows:  map[int]*models.MediaFile{row.ID: row},
	}
	bus := &recordingEventBus{}
	hub := notifications.NewHub("probe-chain-short-posting", bus)
	var saved []models.VirtualFilePersistArgs
	h := &PlaybackHandler{
		ServiceContext:     context.Background(),
		EventsHub:          hub.EventsHub(),
		ItemLookup:         staticRuntimeItemLookup{runtime: 51},
		DefaultProbeTarget: NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: func(_ context.Context, sourceURL string, f *models.MediaFile) (*models.MediaFile, error) {
			f.ProbedFormatSize = servedBytes
			f.Duration = 3072
			f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
			f.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
			f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
			return f, nil
		},
		VirtualFileMetadataSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			saved = append(saved, args)
			return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
		},
	}
	filter := catalog.AccessFilter{SelectedFileID: row.ID, AllowedLibraryIDs: []int{77}}

	// First serve: the inventory is written metadata-only, so the detail page
	// gets the real track list and a versions_updated announcement.
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), filter)
	h.probeChainWG.Wait()
	if len(saved) != 1 {
		t.Fatalf("the recording serve persisted %+v, want one metadata-only inventory", saved)
	}
	if saved[0].StampProbe {
		t.Fatal("the recording serve stamped unconfirmed bytes as identity")
	}
	if types := bus.types(); len(types) == 0 {
		t.Fatal("the recording serve wrote inventory but announced no catalog change")
	}

	// Second serve on the same bytes: the confirming serve stamps the inventory.
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), filter)
	h.probeChainWG.Wait()
	if len(saved) != 2 || saved[1].FileID != row.ID {
		t.Fatalf("saved = %+v, want the observation then the confirming inventory on row %d", saved, row.ID)
	}
	if !saved[1].StampProbe {
		t.Fatal("the confirming serve did not stamp the probe evidence")
	}
}
