package chapterthumbs

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type testFileRepo struct {
	file           *models.MediaFile
	updateCalls    int
	failureUpdates []struct {
		retryAfter   time.Time
		failureCount int
		lastError    string
	}
}

func (r *testFileRepo) cloneFile() *models.MediaFile {
	if r.file == nil {
		return nil
	}
	cp := *r.file
	cp.Chapters = append([]models.MediaChapter(nil), r.file.Chapters...)
	return &cp
}

func (r *testFileRepo) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	if r.file == nil || r.file.ID != id {
		return nil, nil
	}
	return r.cloneFile(), nil
}

func (r *testFileRepo) TryLockChapterThumbnails(ctx context.Context, _ int) (context.Context, func(), bool, error) {
	return ctx, func() {}, true, nil
}

func (r *testFileRepo) ListMissingChapterThumbnails(context.Context, int, string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (r *testFileRepo) ListChapterThumbnailsAtOtherWidths(context.Context, int, string, int, bool) ([]*models.MediaFile, time.Time, error) {
	return nil, time.Time{}, nil
}

func (r *testFileRepo) ChapterThumbnailLibraryKey(context.Context) (string, error) {
	return "test-libraries", nil
}

func (r *testFileRepo) UpdateChapterThumbnailState(
	_ context.Context,
	fileID int,
	chapters []models.MediaChapter,
	fileFailure *scanner.ChapterThumbnailFailureState,
) (*models.MediaFile, error) {
	if r.file == nil || r.file.ID != fileID {
		return nil, nil
	}

	cp := *r.file
	cp.Chapters = append([]models.MediaChapter(nil), chapters...)
	if fileFailure != nil && fileFailure.Apply {
		cp.ChapterThumbnailRetryAfter = fileFailure.RetryAfter
		cp.ChapterThumbnailFailureCount = fileFailure.FailureCount
		cp.ChapterThumbnailLastError = fileFailure.LastError
	}
	r.file = &cp
	r.updateCalls++
	return r.cloneFile(), nil
}

func (r *testFileRepo) SetChapterThumbnailFailure(
	_ context.Context,
	fileID int,
	retryAfter time.Time,
	failureCount int,
	lastError string,
) error {
	if r.file == nil || r.file.ID != fileID {
		return nil
	}
	r.file.ChapterThumbnailRetryAfter = &retryAfter
	r.file.ChapterThumbnailFailureCount = failureCount
	r.file.ChapterThumbnailLastError = lastError
	r.failureUpdates = append(r.failureUpdates, struct {
		retryAfter   time.Time
		failureCount int
		lastError    string
	}{
		retryAfter:   retryAfter,
		failureCount: failureCount,
		lastError:    lastError,
	})
	return nil
}

type testFolderRepo struct {
	folder *models.MediaFolder
}

func (r *testFolderRepo) GetByID(context.Context, int) (*models.MediaFolder, error) {
	if r.folder == nil {
		return nil, nil
	}
	cp := *r.folder
	return &cp, nil
}

type testProbeEnsurer struct {
	file *models.MediaFile
	err  error
}

func (e testProbeEnsurer) EnsureProbeOnly(context.Context, *models.MediaFile) (*models.MediaFile, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.file == nil {
		return nil, nil
	}
	cp := *e.file
	cp.Chapters = append([]models.MediaChapter(nil), e.file.Chapters...)
	return &cp, nil
}

type testThumbnailNotifier struct {
	events []struct {
		fileID       int
		chapterIndex int
		path         string
		thumbhash    string
	}
}

func (n *testThumbnailNotifier) ChapterThumbnailReady(
	_ context.Context,
	fileID int,
	chapterIndex int,
	thumbnailPath string,
	thumbnailThumbhash string,
) {
	n.events = append(n.events, struct {
		fileID       int
		chapterIndex int
		path         string
		thumbhash    string
	}{
		fileID:       fileID,
		chapterIndex: chapterIndex,
		path:         thumbnailPath,
		thumbhash:    thumbnailThumbhash,
	})
}

type testSettingsReader struct {
	values map[string]string
}

func (r testSettingsReader) Get(_ context.Context, key string) (string, error) {
	return r.values[key], nil
}

type testRemoteFrameExtractor struct {
	data     []byte
	reason   string
	err      error
	nodes    []string
	requests []RemoteExtractRequest
}

func (e *testRemoteFrameExtractor) ExtractFrame(
	_ context.Context,
	node *nodepool.Node,
	_ nodeauth.Keyring,
	req RemoteExtractRequest,
) ([]byte, string, error) {
	if node != nil {
		e.nodes = append(e.nodes, node.URL)
	}
	e.requests = append(e.requests, req)
	return e.data, e.reason, e.err
}

func TestProcessPriorityRequestSelectsNearestChaptersAndRequeuesRemainder(t *testing.T) {
	fileRepo := &testFileRepo{
		file: &models.MediaFile{
			ID:            42,
			MediaFolderID: 9,
			FilePath:      "/media/movie.mkv",
			Chapters: []models.MediaChapter{
				{Index: 0, StartSeconds: 0, EndSeconds: 10},
				{Index: 1, StartSeconds: 10, EndSeconds: 20},
				{Index: 2, StartSeconds: 20, EndSeconds: 30},
				{Index: 3, StartSeconds: 30, EndSeconds: 40},
				{Index: 4, StartSeconds: 40, EndSeconds: 50},
			},
		},
	}
	notifier := &testThumbnailNotifier{}
	var uploaded []int
	service := &Service{
		fileRepo:   fileRepo,
		folderRepo: &testFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true, ChapterThumbnailsEnabled: true}},
		notifier:   notifier,
		clock: func() time.Time {
			return time.Unix(1_700_000_000, 0).UTC()
		},
		extractFrameFunc: func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
			return []byte("frame"), "", nil
		},
		uploadChapterThumbnailFunc: func(_ context.Context, _ int, chapterIndex int, _ []byte) (string, string, error) {
			uploaded = append(uploaded, chapterIndex)
			return "chapter-images/42/2/w300.webp", "thumbhash", nil
		},
	}

	target := 26.0
	requeue, err := service.processRequest(
		context.Background(),
		ChapterThumbnailRequest{FileID: 42, TargetSeconds: &target},
		true,
	)
	if err != nil {
		t.Fatalf("processRequest() error = %v", err)
	}
	if !requeue {
		t.Fatalf("processRequest() requeue = false, want true")
	}
	wantOrder := []int{2, 3, 1}
	if !slices.Equal(uploaded, wantOrder) {
		t.Fatalf("uploaded order = %#v, want %#v", uploaded, wantOrder)
	}
	if got := len(notifier.events); got != 3 {
		t.Fatalf("len(events) = %d, want 3", got)
	}
	if fileRepo.updateCalls != 1 {
		t.Fatalf("updateCalls = %d, want 1", fileRepo.updateCalls)
	}
	if fileRepo.file.Chapters[0].ThumbnailPath != "" || fileRepo.file.Chapters[4].ThumbnailPath != "" {
		t.Fatalf("expected chapters 0 and 4 to remain missing")
	}
}

func TestReserveRemoteNodeAccountsForReservations(t *testing.T) {
	service := &Service{
		settings: testSettingsReader{values: map[string]string{
			chapterThumbnailNodeCapacitySetting: "2",
		}},
		transcodePool: nodepooltest.NewTranscodePool(),
		nodeKeys:      nodeauthtest.NodeKey("secret"),
		remoteReservations: nodepool.NewReservations(map[string]int{
			"http://node-1": 1,
		}),
		remoteExtractor: &testRemoteFrameExtractor{},
	}
	service.transcodePool.SetNodes([]*nodepool.Node{
		{URL: "http://node-1", Enabled: true, Healthy: true, ActiveJobs: 0},
		{URL: "http://node-2", Enabled: true, Healthy: true, ActiveJobs: 0},
	})

	node, release, reason := service.reserveRemoteNode(context.Background())
	defer release()

	if reason != "" {
		t.Fatalf("reserveRemoteNode() reason = %q, want empty", reason)
	}
	if node == nil || node.URL != "http://node-2" {
		t.Fatalf("reserveRemoteNode() node = %#v, want node-2", node)
	}
}

// failingSettingsReader stands in for a settings repo that cannot answer, so
// the fallback path can be told apart from a configured-empty value.
type failingSettingsReader struct{}

func (failingSettingsReader) Get(_ context.Context, _ string) (string, error) {
	return "", errors.New("settings unavailable")
}
