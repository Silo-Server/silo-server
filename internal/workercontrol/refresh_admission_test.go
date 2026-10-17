//go:build unix

package workercontrol

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
)

func TestExecuteRefreshesSnapshotCapturedBeforeAdmission(t *testing.T) {
	f := newStopFixture(t)
	captured, release, snapshots := captureFirstRefresh(t, f.runtime)
	initial := make(chan error, 1)
	go func() { initial <- f.runtime.refresh(t.Context()) }()
	<-captured
	waiting := make(chan struct{})
	original := f.worker.Config.Handler
	f.worker.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/worker/execute" {
			request = request.WithContext(&refreshWaitContext{Context: request.Context(), waiting: waiting})
		}
		original.ServeHTTP(w, request)
	})
	body, _ := json.Marshal(map[string]any{"session_id": "work", inputPathField: f.source, "source_video_codec": "h264", "target_codec_video": "h264", "target_codec_audio": "aac", "segment_duration": 2, "total_duration": 600})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.worker.URL+"/transcode/start", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := nodeauth.Sign(request, f.worker.URL, "legacy-test-secret"); err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		body   string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		response, err := nodehttp.Client(&http.Client{}).Do(request)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer func() { _ = response.Body.Close() }()
		raw, _ := io.ReadAll(response.Body)
		done <- result{status: response.StatusCode, body: string(raw)}
	}()
	awaitScopeWaiter(t, waiting)
	release()
	if err := awaitRefresh(t, initial); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.status != http.StatusAccepted {
			t.Fatalf("valid start joined pre-admission snapshot: status=%d body=%s err=%v snapshots=%d", result.status, result.body, result.err, snapshots.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("start did not finish after refreshing admitted scope")
	}
}
