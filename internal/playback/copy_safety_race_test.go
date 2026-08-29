package playback

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeCopySafetyScanner struct {
	mu    sync.Mutex
	needs bool
	multi bool
	// stale makes the scan report a verdict for a generation of the file the
	// row has moved past.
	stale bool
	err   error
	// known and knownMulti stand in for the process memo: a verdict this
	// replica reached whose write to media_files never landed, and which is
	// therefore invisible on the row.
	known      bool
	knownMulti bool
	scans      int
	active     int
	maxActive  int
	release    chan struct{}
	scanning   chan struct{}
}

func (s *fakeCopySafetyScanner) NeedsCopySafetyScan(*models.MediaFile) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needs
}

func (s *fakeCopySafetyScanner) ScanCopySafety(context.Context, *models.MediaFile) (bool, bool, error) {
	s.mu.Lock()
	s.scans++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.scanning != nil {
		s.scanning <- struct{}{}
	}
	if s.release != nil {
		<-s.release
	}
	return s.multi, s.stale, s.err
}

// KnownCopySafetyVerdict mirrors the real ensurer: the process memo first, then
// the verdict the row carries.
func (s *fakeCopySafetyScanner) KnownCopySafetyVerdict(_ context.Context, file *models.MediaFile) (bool, bool) {
	s.mu.Lock()
	known, multi := s.known, s.knownMulti
	s.mu.Unlock()
	if known {
		return multi, true
	}
	if file == nil {
		return false, false
	}
	return file.PersistedVideoCopyVerdict()
}

func (s *fakeCopySafetyScanner) scanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scans
}

// peakConcurrency is the largest number of scans that were ever running at the
// same time.
func (s *fakeCopySafetyScanner) peakConcurrency() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxActive
}

type fakeFileLoader struct {
	mu   sync.Mutex
	file *models.MediaFile
	// later, when set, is what every read after the first returns — the row as
	// another replica has since rewritten it.
	later *models.MediaFile
	err   error
	loads int
}

func (l *fakeFileLoader) GetByID(context.Context, int) (*models.MediaFile, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loads++
	if l.loads > 1 && l.later != nil {
		return l.later, l.err
	}
	return l.file, l.err
}

func (l *fakeFileLoader) loadCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loads
}

// raceFixture wires a racer whose notifier reports into a fake control, so a
// multi-PPS verdict is observable as a session stop.
func raceFixture(t *testing.T, scanner *fakeCopySafetyScanner) (*CopySafetyRace, *SessionManager, *fakeCopySafetyControl) {
	t.Helper()
	return raceFixtureForFile(t, scanner, &models.MediaFile{
		ID:          100,
		CodecVideo:  "h264",
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
	})
}

// raceFixtureForFile is raceFixture over a specific media file, for the cases
// that care about what the row carries — a persisted verdict, above all.
func raceFixtureForFile(t *testing.T, scanner *fakeCopySafetyScanner, file *models.MediaFile) (*CopySafetyRace, *SessionManager, *fakeCopySafetyControl) {
	t.Helper()
	return raceFixtureWithLoader(t, scanner, &fakeFileLoader{file: file})
}

// raceFixtureWithLoader is raceFixtureForFile over a loader the caller controls,
// for the cases that care about the row changing between two reads.
func raceFixtureWithLoader(t *testing.T, scanner *fakeCopySafetyScanner, loader *fakeFileLoader) (*CopySafetyRace, *SessionManager, *fakeCopySafetyControl) {
	t.Helper()
	sessions := NewSessionManager(0, 0)
	hub := NewRealtimeHub()
	tracker := NewCommandTracker()
	t.Cleanup(tracker.Close)
	control := &fakeCopySafetyControl{}
	notifier := NewCopySafetyNotifier(sessions, nil, NewCommandDispatcher(sessions, hub, tracker), control)
	// These tests are about the race, not about waiting out the window a
	// just-started session gets before it can be stopped.
	notifier.settle = 0
	return NewCopySafetyRace(scanner, loader, notifier), sessions, control
}

func waitForStop(t *testing.T, control *fakeCopySafetyControl, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stopped := control.stoppedSessions()
		if len(stopped) == 1 && stopped[0] == sessionID {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stopped = %v, want the copy-routed session %q", stopped, sessionID)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCopySafetyRaceNotifiesOnMultiPPS(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: true, multi: true}
	race, sessions, control := raceFixture(t, scanner)
	session, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	waitForStop(t, control, session.ID)
}

// A copy-safe verdict is the common case and must be silent: the plan the
// client is already running stays valid.
func TestCopySafetyRaceKeepsSessionsWhenCopySafe(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: true, multi: false}
	race, sessions, control := raceFixture(t, scanner)
	if _, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	waitForScans(t, scanner, 1)
	time.Sleep(20 * time.Millisecond)
	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want no session touched by a copy-safe verdict", stopped)
	}
}

// An inconclusive scan proves nothing, so sessions keep playing. Failing closed
// here would kill a live playback over a transient ffmpeg or storage error.
func TestCopySafetyRaceLeavesSessionsAloneOnScanError(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: true, err: errors.New("ffmpeg exploded")}
	race, sessions, control := raceFixture(t, scanner)
	if _, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	waitForScans(t, scanner, 1)
	time.Sleep(20 * time.Millisecond)
	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want live sessions untouched after an inconclusive scan", stopped)
	}
}

// A failed local scan and a verdict reached elsewhere can happen at once, and
// the combination is the worst of both: the persisted verdict suppresses every
// later local scan of the file, so the failure that just returned empty-handed
// is this replica's last chance to notice it. Without the re-read, the sessions
// this replica owns keep the condemned route until some unrelated future race
// happens to load the row.
func TestCopySafetyRaceAppliesAVerdictPersistedWhileTheScanFailed(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: true, err: errors.New("ffmpeg exploded")}
	loader := &fakeFileLoader{
		file: &models.MediaFile{
			ID:          100,
			CodecVideo:  "h264",
			VideoTracks: []models.VideoTrack{{Codec: "h264"}},
		},
		later: fileWithPersistedVerdict(true),
	}
	race, sessions, control := raceFixtureWithLoader(t, scanner, loader)
	session, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	waitForStop(t, control, session.ID)
}

// The mirror image: a scan that fails over a row that still carries no verdict
// anywhere proves nothing, and the re-read must not invent one.
func TestCopySafetyRaceScanFailureWithNoVerdictAnywhereStaysSilent(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: true, err: errors.New("ffmpeg exploded")}
	loader := &fakeFileLoader{
		file: &models.MediaFile{
			ID:          100,
			CodecVideo:  "h264",
			VideoTracks: []models.VideoTrack{{Codec: "h264"}},
		},
		later: fileWithPersistedVerdict(false),
	}
	race, sessions, control := raceFixtureWithLoader(t, scanner, loader)
	if _, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	// The re-read after the failed scan is the last thing the pass does, and it
	// is the only thing that could have produced a stop. Waiting for it is what
	// makes "nothing was stopped" an assertion about a finished pass rather
	// than about a moment in time.
	waitForLoads(t, loader, 2)
	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want live sessions untouched when no verdict exists anywhere", stopped)
	}
}

func TestCopySafetyRaceSkipsFilesWithNothingToScan(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: false, multi: true}
	race, _, control := raceFixture(t, scanner)

	race.RaceScan(100)

	time.Sleep(20 * time.Millisecond)
	if got := scanner.scanCount(); got != 0 {
		t.Fatalf("scans = %d, want 0 when the verdict is already known", got)
	}
	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want none", stopped)
	}
}

// Every start, replan and watch-page load for a popular file asks for the same
// race; only one goroutine may be in flight for it, and a burst arriving while
// it runs collapses into a single follow-up pass rather than one scan each.
func TestCopySafetyRaceDedupesInFlightScans(t *testing.T) {
	scanner := &fakeCopySafetyScanner{
		needs:    true,
		release:  make(chan struct{}),
		scanning: make(chan struct{}, 8),
	}
	loader := &fakeFileLoader{file: &models.MediaFile{
		ID:          100,
		CodecVideo:  "h264",
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
	}}
	race, _, _ := raceFixtureWithLoader(t, scanner, loader)

	race.RaceScan(100)
	<-scanner.scanning
	for i := 0; i < 5; i++ {
		race.RaceScan(100)
	}
	close(scanner.release)

	// One follow-up pass for the whole burst: two row reads, and never five.
	waitForLoads(t, loader, 2)
	if got := scanner.scanCount(); got > 2 {
		t.Fatalf("scans = %d, want at most 2 — the in-flight scan and one follow-up for the burst", got)
	}
}

// The dedupe used to drop a request that arrived mid-scan, and that dropped
// request was sometimes the only thing that would have looked at a session.
// A replan can commit a replacement stream-copy while the scan runs: the pass
// already under way lists the sessions as they were, and the notifier excludes
// the ones it disposed of from its own late sweep, so the replacement is
// considered by nobody. The follow-up pass is what re-lists them.
func TestCopySafetyRaceReconsidersSessionsThatAppearedDuringAScan(t *testing.T) {
	scanner := &fakeCopySafetyScanner{
		needs:    true,
		multi:    true,
		release:  make(chan struct{}),
		scanning: make(chan struct{}, 1),
	}
	race, sessions, control := raceFixture(t, scanner)

	race.RaceScan(100)
	waitForScanning(t, scanner, "the first pass")
	// Asked for while the scan is running: under the old dedupe this request
	// was dropped on the floor.
	race.RaceScan(100)
	releaseScan(t, scanner, "the first pass")

	// The second scan starting proves the first pass — its scan, its verdict
	// and its notification — is over. No session existed for it to touch, so
	// the follow-up pass is the only thing left that can reach one.
	waitForScanning(t, scanner, "the follow-up pass")
	late, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	releaseScan(t, scanner, "the follow-up pass")

	waitForStop(t, control, late.ID)
}

// waitForScanning blocks until a pass is inside its scan, failing rather than
// hanging when the pass never comes.
func waitForScanning(t *testing.T, scanner *fakeCopySafetyScanner, pass string) {
	t.Helper()
	select {
	case <-scanner.scanning:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s never started scanning", pass)
	}
}

// releaseScan lets a waiting scan return, failing rather than hanging when
// nothing is waiting for it.
func releaseScan(t *testing.T, scanner *fakeCopySafetyScanner, pass string) {
	t.Helper()
	select {
	case scanner.release <- struct{}{}:
	case <-time.After(2 * time.Second):
		t.Fatalf("nothing was waiting to be released for %s", pass)
	}
}

// A file rewritten in place while the scan reads it produces a verdict about
// bytes the server is no longer serving. Persisting it would re-validate a dead
// generation, and notifying on it would tear down sessions playing the
// replacement over evidence from the file it replaced.
func TestCopySafetyRaceIgnoresAVerdictForASupersededGeneration(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: true, multi: true, stale: true}
	race, sessions, control := raceFixture(t, scanner)
	if _, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	waitForScans(t, scanner, 1)
	time.Sleep(20 * time.Millisecond)
	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want no session withdrawn on a verdict for bytes it is not playing", stopped)
	}
}

// A verdict this process reached and failed to write is invisible on the row,
// and the row is all a later pass would otherwise consult. The scanner is the
// only thing that can still answer, so the pass with nothing left to scan asks
// it rather than the row.
func TestCopySafetyRaceAppliesAnUnpersistedUnsafeVerdict(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: false, known: true, knownMulti: true}
	race, sessions, control := raceFixture(t, scanner)
	session, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	waitForStop(t, control, session.ID)
	if got := scanner.scanCount(); got != 0 {
		t.Fatalf("scans = %d, want 0 for a verdict this process already holds", got)
	}
}

// VideoCopyUnsafeKnown is what the serve paths gate a revived stream-copy on,
// so it has to answer from the same place the racer does — including the memo
// the row knows nothing about.
func TestCopySafetyRaceVideoCopyUnsafeKnown(t *testing.T) {
	unverdicted := &models.MediaFile{
		ID:          100,
		CodecVideo:  "h264",
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
	}

	for _, tc := range []struct {
		name    string
		scanner *fakeCopySafetyScanner
		file    *models.MediaFile
		want    bool
	}{
		{name: "unknown", scanner: &fakeCopySafetyScanner{}, file: unverdicted},
		{name: "persisted unsafe", scanner: &fakeCopySafetyScanner{}, file: fileWithPersistedVerdict(true), want: true},
		{name: "persisted safe", scanner: &fakeCopySafetyScanner{}, file: fileWithPersistedVerdict(false)},
		{
			name:    "unpersisted unsafe",
			scanner: &fakeCopySafetyScanner{known: true, knownMulti: true},
			file:    unverdicted,
			want:    true,
		},
		{
			name:    "unpersisted safe",
			scanner: &fakeCopySafetyScanner{known: true},
			file:    unverdicted,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			race, _, _ := raceFixtureForFile(t, tc.scanner, tc.file)
			if got := race.VideoCopyUnsafeKnown(t.Context(), tc.file); got != tc.want {
				t.Fatalf("VideoCopyUnsafeKnown() = %v, want %v", got, tc.want)
			}
		})
	}

	var nilRace *CopySafetyRace
	if nilRace.VideoCopyUnsafeKnown(t.Context(), unverdicted) {
		t.Fatal("VideoCopyUnsafeKnown() on a nil racer = true, want false")
	}
}

// fileWithPersistedVerdict is a media file row whose multi-PPS verdict is
// already recorded and still describes the file, as it would be on a replica
// that loads the row after another replica wrote it.
func fileWithPersistedVerdict(multi bool) *models.MediaFile {
	size := int64(4096)
	return &models.MediaFile{
		ID:                  100,
		CodecVideo:          "h264",
		VideoTracks:         []models.VideoTrack{{Codec: "h264", MultiplePPS: &multi}},
		FileSize:            size,
		MultiplePPS:         &multi,
		MultiplePPSScanSize: &size,
	}
}

// Another replica can reach the verdict first: it persists it, notifies its own
// sessions, and cannot reach ours. Loading a row that already carries an unsafe
// verdict therefore has to withdraw this replica's copy-routed sessions even
// though there is nothing left to scan.
func TestCopySafetyRaceAppliesPersistedUnsafeVerdict(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: false}
	race, sessions, control := raceFixtureForFile(t, scanner, fileWithPersistedVerdict(true))
	session, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	waitForStop(t, control, session.ID)
	if got := scanner.scanCount(); got != 0 {
		t.Fatalf("scans = %d, want 0 for a verdict that was already reached", got)
	}
}

// A persisted copy-safe verdict is the common resolved state and must stay
// silent: it is exactly the evidence that the route the session is on is fine.
func TestCopySafetyRaceIgnoresPersistedSafeVerdict(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: false}
	race, sessions, control := raceFixtureForFile(t, scanner, fileWithPersistedVerdict(false))
	if _, err := sessions.StartSession(1, "profile-1", 100, PlayRemux, false); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	race.RaceScan(100)

	time.Sleep(20 * time.Millisecond)
	if stopped := control.stoppedSessions(); len(stopped) != 0 {
		t.Fatalf("stopped = %v, want no session touched by a persisted copy-safe verdict", stopped)
	}
}

// Per-file dedupe bounds the races for one popular file; nothing bounds the
// number of distinct files a burst of watch-page loads names, and each scan
// costs an ffmpeg process against remote storage.
func TestCopySafetyRaceCapsConcurrentScans(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: true, release: make(chan struct{})}
	race, _, _ := raceFixture(t, scanner)

	const files = 12
	for i := 0; i < files; i++ {
		race.RaceScan(200 + i)
	}

	waitForScans(t, scanner, copySafetyScanConcurrency)
	time.Sleep(50 * time.Millisecond)
	if got := scanner.scanCount(); got != copySafetyScanConcurrency {
		t.Fatalf("scans = %d, want %d in flight while every slot is held", got, copySafetyScanConcurrency)
	}

	// Every queued race still runs; the cap defers work rather than dropping it.
	close(scanner.release)
	waitForScans(t, scanner, files)
	if got := scanner.peakConcurrency(); got > copySafetyScanConcurrency {
		t.Fatalf("peak concurrency = %d, want at most %d", got, copySafetyScanConcurrency)
	}
}

// The route test lives with the racer so start and replan cannot disagree: only
// a plan that actually stream-copies video is worth chasing.
func TestCopySafetyRaceForPlanOnlyChasesCopyRoutes(t *testing.T) {
	tests := []struct {
		delivery  DeliveryV3
		wantScans int
	}{
		{delivery: DeliveryRemuxHLSV3, wantScans: 1},
		{delivery: DeliveryRemuxProgressiveV3, wantScans: 1},
		{delivery: DeliveryTranscodeHLSV3, wantScans: 0},
		{delivery: DeliveryOriginalHTTPV3, wantScans: 0},
	}

	for _, tc := range tests {
		t.Run(string(tc.delivery), func(t *testing.T) {
			scanner := &fakeCopySafetyScanner{needs: true}
			race, _, _ := raceFixture(t, scanner)

			race.RaceScanForPlan(100, &PlanV3{PlanID: "plan-1", Delivery: tc.delivery})

			if tc.wantScans > 0 {
				waitForScans(t, scanner, tc.wantScans)
				return
			}
			time.Sleep(20 * time.Millisecond)
			if got := scanner.scanCount(); got != 0 {
				t.Fatalf("scans = %d, want 0 for delivery %q", got, tc.delivery)
			}
		})
	}
}

func TestCopySafetyRaceNilIsSafe(t *testing.T) {
	var race *CopySafetyRace
	race.RaceScan(100)
	race.RaceScanForPlan(100, &PlanV3{Delivery: DeliveryRemuxHLSV3})
	if NewCopySafetyRace(nil, nil, nil) != nil {
		t.Fatal("NewCopySafetyRace() with no dependencies = non-nil, want nil")
	}
}

// waitForLoads waits for the racer to have read the row want times. Each pass
// opens with exactly one read and a failed scan adds its re-read, so the count
// is the observable "this much of the pass has happened".
func waitForLoads(t *testing.T, loader *fakeFileLoader, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if loader.loadCount() >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file loads = %d, want %d", loader.loadCount(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForScans(t *testing.T, scanner *fakeCopySafetyScanner, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if scanner.scanCount() >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("scans = %d, want %d", scanner.scanCount(), want)
		}
		time.Sleep(time.Millisecond)
	}
}
