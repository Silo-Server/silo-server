package playback

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeCopySafetyScanner struct {
	mu        sync.Mutex
	needs     bool
	scans     int
	active    int
	maxActive int
	release   chan struct{}
	scanning  chan struct{}
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
	return false, false, nil
}

func (s *fakeCopySafetyScanner) KnownCopySafetyVerdict(context.Context, *models.MediaFile) (bool, bool) {
	return false, false
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

type fakeFileLoader struct{ file *models.MediaFile }

func (l *fakeFileLoader) GetByID(context.Context, int) (*models.MediaFile, error) {
	return l.file, nil
}

func raceFixture(scanner *fakeCopySafetyScanner) *CopySafetyRace {
	return NewCopySafetyRace(scanner, &fakeFileLoader{file: &models.MediaFile{
		ID:          100,
		CodecVideo:  "h264",
		VideoTracks: []models.VideoTrack{{Codec: "h264"}},
	}})
}

// Every start, replan and watch-page load for a popular file asks for the same
// race; a burst arriving while the scan runs is answered by that scan.
func TestCopySafetyRaceDedupesInFlightScans(t *testing.T) {
	scanner := &fakeCopySafetyScanner{
		needs:    true,
		release:  make(chan struct{}),
		scanning: make(chan struct{}, 8),
	}
	race := raceFixture(scanner)

	race.RaceScan(100)
	<-scanner.scanning
	for i := 0; i < 5; i++ {
		race.RaceScan(100)
	}
	close(scanner.release)
	time.Sleep(20 * time.Millisecond)
	if got := scanner.scanCount(); got != 1 {
		t.Fatalf("scans = %d, want 1 for the whole burst", got)
	}
}

// Per-file dedupe bounds the races for one popular file; nothing bounds the
// number of distinct files a burst of watch-page loads names, and each scan
// costs an ffmpeg process against remote storage.
func TestCopySafetyRaceCapsConcurrentScans(t *testing.T) {
	scanner := &fakeCopySafetyScanner{needs: true, release: make(chan struct{})}
	race := raceFixture(scanner)

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
			race := raceFixture(scanner)

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
	if NewCopySafetyRace(nil, nil) != nil {
		t.Fatal("NewCopySafetyRace() with no dependencies = non-nil, want nil")
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
