package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsync"
)

type fakeSubtitleSync struct {
	row        subtitles.DownloadedSubtitle
	job        *subsync.Job
	requests   int
	timing     *subtitles.Timing
	revision   int64
	requestErr error
}

func (f *fakeSubtitleSync) SyncAvailable() bool                  { return true }
func (f *fakeSubtitleSync) AutoSyncEnabled(context.Context) bool { return true }
func (f *fakeSubtitleSync) RequestStoredSubtitleSync(context.Context, catalogpkg.AccessFilter, int) (*subsync.Job, error) {
	f.requests++
	return f.job, f.requestErr
}
func (f *fakeSubtitleSync) StoredSubtitleSync(context.Context, catalogpkg.AccessFilter, int) (*subtitles.DownloadedSubtitle, *subsync.Job, error) {
	return &f.row, f.job, nil
}
func (f *fakeSubtitleSync) SubtitleSyncJobs(_ context.Context, ids []int) map[int]*subsync.Job {
	return map[int]*subsync.Job{f.row.ID: f.job}
}
func (f *fakeSubtitleSync) SetStoredSubtitleTiming(_ context.Context, _ catalogpkg.AccessFilter, _ int, revision int64, t subtitles.Timing) (*subtitles.DownloadedSubtitle, error) {
	f.timing, f.revision = &t, revision
	updated := f.row
	updated.Timing = t
	updated.Revision++
	return &updated, nil
}

func TestSubtitleSyncContract(t *testing.T) {
	deps, _ := catalogDeps(t)
	row := subtitles.DownloadedSubtitle{ID: 9, MediaFileID: 42, Provider: "opensubtitles", Language: "en", Format: subtitles.FormatSRT,
		Revision: 3, CreatedAt: fixedTime(), DownloadedBy: new(1), Timing: subtitles.Timing{Scale: 25 / 23.976, OffsetMS: -1200}}
	confidence := 0.83
	finished := fixedTime().Add(time.Minute)
	sync := &fakeSubtitleSync{row: row, job: &subsync.Job{ID: 77, SubtitleID: 9, Trigger: subsync.TriggerAuto, Status: string(subsync.StatusSynced),
		Confidence: &confidence, Result: &subtitles.Timing{Scale: 25 / 23.976, OffsetMS: -1200}, CreatedAt: fixedTime(), FinishedAt: &finished}}
	deps.SubtitleSync = sync
	deps.ViewerSubtitleDelete = &fakeViewerSubtitleDelete{row: row}
	h := newTestHandler(t, deps)
	path := Prefix + "/subtitles/stored/9"

	status := do(t, h, http.MethodGet, Prefix+"/subtitles/sync/status", "", viewerHeaders())
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"auto_sync":true`) {
		t.Fatalf("status %d %s", status.Code, status.Body)
	}

	read := do(t, h, http.MethodGet, path+"/sync", "", viewerHeaders())
	var got struct {
		Subtitle StoredSubtitle `json:"subtitle"`
	}
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &got) != nil {
		t.Fatalf("read %d %s", read.Code, read.Body)
	}
	if got.Subtitle.Timing.OffsetMS != -1200 || got.Subtitle.Sync == nil || got.Subtitle.Sync.ID != "77" ||
		got.Subtitle.Sync.Status != "synced" || got.Subtitle.Sync.Result == nil || *got.Subtitle.Sync.Confidence != confidence {
		t.Fatalf("projection %+v", got.Subtitle)
	}

	started := do(t, h, http.MethodPost, path+"/sync", "", viewerHeaders())
	if started.Code != http.StatusAccepted || sync.requests != 1 || !strings.Contains(started.Body.String(), `"id":"77"`) {
		t.Fatalf("start %d %s", started.Code, started.Body)
	}

	// Timing writes are guarded by the viewer subtitle validator.
	meta := do(t, h, http.MethodGet, path+"/metadata", "", viewerHeaders())
	tag := meta.Header().Get("ETag")
	body := `{"offset_ms":0,"scale":1}`
	requireProblem(t, do(t, h, http.MethodPut, path+"/timing", body, viewerHeaders()), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodPut, path+"/timing", body, with(viewerHeaders(), "If-Match", `"stale"`)), TypePreconditionFailed)
	requireProblem(t, do(t, h, http.MethodPut, path+"/timing", `{"offset_ms":0,"scale":2}`, with(viewerHeaders(), "If-Match", tag)), TypeValidationFailed)
	if sync.timing != nil {
		t.Fatal("refused timing write dispatched")
	}
	reset := do(t, h, http.MethodPut, path+"/timing", body, with(viewerHeaders(), "If-Match", tag))
	if reset.Code != 200 || sync.timing == nil || !sync.timing.IsIdentity() || sync.revision != 3 || reset.Header().Get("ETag") == tag {
		t.Fatalf("reset %d %s", reset.Code, reset.Body)
	}

	deps.SubtitleSync = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path+"/sync", "", viewerHeaders()), TypeDependencyUnavailable)
}
