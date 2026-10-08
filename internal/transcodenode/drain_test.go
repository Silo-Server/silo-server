package transcodenode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

func TestTranscodeDrainListenerCountsTeardownAndRefusesLateExecution(t *testing.T) {
	srv := newTestServer(t)
	srv.drain = workerdrain.New(func(context.Context) (int, string, string, error) { return 7, "durable-fence", "synthetic-realm", nil }, srv.drainActiveJobs, nil)
	srv.gpu.holdWork() // activeJobs has already dropped, but Close is still running.
	listener := httptest.NewServer(srv.Handler())
	defer listener.Close()
	request := func(method, path, body string) (int, workerdrain.Status) {
		t.Helper()
		req, err := http.NewRequest(method, listener.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testSecret)
		resp, err := listener.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var status workerdrain.Status
		if path == "/admin/drain" && resp.StatusCode == http.StatusOK {
			if err = json.NewDecoder(resp.Body).Decode(&status); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, status
	}
	if code, status := request(http.MethodGet, "/admin/drain", ""); code != http.StatusOK || status.Drained || status.ActiveJobs != 1 || status.NativeServerID != "synthetic-realm" {
		t.Fatalf("teardown omitted: %d %+v", code, status)
	}
	if code, _ := request(http.MethodPost, "/transcode/start", `{"session_id":"late","input_path":"/irrelevant"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("late start: %d", code)
	}
	for _, path := range []string{"/remux/late", "/transcode/late/master.m3u8", "/transcode/late/segment/0.ts", "/hw-capabilities"} {
		if code, _ := request(http.MethodGet, path, ""); code != http.StatusServiceUnavailable {
			t.Fatalf("late %s: %d", path, code)
		}
	}
	srv.gpu.endWork()
	if code, status := request(http.MethodPost, "/admin/drain", ""); code != http.StatusOK || !status.Drained || status.ActiveRequests != 0 || status.ActiveReservations != 0 {
		t.Fatalf("post-teardown proof: %d %+v", code, status)
	}
}

func TestTranscodeHealthBindsExactPrivateWorkerIdentity(t *testing.T) {
	srv := newTestServer(t)
	srv.drain = workerdrain.New(func(context.Context) (int, string, string, error) { return 7, "", "synthetic-realm", nil }, srv.drainActiveJobs, nil)
	health := httptest.NewRecorder()
	srv.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	var public HealthResponse
	if health.Code != http.StatusOK || json.Unmarshal(health.Body.Bytes(), &public) != nil || public.NativeServerID != "synthetic-realm" || public.WorkerInstanceID == "" || health.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("public worker identity: %d %s", health.Code, health.Body.String())
	}
	proof, err := srv.drain.Observe(t.Context())
	if err != nil || proof.NativeServerID != public.NativeServerID || proof.WorkerInstanceID != public.WorkerInstanceID {
		t.Fatalf("public/private identity mismatch: %+v %v", proof, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/drain", nil)
	request.Header.Set("Authorization", "Bearer synthetic-at-rest-master-key")
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("at-rest master key admitted as control bearer: %d", response.Code)
	}
}
