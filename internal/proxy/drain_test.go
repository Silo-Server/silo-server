package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workerdrain"
)

func TestProxyDrainListenerPreservesExistingEgressAndRejectsLateDispatch(t *testing.T) {
	secret := "drain-proxy-secret"
	srv := newSocketProxyServer(t, secret, nil)
	var fence atomic.Value
	fence.Store("")
	var deny atomic.Bool
	srv.drain = workerdrain.New(func(context.Context) (int, string, string, error) {
		return 7, fence.Load().(string), "synthetic-realm", nil
	}, nil, func(context.Context, string) bool { return deny.Load() })
	path := writeSocketProxyMedia(t)
	token := socketProxyMediaToken(t, secret, path)
	listener := httptest.NewServer(srv.Handler())
	defer listener.Close()
	get := func(url, auth string) (int, workerdrain.Status) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, listener.URL+url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			request.Header.Set("Authorization", "Bearer "+auth)
		}
		response, err := listener.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var status workerdrain.Status
		if url == "/admin/drain" && response.StatusCode == http.StatusOK {
			if err = json.NewDecoder(response.Body).Decode(&status); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode, status
	}
	if code, _ := get("/stream/direct/"+token, ""); code != http.StatusOK {
		t.Fatalf("initial media: %d", code)
	}
	fence.Store("durable-fence")
	if code, status := get("/admin/drain", secret); code != http.StatusOK || !status.Fenced || status.Drained || status.ActiveReservations != 1 || status.NativeServerID != "synthetic-realm" {
		t.Fatalf("quiet live token lost: %d %+v", code, status)
	}
	if code, _ := get("/stream/direct/"+token, ""); code != http.StatusOK {
		t.Fatalf("existing egress interrupted: %d", code)
	}
	late, err := streamtoken.Sign(streamtoken.Claims{SessionID: "late", MediaPath: path, PlayMethod: "direct"}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := get("/stream/direct/"+late, ""); code != http.StatusServiceUnavailable {
		t.Fatalf("late token admitted: %d", code)
	}
	if code, _ := get("/admin/drain", ""); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated drain read: %d", code)
	}
	if code, _ := get("/admin/drain", "synthetic-at-rest-master-key"); code != http.StatusUnauthorized {
		t.Fatalf("at-rest master key admitted as control bearer: %d", code)
	}
	deny.Store(true)
	if code, status := get("/admin/drain", secret); code != http.StatusOK || !status.Drained || status.ActiveJobs != 0 || status.ActiveRequests != 0 || status.ActiveReservations != 0 {
		t.Fatalf("sealed proof: %d %+v", code, status)
	}
	if code, _ := get("/stream/direct/"+token, ""); code != http.StatusServiceUnavailable {
		t.Fatalf("late resumed egress after proof: %d", code)
	}
	fence.Store("")
	deny.Store(false)
	if code, _ := get("/stream/direct/"+late, ""); code != http.StatusOK {
		t.Fatalf("explicit cancellation did not reopen: %d", code)
	}
}

func TestProxyHealthBindsRealmAndExactWorkerProcess(t *testing.T) {
	var unavailable atomic.Bool
	servers := []*Server{newSocketProxyServer(t, "node-control-secret", nil), newSocketProxyServer(t, "node-control-secret", nil)}
	instances := make(map[string]bool)
	for _, srv := range servers {
		srv.drain = workerdrain.New(func(context.Context) (int, string, string, error) {
			if unavailable.Load() {
				return 0, "", "", workerdrain.ErrUnavailable
			}
			return 7, "", "synthetic-realm", nil
		}, nil, nil)
		unavailable.Store(true)
		health := httptest.NewRecorder()
		srv.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		var before healthResponse
		if health.Code != http.StatusOK || json.Unmarshal(health.Body.Bytes(), &before) != nil || before.NativeServerID != "" || before.WorkerInstanceID != "" {
			t.Fatalf("unregistered health synthesized a realm: %d %s", health.Code, health.Body.String())
		}
		unavailable.Store(false)
		health = httptest.NewRecorder()
		srv.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		var public healthResponse
		if health.Code != http.StatusOK || json.Unmarshal(health.Body.Bytes(), &public) != nil || public.NativeServerID != "synthetic-realm" || public.WorkerInstanceID == "" || health.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("public worker identity: %d %s", health.Code, health.Body.String())
		}
		proof, err := srv.drain.Observe(t.Context())
		if err != nil || proof.NativeServerID != public.NativeServerID || proof.WorkerInstanceID != public.WorkerInstanceID || instances[proof.WorkerInstanceID] {
			t.Fatalf("sibling/private identity confused: %+v %v", proof, err)
		}
		instances[proof.WorkerInstanceID] = true
	}
}

func TestProxyDrainListenerDoesNotRetainAnonymousGrantRequests(t *testing.T) {
	srv := newGrantProxyServer(t, nil)
	srv.drain = workerdrain.New(func(context.Context) (int, string, string, error) { return 7, "", "synthetic-realm", nil }, nil, nil)
	response := grantRequest(t, srv, http.MethodGet, "/stream/v3/guessed-session", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous grant: %d", response.Code)
	}
	status, err := srv.drain.Observe(t.Context())
	if err != nil || status.ActiveReservations != 0 || status.ActiveRequests != 0 {
		t.Fatalf("anonymous reservation retained: %+v %v", status, err)
	}
}
