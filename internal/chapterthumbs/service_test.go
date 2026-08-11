package chapterthumbs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

func TestChapterCaptureTime(t *testing.T) {
	tests := []struct {
		name    string
		chapter models.MediaChapter
		want    float64
	}{
		{
			name:    "uses quarter of short chapter",
			chapter: models.MediaChapter{StartSeconds: 10, EndSeconds: 18},
			want:    12,
		},
		{
			name:    "uses five second offset for long chapter",
			chapter: models.MediaChapter{StartSeconds: 30, EndSeconds: 90},
			want:    35,
		},
		{
			name:    "uses quarter offset for tiny chapter",
			chapter: models.MediaChapter{StartSeconds: 50, EndSeconds: 50.05},
			want:    50.0125,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chapterCaptureTime(tt.chapter); got != tt.want {
				t.Fatalf("chapterCaptureTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildFrameExtractArgs(t *testing.T) {
	t.Run("qsv uses hardware flags when render device exists", func(t *testing.T) {
		args, err := buildFrameExtractArgs("/media/movie.mkv", 42.5, "qsv", "/dev/dri/renderD128", false)
		if err != nil {
			t.Fatalf("buildFrameExtractArgs() error = %v", err)
		}
		if !slices.Contains(args, "-init_hw_device") || !slices.Contains(args, "qsv=qs@va") {
			t.Fatalf("qsv args missing hardware setup: %#v", args)
		}
	})

	t.Run("vaapi uses hardware flags when render device exists", func(t *testing.T) {
		args, err := buildFrameExtractArgs("/media/movie.mkv", 42.5, "vaapi", "/dev/dri/renderD128", false)
		if err != nil {
			t.Fatalf("buildFrameExtractArgs() error = %v", err)
		}
		if !slices.Contains(args, "-hwaccel") || !slices.Contains(args, "vaapi") {
			t.Fatalf("vaapi args missing hardware setup: %#v", args)
		}
	})

	t.Run("unsupported hw accel does not masquerade as hardware extraction", func(t *testing.T) {
		_, err := buildFrameExtractArgs("/media/movie.mkv", 42.5, "nvenc", "/dev/dri/renderD128", false)
		if err == nil || !strings.Contains(err.Error(), "does not support") {
			t.Fatalf("buildFrameExtractArgs() error = %v, want unsupported accelerator error", err)
		}
	})
}

func TestQueueFileIDsDedupes(t *testing.T) {
	service := &Service{
		notifyNormal:   make(chan struct{}, 8),
		notifyPriority: make(chan struct{}, 8),
		queuedPriority: make(map[int]ChapterThumbnailRequest),
		queuedNormal:   make(map[int]ChapterThumbnailRequest),
		inProgress:     make(map[int]struct{}),
	}
	service.inProgress[9] = struct{}{}

	service.QueueFileIDs(context.Background(), []int{7, 7, 8, 9, 0, -1})

	if len(service.queuedNormal) != 2 {
		t.Fatalf("len(queuedNormal) = %d, want 2", len(service.queuedNormal))
	}
	if _, ok := service.queuedNormal[7]; !ok {
		t.Fatalf("file 7 was not queued")
	}
	if _, ok := service.queuedNormal[8]; !ok {
		t.Fatalf("file 8 was not queued")
	}
	if len(service.normalQueue) != 2 {
		t.Fatalf("len(normalQueue) = %d, want 2", len(service.normalQueue))
	}
}

func TestQueuePriorityPromotesExistingFile(t *testing.T) {
	service := &Service{
		notifyNormal:   make(chan struct{}, 8),
		notifyPriority: make(chan struct{}, 8),
		queuedPriority: make(map[int]ChapterThumbnailRequest),
		queuedNormal:   make(map[int]ChapterThumbnailRequest),
		inProgress:     make(map[int]struct{}),
	}

	service.QueueFileIDs(context.Background(), []int{7, 8})
	service.QueuePriorityFileAtPosition(context.Background(), 8, 123)

	if _, ok := service.queuedPriority[8]; !ok {
		t.Fatalf("file 8 was not promoted to priority")
	}
	if _, ok := service.queuedNormal[8]; ok {
		t.Fatalf("file 8 still present in normal queue")
	}
	req, ok := service.nextRequest(context.Background(), false)
	if !ok || req.FileID != 8 {
		t.Fatalf("nextRequest() = %#v, %v, want file 8", req, ok)
	}
	if req.TargetSeconds == nil || *req.TargetSeconds != 123 {
		t.Fatalf("targetSeconds = %v, want 123", req.TargetSeconds)
	}
}

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

func (r *testFileRepo) ListMissingChapterThumbnails(context.Context, int) ([]*models.MediaFile, error) {
	return nil, nil
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

func (e testProbeEnsurer) Ensure(context.Context, *models.MediaFile) (*models.MediaFile, error) {
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
	_ string,
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
			return "chapter-images/42/original.webp", "thumbhash", nil
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

func TestProcessRequestSetsProbeFailureCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	fileRepo := &testFileRepo{
		file: &models.MediaFile{
			ID:                           42,
			MediaFolderID:                9,
			FilePath:                     "/media/movie.mkv",
			ChapterThumbnailFailureCount: 1,
		},
	}
	service := &Service{
		fileRepo:     fileRepo,
		folderRepo:   &testFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true, ChapterThumbnailsEnabled: true}},
		probeEnsurer: testProbeEnsurer{err: context.DeadlineExceeded},
		clock: func() time.Time {
			return now
		},
	}

	_, err := service.processRequest(context.Background(), ChapterThumbnailRequest{FileID: 42}, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("processRequest() error = %v, want context deadline exceeded", err)
	}
	if len(fileRepo.failureUpdates) != 1 {
		t.Fatalf("len(failureUpdates) = %d, want 1", len(fileRepo.failureUpdates))
	}
	update := fileRepo.failureUpdates[0]
	if update.failureCount != 2 {
		t.Fatalf("failureCount = %d, want 2", update.failureCount)
	}
	if got, want := update.retryAfter, now.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("retryAfter = %v, want %v", got, want)
	}
	if update.lastError == "" || !strings.HasPrefix(update.lastError, "probe_timeout:") {
		t.Fatalf("lastError = %q, want probe_timeout prefix", update.lastError)
	}
}

func TestProcessRequestSkipsFileDuringCooldown(t *testing.T) {
	retryAfter := time.Now().UTC().Add(time.Hour)
	fileRepo := &testFileRepo{
		file: &models.MediaFile{
			ID:                         42,
			MediaFolderID:              9,
			FilePath:                   "/media/movie.mkv",
			ChapterThumbnailRetryAfter: &retryAfter,
			Chapters: []models.MediaChapter{
				{Index: 0, StartSeconds: 0, EndSeconds: 10},
			},
		},
	}
	called := false
	service := &Service{
		fileRepo:   fileRepo,
		folderRepo: &testFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true, ChapterThumbnailsEnabled: true}},
		extractFrameFunc: func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
			called = true
			return nil, "", nil
		},
	}

	requeue, err := service.processRequest(context.Background(), ChapterThumbnailRequest{FileID: 42}, false)
	if err != nil {
		t.Fatalf("processRequest() error = %v", err)
	}
	if requeue {
		t.Fatalf("processRequest() requeue = true, want false")
	}
	if called {
		t.Fatalf("expected extractFrameFunc not to be called during cooldown")
	}
}

func TestProcessRequestSkipsHDRWhenPolicyDisabled(t *testing.T) {
	fileRepo := &testFileRepo{
		file: &models.MediaFile{
			ID:            42,
			MediaFolderID: 9,
			FilePath:      "/media/movie.mkv",
			HDR:           true,
			Chapters: []models.MediaChapter{
				{Index: 0, StartSeconds: 0, EndSeconds: 10},
			},
		},
	}
	service := &Service{
		fileRepo:   fileRepo,
		folderRepo: &testFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true, ChapterThumbnailsEnabled: true}},
		settings: testSettingsReader{values: map[string]string{
			chapterThumbnailHDRPolicySetting: chapterThumbnailHDRPolicyDisabled,
		}},
		extractFrameFunc: func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
			t.Fatal("HDR extraction should not run when the policy is disabled")
			return nil, "", nil
		},
	}

	requeue, err := service.processRequest(context.Background(), ChapterThumbnailRequest{FileID: 42}, false)
	if err != nil {
		t.Fatalf("processRequest() error = %v", err)
	}
	if requeue {
		t.Fatal("processRequest() requeue = true, want false")
	}
	if fileRepo.updateCalls != 0 {
		t.Fatalf("updateCalls = %d, want 0", fileRepo.updateCalls)
	}
}

func TestProcessRequestMarksDecodeInvalidDataAsFileFailure(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	fileRepo := &testFileRepo{
		file: &models.MediaFile{
			ID:            42,
			MediaFolderID: 9,
			FilePath:      "/media/movie.mkv",
			Chapters: []models.MediaChapter{
				{Index: 0, StartSeconds: 0, EndSeconds: 10},
				{Index: 1, StartSeconds: 10, EndSeconds: 20},
			},
		},
	}
	callCount := 0
	service := &Service{
		fileRepo:   fileRepo,
		folderRepo: &testFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true, ChapterThumbnailsEnabled: true}},
		clock: func() time.Time {
			return now
		},
		extractFrameFunc: func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
			callCount++
			return nil, "decode_invalid_data", errors.New("Invalid NAL unit size")
		},
	}

	requeue, err := service.processRequest(context.Background(), ChapterThumbnailRequest{FileID: 42}, false)
	if err != nil {
		t.Fatalf("processRequest() error = %v", err)
	}
	if requeue {
		t.Fatalf("processRequest() requeue = true, want false")
	}
	if callCount != 1 {
		t.Fatalf("callCount = %d, want 1", callCount)
	}
	if fileRepo.updateCalls != 1 {
		t.Fatalf("updateCalls = %d, want 1", fileRepo.updateCalls)
	}
	if fileRepo.file.ChapterThumbnailFailureCount != len(chapterThumbnailRetrySchedule) {
		t.Fatalf("failureCount = %d, want %d", fileRepo.file.ChapterThumbnailFailureCount, len(chapterThumbnailRetrySchedule))
	}
	if fileRepo.file.ChapterThumbnailRetryAfter == nil {
		t.Fatalf("expected file-level retry_after to be set")
	}
	if got, want := *fileRepo.file.ChapterThumbnailRetryAfter, now.Add(24*time.Hour); !got.Equal(want) {
		t.Fatalf("retryAfter = %v, want %v", got, want)
	}
	if !strings.HasPrefix(fileRepo.file.ChapterThumbnailLastError, "decode_invalid_data:") {
		t.Fatalf("lastError = %q, want decode_invalid_data prefix", fileRepo.file.ChapterThumbnailLastError)
	}
	if fileRepo.file.Chapters[0].ThumbnailRetryAfter == nil {
		t.Fatalf("expected chapter-level retry_after to be set")
	}
	if fileRepo.file.Chapters[1].ThumbnailRetryAfter != nil {
		t.Fatalf("expected later chapters to be skipped after file-level failure")
	}
}

func TestProcessRequestReportsToneMapCapabilityFailuresOnceWithNormalRetry(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		err    error
	}{
		{
			name:   "unsupported filters",
			reason: reasonToneMapUnsupported,
			err:    errors.New("configured FFmpeg lacks the required zscale filter"),
		},
		{
			name:   "transient probe failure",
			reason: reasonFFmpegProbeFailed,
			err:    errors.New("FFmpeg filter probe failed: resource temporarily unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0).UTC()
			fileRepo := &testFileRepo{
				file: &models.MediaFile{
					ID:            42,
					MediaFolderID: 9,
					FilePath:      "/media/movie.mkv",
					HDR:           true,
					Chapters: []models.MediaChapter{
						{Index: 0, StartSeconds: 0, EndSeconds: 10},
						{Index: 1, StartSeconds: 10, EndSeconds: 20},
					},
				},
			}
			callCount := 0
			service := &Service{
				fileRepo:   fileRepo,
				folderRepo: &testFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true, ChapterThumbnailsEnabled: true}},
				clock: func() time.Time {
					return now
				},
				extractFrameFunc: func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
					callCount++
					return nil, tt.reason, tt.err
				},
			}

			requeue, err := service.processRequest(context.Background(), ChapterThumbnailRequest{FileID: 42}, false)
			if err != nil {
				t.Fatalf("processRequest() error = %v", err)
			}
			if requeue {
				t.Fatal("processRequest() requeue = true, want false")
			}
			if callCount != 1 {
				t.Fatalf("callCount = %d, want 1", callCount)
			}
			if fileRepo.updateCalls != 1 {
				t.Fatalf("updateCalls = %d, want 1", fileRepo.updateCalls)
			}
			if fileRepo.file.ChapterThumbnailFailureCount != 1 {
				t.Fatalf("failureCount = %d, want 1", fileRepo.file.ChapterThumbnailFailureCount)
			}
			if fileRepo.file.ChapterThumbnailRetryAfter == nil {
				t.Fatal("expected file-level retry_after to be set")
			}
			if got, want := *fileRepo.file.ChapterThumbnailRetryAfter, now.Add(15*time.Minute); !got.Equal(want) {
				t.Fatalf("retryAfter = %v, want %v", got, want)
			}
			if !strings.HasPrefix(fileRepo.file.ChapterThumbnailLastError, tt.reason+":") {
				t.Fatalf("lastError = %q, want %s prefix", fileRepo.file.ChapterThumbnailLastError, tt.reason)
			}
			if fileRepo.file.Chapters[0].ThumbnailRetryAfter == nil {
				t.Fatal("expected first chapter retry_after to be set")
			}
			if fileRepo.file.Chapters[1].ThumbnailRetryAfter != nil {
				t.Fatal("expected later chapters to be skipped after file-level failure")
			}
		})
	}
}

func TestExtractFrameCPUFallbackGetsFreshDeadline(t *testing.T) {
	service := &Service{
		hwAccel:  "vaapi",
		hwDevice: "/dev/dri/renderD128",
	}
	file := &models.MediaFile{FilePath: "/media/movie.mkv"}

	callCount := 0
	var hwRemaining time.Duration
	var cpuRemaining time.Duration
	service.runFFmpegFrameExtractFunc = func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		callCount++
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("expected deadline on attempt %d", callCount)
		}
		remaining := time.Until(deadline)
		if callCount == 1 {
			hwRemaining = remaining
			return nil, errors.New("signal: killed")
		}
		cpuRemaining = remaining
		return []byte("frame"), nil
	}

	data, reason, err := service.extractFrame(context.Background(), file, 5, chapterThumbnailHDRPolicyBestEffort)
	if err != nil {
		t.Fatalf("extractFrame() error = %v", err)
	}
	if reason != "" {
		t.Fatalf("extractFrame() reason = %q, want empty", reason)
	}
	if string(data) != "frame" {
		t.Fatalf("extractFrame() data = %q, want frame", string(data))
	}
	if callCount != 2 {
		t.Fatalf("callCount = %d, want 2", callCount)
	}
	if hwRemaining < 7*time.Second || hwRemaining > 9*time.Second {
		t.Fatalf("hw deadline = %s, want about 8s", hwRemaining)
	}
	if cpuRemaining < 9*time.Second || cpuRemaining > 11*time.Second {
		t.Fatalf("cpu deadline = %s, want about 10s", cpuRemaining)
	}
}

func TestExtractFramePrefersRemoteNodeWhenEnabled(t *testing.T) {
	remote := &testRemoteFrameExtractor{data: []byte("remote-frame")}
	service := &Service{
		settings: testSettingsReader{values: map[string]string{
			chapterThumbnailExecutionSetting: chapterThumbnailExecutionPreferTranscode,
			authJWTSecretSetting:             "secret",
		}},
		transcodePool:      &nodepool.TranscodePool{},
		remoteReservations: make(map[string]int),
		remoteExtractor:    remote,
	}
	service.transcodePool.SetNodes([]*nodepool.Node{{
		URL:        "http://node-1",
		Enabled:    true,
		Healthy:    true,
		ActiveJobs: 1,
	}})

	data, reason, err := service.extractFrame(
		context.Background(),
		&models.MediaFile{FilePath: "/media/movie.mkv"},
		5,
		chapterThumbnailHDRPolicyBestEffort,
	)
	if err != nil {
		t.Fatalf("extractFrame() error = %v", err)
	}
	if reason != "" {
		t.Fatalf("extractFrame() reason = %q, want empty", reason)
	}
	if string(data) != "remote-frame" {
		t.Fatalf("extractFrame() data = %q, want remote-frame", string(data))
	}
	if len(remote.nodes) != 1 || remote.nodes[0] != "http://node-1" {
		t.Fatalf("remote nodes = %#v, want node-1", remote.nodes)
	}
}

func TestExtractFramePropagatesSoftwareToneMapSettingToRemoteNode(t *testing.T) {
	tests := []struct {
		name         string
		settingValue string
		wantAllowed  bool
	}{
		{name: "disabled by default", wantAllowed: false},
		{name: "explicitly enabled", settingValue: "true", wantAllowed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := &testRemoteFrameExtractor{data: []byte("remote-frame")}
			settings := map[string]string{
				chapterThumbnailExecutionSetting: chapterThumbnailExecutionPreferTranscode,
				authJWTSecretSetting:             "secret",
			}
			if tt.settingValue != "" {
				settings[chapterThumbnailSoftwareToneMapSetting] = tt.settingValue
			}
			service := &Service{
				settings:           testSettingsReader{values: settings},
				transcodePool:      &nodepool.TranscodePool{},
				remoteReservations: make(map[string]int),
				remoteExtractor:    remote,
			}
			service.transcodePool.SetNodes([]*nodepool.Node{{
				URL:     "http://node-1",
				Enabled: true,
				Healthy: true,
			}})

			_, _, err := service.extractFrame(
				context.Background(),
				&models.MediaFile{FilePath: "/media/movie.mkv", HDR: true},
				5,
				chapterThumbnailHDRPolicyBestEffort,
			)
			if err != nil {
				t.Fatalf("extractFrame() error = %v", err)
			}
			if len(remote.requests) != 1 {
				t.Fatalf("remote requests = %d, want 1", len(remote.requests))
			}
			request := remote.requests[0]
			if !request.ToneMap {
				t.Fatal("remote request tone_map = false, want true for HDR")
			}
			if request.AllowSoftwareToneMap != tt.wantAllowed {
				t.Fatalf("remote request allow_software_tone_map = %t, want %t", request.AllowSoftwareToneMap, tt.wantAllowed)
			}
		})
	}
}

func TestExtractFrameFallsBackLocalWhenPreferredNodeUnavailable(t *testing.T) {
	service := &Service{
		settings: testSettingsReader{values: map[string]string{
			chapterThumbnailExecutionSetting: chapterThumbnailExecutionPreferTranscode,
			authJWTSecretSetting:             "secret",
		}},
		transcodePool:      &nodepool.TranscodePool{},
		remoteReservations: make(map[string]int),
		remoteExtractor: &testRemoteFrameExtractor{
			reason: chapterThumbnailNodeUnavailableReason,
			err:    errors.New("node unavailable"),
		},
		runFFmpegFrameExtractFunc: func(context.Context, string, []string) ([]byte, error) {
			return []byte("local-frame"), nil
		},
	}
	service.transcodePool.SetNodes([]*nodepool.Node{{
		URL:        "http://node-1",
		Enabled:    true,
		Healthy:    true,
		ActiveJobs: 0,
	}})

	data, reason, err := service.extractFrame(
		context.Background(),
		&models.MediaFile{FilePath: "/media/movie.mkv"},
		5,
		chapterThumbnailHDRPolicyBestEffort,
	)
	if err != nil {
		t.Fatalf("extractFrame() error = %v", err)
	}
	if reason != "" {
		t.Fatalf("extractFrame() reason = %q, want empty", reason)
	}
	if string(data) != "local-frame" {
		t.Fatalf("extractFrame() data = %q, want local-frame", string(data))
	}
}

func TestExtractFrameRequiresRemoteCapacityWhenConfigured(t *testing.T) {
	service := &Service{
		settings: testSettingsReader{values: map[string]string{
			chapterThumbnailExecutionSetting:    chapterThumbnailExecutionTranscodeOnly,
			chapterThumbnailNodeCapacitySetting: "1",
			authJWTSecretSetting:                "secret",
		}},
		transcodePool: &nodepool.TranscodePool{},
		remoteReservations: map[string]int{
			"http://node-1": 1,
		},
		remoteExtractor: &testRemoteFrameExtractor{},
		runFFmpegFrameExtractFunc: func(context.Context, string, []string) ([]byte, error) {
			t.Fatalf("local extractor should not run in transcode_nodes_only mode")
			return nil, nil
		},
	}
	service.transcodePool.SetNodes([]*nodepool.Node{{
		URL:        "http://node-1",
		Enabled:    true,
		Healthy:    true,
		ActiveJobs: 0,
	}})

	_, reason, err := service.extractFrame(
		context.Background(),
		&models.MediaFile{FilePath: "/media/movie.mkv"},
		5,
		chapterThumbnailHDRPolicyBestEffort,
	)
	if err == nil {
		t.Fatalf("extractFrame() error = nil, want capacity failure")
	}
	if reason != chapterThumbnailNodeCapacityExhaustedReason {
		t.Fatalf("extractFrame() reason = %q, want %q", reason, chapterThumbnailNodeCapacityExhaustedReason)
	}
}

func TestReserveRemoteNodeAccountsForReservations(t *testing.T) {
	service := &Service{
		settings: testSettingsReader{values: map[string]string{
			chapterThumbnailNodeCapacitySetting: "2",
			authJWTSecretSetting:                "secret",
		}},
		transcodePool: &nodepool.TranscodePool{},
		remoteReservations: map[string]int{
			"http://node-1": 1,
		},
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

func TestExtractFrameResolvesMultiDeviceListToOneDevice(t *testing.T) {
	service := &Service{
		hwAccel:  "vaapi",
		hwDevice: "/dev/dri/renderD888,/dev/dri/renderD889",
	}
	file := &models.MediaFile{FilePath: "/media/movie.mkv"}

	var hwArgs []string
	service.runFFmpegFrameExtractFunc = func(_ context.Context, _ string, args []string) ([]byte, error) {
		if hwArgs == nil {
			hwArgs = append([]string(nil), args...)
		}
		return []byte("frame"), nil
	}

	if _, _, err := service.extractFrame(context.Background(), file, 5, chapterThumbnailHDRPolicyBestEffort); err != nil {
		t.Fatalf("extractFrame() error = %v", err)
	}

	joined := strings.Join(hwArgs, " ")
	// Neither test device exists, so the balancer deterministically falls back
	// to the first entry — but never passes the raw list to ffmpeg.
	if strings.Contains(joined, "/dev/dri/renderD888,/dev/dri/renderD889") {
		t.Fatalf("ffmpeg args contain the raw device list:\n%s", joined)
	}
	if !strings.Contains(joined, "/dev/dri/renderD888") {
		t.Fatalf("ffmpeg args missing a resolved device:\n%s", joined)
	}
}
