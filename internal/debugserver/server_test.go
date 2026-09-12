package debugserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAccessAndFiniteRoutes(t *testing.T) {
	h := newHandler(Config{}, "test-instance")
	for _, tc := range []struct {
		method, path, host, origin, site string
		status                           int
	}{
		{"GET", "/debug/pprof/", "127.0.0.1:6060", "", "", 200},
		{"GET", "/debug/pprof/", "[::1]:9999", "", "", 200},
		{"GET", "/debug/pprof/", "127.0.0.1:9000", "http://127.0.0.1:9000", "same-origin", 200},
		{"POST", "/debug/pprof/heap", "127.0.0.1:6060", "", "", 405},
		{"HEAD", "/debug/pprof/heap", "127.0.0.1:6060", "", "", 405},
		{"GET", "/debug/pprof/", "attacker.example:6060", "", "", 403},
		{"GET", "/debug/pprof/", "localhost:6060", "", "", 403},
		{"GET", "/debug/pprof/", "127.0.0.1:6060", "https://attacker.example", "", 403},
		{"GET", "/debug/pprof/", "127.0.0.1:6060", "null", "", 403},
		{"GET", "/debug/pprof/", "127.0.0.1:6060", "", "cross-site", 403},
		{"GET", "/debug/pprof/cmdline", "127.0.0.1:6060", "", "", 404},
		{"GET", "/debug/pprof/goroutineleak", "127.0.0.1:6060", "", "", 404},
		{"GET", "/debug/pprof/heap/extra", "127.0.0.1:6060", "", "", 404},
		{"GET", "/debug/vars", "127.0.0.1:6060", "", "", 404},
		{"GET", "/debug/pprof/block", "127.0.0.1:6060", "", "", 409},
		{"GET", "/debug/pprof/mutex", "127.0.0.1:6060", "", "", 409},
	} {
		t.Run(tc.method+tc.path+tc.host+tc.origin+tc.site, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://127.0.0.1:6060"+tc.path, nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
			if tc.path == "/debug/pprof/" && tc.status == 200 && (strings.Contains(w.Body.String(), "cmdline") || strings.Contains(w.Body.String(), "href=\"/debug/pprof/block")) {
				t.Fatal("index advertises unsupported or disabled endpoint")
			}
		})
	}
}

func TestMethodEnforcementWithLegacyMux(t *testing.T) {
	if os.Getenv("SILO_TEST_LEGACY_MUX") == "1" {
		h := newHandler(Config{}, "test")
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:6060/debug/pprof/profile", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 405 {
			t.Fatalf("method guard bypassed: %d", w.Code)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMethodEnforcementWithLegacyMux$")
	cmd.Env = append(os.Environ(), "SILO_TEST_LEGACY_MUX=1", "GODEBUG=httpmuxgo121=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("legacy mux: %v: %s", err, out)
	}
}

func testServer(t *testing.T, c Config) (*Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := startListener(ln, c, "test-instance")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, "http://" + ln.Addr().String()
}

func waitCapture(t *testing.T, active bool) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for (len(captureSlot) != 0) != active {
		select {
		case <-timer.C:
			t.Fatal("capture did not reach expected state")
		case <-tick.C:
		}
	}
}

func TestCaptureBusyAndShutdownCancellation(t *testing.T) {
	s, base := testServer(t, Config{})
	done := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := http.Get(base + "/debug/pprof/profile?seconds=60")
		if err != nil {
			errs <- err
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		done <- resp
	}()
	waitCapture(t, true)
	resp, err := http.Get(base + "/debug/pprof/heap?gc=1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("concurrent snapshot got %d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-done:
		if response.Trailer.Get("X-Silo-Capture-Interrupted") != "true" {
			t.Fatalf("missing interrupted metadata: %v", response.Trailer)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("shutdown left capture running")
	}
	waitCapture(t, false)
}

func TestClientCancellationReleasesCapture(t *testing.T) {
	_, base := testServer(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/debug/pprof/heap?seconds=60", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
	}()
	waitCapture(t, true)
	cancel()
	<-done
	waitCapture(t, false)
}

func TestDisabledAndOccupiedListener(t *testing.T) {
	if s, err := Start(Config{}, "test"); s != nil || err != nil {
		t.Fatalf("disabled listener: %v, %v", s, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if s, err := Start(Config{Listen: ln.Addr().String()}, "test"); s != nil || err == nil {
		t.Fatalf("occupied port: %v, %v", s, err)
	}
}

func TestTimedCaptureExtendsWriteDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: newHandler(Config{}, "deadline-test"), WriteTimeout: 500 * time.Millisecond, ReadHeaderTimeout: time.Second}
	defer func() { _ = srv.Close() }()
	go func() { _ = srv.Serve(ln) }()
	resp, err := http.Get("http://" + ln.Addr().String() + "/debug/pprof/profile?seconds=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("capture exceeded original write deadline: status=%d bytes=%d err=%v", resp.StatusCode, len(body), err)
	}
}

// Real profile/trace output must remain compatible with the toolchain. These
// tests are serialized because the Go profiler is process-wide.
func TestProfilesParseWithGoTools(t *testing.T) {
	runtime.SetBlockProfileRate(1_000_000)
	runtime.SetMutexProfileFraction(100)
	defer runtime.SetBlockProfileRate(0)
	defer runtime.SetMutexProfileFraction(0)
	_, base := testServer(t, Config{BlockRate: 1_000_000, MutexFraction: 100})
	for _, profile := range []string{"profile?seconds=1", "heap?gc=1", "allocs", "goroutine", "threadcreate", "block", "mutex", "trace?seconds=.02"} {
		t.Run(profile, func(t *testing.T) {
			resp, err := http.Get(base + "/debug/pprof/" + profile)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("capture: %d, %v, %s", resp.StatusCode, err, body)
			}
			if resp.Trailer.Get("X-Silo-Capture-Interrupted") != "false" {
				t.Fatalf("capture metadata: %v", resp.Trailer)
			}
			file := filepath.Join(t.TempDir(), "capture")
			if err := os.WriteFile(file, body, 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"tool", "pprof", "-top", file}
			if strings.HasPrefix(profile, "trace") {
				args = []string{"tool", "trace", "-d=parsed", file}
			}
			cmd := exec.Command("go", args...)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Go tool cannot parse capture: %v: %s", err, output)
			}
		})
	}
	// Exercise the URL tooling path too, including symbol resolution. Modern Go
	// profiles carry their symbols, so the symbol/cmdline endpoints are omitted.
	cmd := exec.Command("go", "tool", "pprof", "-top", base+"/debug/pprof/heap")
	cmd.Env = append(os.Environ(), "PPROF_TMPDIR="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("live pprof: %v: %s", err, output)
	}
}
