package chapterthumbs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/encodinglimit"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

func TestChapterThumbnailRemoteWorkerFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the FFmpeg fixture uses a POSIX shell")
	}
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		name          string
		mode          string
		atCapacity    bool
		invalidMedia  bool
		wantStatus    int
		wantReason    string
		wantLocal     bool
		wantThumbnail bool
	}{
		{
			name: "preferred worker capacity permits local fallback", mode: "prefer_transcode_nodes", atCapacity: true,
			wantStatus: http.StatusServiceUnavailable, wantReason: "transcode_node_capacity_exhausted", wantLocal: true, wantThumbnail: true,
		},
		{
			name: "remote only preserves worker capacity failure", mode: "transcode_nodes_only", atCapacity: true,
			wantStatus: http.StatusServiceUnavailable, wantReason: "transcode_node_capacity_exhausted",
		},
		{
			name: "invalid media does not fall back locally", mode: "prefer_transcode_nodes", invalidMedia: true,
			wantStatus: http.StatusUnprocessableEntity, wantReason: "decode_invalid_data",
		},
		{
			name: "successful worker does not invoke local extraction", mode: "prefer_transcode_nodes",
			wantStatus: http.StatusOK, wantThumbnail: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var jpegData bytes.Buffer
			frame := image.NewRGBA(image.Rect(0, 0, 8, 8))
			for y := 0; y < 8; y++ {
				for x := 0; x < 8; x++ {
					frame.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 30), B: 64, A: 255})
				}
			}
			if err := jpeg.Encode(&jpegData, frame, nil); err != nil {
				t.Fatal(err)
			}
			localFFmpeg := writeChapterFFmpegFixture(t, jpegData.Bytes(), false)
			workerFFmpeg := writeChapterFFmpegFixture(t, jpegData.Bytes(), tc.invalidMedia)

			cfg := &config.Config{}
			cfg.Auth.JWTSecret = "chapter-worker-test-secret"
			cfg.Playback.FFmpegPath = workerFFmpeg
			cfg.Playback.HWAccel = "none"
			cfg.Playback.TranscodeDir = t.TempDir()
			watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
			watcher.SetConfigForTest(cfg)
			worker := transcodenode.NewServer(watcher, nil)
			worker.SetInputPathAuthorizer(chapterInputPaths{})
			handler := worker.Handler()

			encodinglimit.Configure(0)
			t.Cleanup(func() { encodinglimit.Configure(0) })
			release := func() {}
			if tc.atCapacity {
				encodinglimit.Configure(1)
				var err error
				release, err = encodinglimit.Acquire()
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}

			responses := make(chan *httptest.ResponseRecorder, 2)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				// Production workers and the API host have separate process-local
				// limiters. Release the simulated worker's occupied slot before
				// forwarding its actual response so local extraction can run here.
				release()
				responses <- response
				for name, values := range response.Header() {
					w.Header()[name] = values
				}
				w.WriteHeader(response.Code)
				_, _ = w.Write(response.Body.Bytes())
			}))
			defer remote.Close()
			pool := nodepool.NewTranscodePool()
			// A healthy inventory can be stale by the time the worker admits
			// the request; the refusal must come from the real worker endpoint.
			pool.SetNodes([]*nodepool.Node{{URL: remote.URL, Enabled: true, Healthy: true}})
			repo := &chapterFileRepo{file: &models.MediaFile{
				ID: 42, MediaFolderID: 9, FilePath: "/media/movie.mkv",
				Chapters: []models.MediaChapter{{Index: 0, StartSeconds: 0, EndSeconds: 20}},
			}}
			store, err := blobstore.NewFilesystem(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			service := chapterthumbs.NewService(repo, chapterFolderRepo{}, nil, chapterSettings{
				"playback.chapter_thumbnail_execution": tc.mode,
				"auth.jwt_secret":                      cfg.Auth.JWTSecret,
				"playback.hw_accel":                    "none",
			}, store, nil, pool, localFFmpeg, "none", "", 1)
			if _, err := service.BackfillMissing(t.Context(), 1); err != nil {
				t.Fatal(err)
			}

			select {
			case response := <-responses:
				if response.Code != tc.wantStatus {
					t.Errorf("worker status = %d, want %d: %s", response.Code, tc.wantStatus, response.Body.String())
				}
				if tc.wantReason != "" {
					var failure chapterthumbs.RemoteExtractErrorResponse
					if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
						t.Fatal(err)
					}
					if failure.Reason != tc.wantReason {
						t.Errorf("worker reason = %q, want %q", failure.Reason, tc.wantReason)
					}
				}
			default:
				t.Fatal("service did not contact the worker")
			}
			select {
			case <-responses:
				t.Error("service unexpectedly retried the worker")
			default:
			}
			localCalls, err := os.ReadFile(localFFmpeg + ".ran")
			if tc.wantLocal && (err != nil || string(localCalls) != "invoked") {
				t.Errorf("local extraction invocations = %q, want one: %v", localCalls, err)
			}
			if !tc.wantLocal && !os.IsNotExist(err) {
				t.Errorf("local extraction unexpectedly ran: %v", err)
			}
			workerCalls, err := os.ReadFile(workerFFmpeg + ".ran")
			if tc.atCapacity && !os.IsNotExist(err) {
				t.Errorf("full worker invoked FFmpeg: %v", err)
			}
			if !tc.atCapacity && (err != nil || string(workerCalls) != "invoked") {
				t.Errorf("worker extraction invocations = %q, want one: %v", workerCalls, err)
			}
			chapter := repo.file.Chapters[0]
			if tc.wantThumbnail {
				if chapter.ThumbnailPath == "" || chapter.ThumbnailThumbhash == "" || chapter.ThumbnailLastError != "" {
					t.Fatalf("thumbnail was not saved successfully: %#v", chapter)
				}
				stored, _, err := store.Get(t.Context(), chapter.ThumbnailPath)
				if err != nil {
					t.Fatal(err)
				}
				defer stored.Close()
				if data, err := io.ReadAll(stored); err != nil || len(data) == 0 {
					t.Fatalf("saved thumbnail is empty: %v", err)
				}
			} else if chapter.ThumbnailPath != "" || !strings.Contains(chapter.ThumbnailLastError, tc.wantReason) {
				t.Fatalf("remote failure was not preserved: %#v", chapter)
			}
			if got := encodinglimit.Snapshot().ActiveJobs; got != 0 {
				t.Fatalf("extraction left %d encoding reservations", got)
			}
		})
	}
}

func writeChapterFFmpegFixture(t *testing.T, frame []byte, invalidMedia bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nprintf invoked >> \"$0.ran\"\n"
	if invalidMedia {
		script += "printf 'Invalid data found when processing input' >&2\nexit 1\n"
	} else {
		var octal strings.Builder
		for _, b := range frame {
			fmt.Fprintf(&octal, "\\%03o", b)
		}
		script += "printf '" + octal.String() + "'\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type chapterInputPaths struct{}

func (chapterInputPaths) Allowed(context.Context, string) (bool, error) { return true, nil }

type chapterSettings map[string]string

func (s chapterSettings) Get(_ context.Context, key string) (string, error) { return s[key], nil }

type chapterFolderRepo struct{}

func (chapterFolderRepo) GetByID(context.Context, int) (*models.MediaFolder, error) {
	return &models.MediaFolder{ID: 9, Enabled: true, ChapterThumbnailsEnabled: true}, nil
}

type chapterFileRepo struct {
	chapterthumbs.FileRepository
	file *models.MediaFile
}

func (r *chapterFileRepo) TryLockChapterThumbnails(ctx context.Context, _ int) (context.Context, func(), bool, error) {
	return ctx, func() {}, true, nil
}

func (r *chapterFileRepo) GetByID(context.Context, int) (*models.MediaFile, error) {
	file := *r.file
	file.Chapters = append([]models.MediaChapter(nil), r.file.Chapters...)
	return &file, nil
}

func (r *chapterFileRepo) ListMissingChapterThumbnails(context.Context, int, string) ([]*models.MediaFile, error) {
	return []*models.MediaFile{r.file}, nil
}

func (r *chapterFileRepo) UpdateChapterThumbnailState(_ context.Context, _ int, chapters []models.MediaChapter, failure *scanner.ChapterThumbnailFailureState) (*models.MediaFile, error) {
	r.file.Chapters = append([]models.MediaChapter(nil), chapters...)
	if failure != nil && failure.Apply {
		r.file.ChapterThumbnailFailureCount = failure.FailureCount
		r.file.ChapterThumbnailRetryAfter = failure.RetryAfter
		r.file.ChapterThumbnailLastError = failure.LastError
	}
	return r.GetByID(context.Background(), r.file.ID)
}

func (r *chapterFileRepo) SetChapterThumbnailFailure(_ context.Context, _ int, retryAfter time.Time, count int, lastError string) error {
	r.file.ChapterThumbnailRetryAfter = &retryAfter
	r.file.ChapterThumbnailFailureCount = count
	r.file.ChapterThumbnailLastError = lastError
	return nil
}
