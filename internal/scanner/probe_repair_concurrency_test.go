package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestLegacyDolbyVisionProbeRemainsRepairableAfterRollingWriter(t *testing.T) {
	probedAt := time.Now()
	base := models.MediaFile{
		ProbeSource: "local", ProbeUpdatedAt: &probedAt, Duration: 100, Container: "mkv",
		CodecVideo: "hevc", Resolution: "2160p", Chapters: []models.MediaChapter{},
	}
	if err := json.Unmarshal([]byte(`[{"codec":"hevc","dv_profile":8,"color_range":"tv"}]`), &base.VideoTracks); err != nil {
		t.Fatal(err)
	}
	if !NeedsCriticalProbeRepair(&base) {
		t.Fatal("legacy writer output with missing DV provenance keys was accepted as current")
	}

	if err := json.Unmarshal([]byte(`[{"codec":"hevc","dv_profile":8,"dv_config_present":false,"dv_bl_compat_id_present":false,"color_range":"tv"}]`), &base.VideoTracks); err != nil {
		t.Fatal(err)
	}
	if NeedsCriticalProbeRepair(&base) {
		t.Fatal("current probe with explicit false DV provenance was treated as legacy")
	}
}

func TestVideoTrackSerializationKeepsFalseDolbyVisionProvenance(t *testing.T) {
	data, err := json.Marshal(models.VideoTrack{Codec: "hevc", DVProfile: 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dv_config_present", "dv_bl_compat_id_present"} {
		if !strings.Contains(string(data), `"`+key+`":false`) {
			t.Fatalf("serialized track omitted %s: %s", key, data)
		}
	}
}

type probeRepairTestRepository struct {
	mu          sync.Mutex
	files       map[int]*models.MediaFile
	upsertCalls int
}

func (r *probeRepairTestRepository) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	file, ok := r.files[id]
	if !ok {
		return nil, errors.New("media file not found")
	}
	copy := *file
	return &copy, nil
}

func (r *probeRepairTestRepository) Upsert(_ context.Context, file models.MediaFile) (*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upsertCalls++
	copy := file
	if r.files == nil {
		r.files = make(map[int]*models.MediaFile)
	}
	r.files[copy.ID] = &copy
	result := copy
	return &result, nil
}

func (r *probeRepairTestRepository) upserts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.upsertCalls
}

func TestPlaybackProbeEnsurerCoalescesRepairWithoutLeaderCancellationPoison(t *testing.T) {
	modified := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	stale := &models.MediaFile{
		ID:             42,
		FilePath:       "/library/movie.mkv",
		FileSize:       1_000_000,
		FileModifiedAt: &modified,
		FileHash:       "source-hash",
	}
	repo := &probeRepairTestRepository{files: map[int]*models.MediaFile{stale.ID: stale}}
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var probeCalls atomic.Int32
	ensurer := &PlaybackProbeEnsurer{
		fileRepo:    repo,
		ffprobePath: "ffprobe",
		timeout:     time.Second,
		probeFile: func(ctx context.Context, _, _ string) (*ProbeData, error) {
			if probeCalls.Add(1) == 1 {
				close(probeStarted)
			}
			select {
			case <-releaseProbe:
				return completeProbeRepairTestData(), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := ensurer.Ensure(leaderCtx, stale)
		leaderResult <- err
	}()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("leader did not start probe repair")
	}

	// An already-canceled caller must join the existing operation without
	// starting another FFprobe process.
	canceledCtx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	if _, err := ensurer.Ensure(canceledCtx, stale); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
	if got := probeCalls.Load(); got != 1 {
		t.Fatalf("probe calls with identical revision = %d, want 1", got)
	}

	cancelLeader()
	if err := <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", err)
	}

	followerResult := make(chan struct {
		file *models.MediaFile
		err  error
	}, 1)
	go func() {
		file, err := ensurer.Ensure(context.Background(), stale)
		followerResult <- struct {
			file *models.MediaFile
			err  error
		}{file: file, err: err}
	}()
	close(releaseProbe)

	result := <-followerResult
	if result.err != nil {
		t.Fatalf("follower error = %v", result.err)
	}
	if NeedsCriticalProbeRepair(result.file) {
		t.Fatal("follower received stale probe metadata")
	}
	if got := probeCalls.Load(); got != 1 {
		t.Fatalf("probe calls after leader cancellation = %d, want 1", got)
	}
	if got := repo.upserts(); got != 1 {
		t.Fatalf("upsert calls = %d, want 1", got)
	}
}

func TestPlaybackProbeEnsurerRefetchesBeforeRepairingStaleCaller(t *testing.T) {
	modified := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	stale := &models.MediaFile{
		ID:             42,
		FilePath:       "/library/movie.mkv",
		FileSize:       1_000_000,
		FileModifiedAt: &modified,
		FileHash:       "source-hash",
	}
	probedAt := modified.Add(time.Minute)
	repaired := &models.MediaFile{
		ID:             stale.ID,
		FilePath:       stale.FilePath,
		FileSize:       stale.FileSize,
		FileModifiedAt: stale.FileModifiedAt,
		FileHash:       stale.FileHash,
		ProbeSource:    "local",
		ProbeUpdatedAt: &probedAt,
		CodecVideo:     "hevc",
		CodecAudio:     "aac",
		Resolution:     "1080p",
		Container:      "mkv",
		Duration:       7_200,
		VideoTracks:    []models.VideoTrack{{Codec: "hevc", ColorRange: "tv"}},
		AudioTracks:    []models.AudioTrack{{Codec: "aac"}},
		Chapters:       []models.MediaChapter{},
	}
	repo := &probeRepairTestRepository{files: map[int]*models.MediaFile{repaired.ID: repaired}}
	var probeCalls atomic.Int32
	ensurer := &PlaybackProbeEnsurer{
		fileRepo:    repo,
		ffprobePath: "ffprobe",
		probeFile: func(context.Context, string, string) (*ProbeData, error) {
			probeCalls.Add(1)
			return nil, errors.New("stale caller launched an unnecessary probe")
		},
	}

	got, err := ensurer.Ensure(context.Background(), stale)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got.ProbeUpdatedAt == nil || !got.ProbeUpdatedAt.Equal(probedAt) {
		t.Fatalf("probe updated at = %v, want %v", got.ProbeUpdatedAt, probedAt)
	}
	if calls := probeCalls.Load(); calls != 0 {
		t.Fatalf("probe calls = %d, want 0", calls)
	}
	if calls := repo.upserts(); calls != 0 {
		t.Fatalf("upsert calls = %d, want 0", calls)
	}
}

func TestPlaybackProbeEnsurerDoesNotCoalesceDifferentSourceRevisions(t *testing.T) {
	modified := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	first := &models.MediaFile{
		ID:             42,
		FilePath:       "/library/movie.mkv",
		FileSize:       1_000_000,
		FileModifiedAt: &modified,
		FileHash:       "first-source-hash",
	}
	secondModified := modified.Add(time.Minute)
	second := *first
	second.FileSize = 2_000_000
	second.FileModifiedAt = &secondModified
	second.FileHash = "second-source-hash"

	repo := &probeRepairTestRepository{files: map[int]*models.MediaFile{first.ID: first}}
	probeStarted := make(chan struct{}, 2)
	releaseProbes := make(chan struct{})
	var probeCalls atomic.Int32
	ensurer := &PlaybackProbeEnsurer{
		fileRepo:    repo,
		ffprobePath: "ffprobe",
		timeout:     time.Second,
		probeFile: func(ctx context.Context, _, _ string) (*ProbeData, error) {
			probeCalls.Add(1)
			probeStarted <- struct{}{}
			select {
			case <-releaseProbes:
				return completeProbeRepairTestData(), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	results := make(chan error, 2)
	go func() {
		_, err := ensurer.Ensure(context.Background(), first)
		results <- err
	}()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("first source revision did not start probe")
	}

	go func() {
		_, err := ensurer.Ensure(context.Background(), &second)
		results <- err
	}()
	select {
	case <-probeStarted:
		close(releaseProbes)
	case <-time.After(100 * time.Millisecond):
		close(releaseProbes)
		for range 2 {
			<-results
		}
		t.Fatal("different source revision incorrectly joined the in-flight probe")
	}

	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("Ensure() error = %v", err)
		}
	}
	if calls := probeCalls.Load(); calls != 2 {
		t.Fatalf("probe calls = %d, want 2 for different source revisions", calls)
	}
}

func TestPlaybackProbeEnsurerBoundsDistinctRepairs(t *testing.T) {
	modified := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
	first := &models.MediaFile{ID: 42, FilePath: "/library/first.mkv", FileSize: 1_000_000, FileModifiedAt: &modified, FileHash: "first"}
	second := *first
	second.ID = 43
	second.FilePath = "/library/second.mkv"
	repo := &probeRepairTestRepository{files: map[int]*models.MediaFile{first.ID: first, second.ID: &second}}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	ensurer := &PlaybackProbeEnsurer{
		fileRepo: repo, ffprobePath: "ffprobe", timeout: time.Second, probeSlots: make(chan struct{}, 1),
		probeFile: func(ctx context.Context, _, _ string) (*ProbeData, error) {
			started <- struct{}{}
			select {
			case <-release:
				return completeProbeRepairTestData(), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	results := make(chan error, 2)
	for _, file := range []*models.MediaFile{first, &second} {
		go func(file *models.MediaFile) {
			_, err := ensurer.Ensure(context.Background(), file)
			results <- err
		}(file)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first repair did not start")
	}
	select {
	case <-started:
		close(release)
		t.Fatal("distinct repair bypassed the process limit")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("Ensure() error = %v", err)
		}
	}
}

func completeProbeRepairTestData() *ProbeData {
	return &ProbeData{
		CodecVideo:  "hevc",
		CodecAudio:  "aac",
		Resolution:  "1080p",
		Container:   "mkv",
		Duration:    7_200,
		VideoTracks: []VideoTrackInfo{{Codec: "hevc", ColorRange: "tv"}},
		AudioTracks: []AudioTrackInfo{{Codec: "aac"}},
		Chapters:    []ChapterInfo{},
	}
}
