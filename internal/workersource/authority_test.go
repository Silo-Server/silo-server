package workersource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func testAuthority(t *testing.T, data string, limits Limits) (*Authority, string, *atomic.Bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "approved-media.mp4")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	active := &atomic.Bool{}
	active.Store(true)
	a, err := New(Options{
		Store: NewMemoryStore(), Origin: "https://core.example", Limits: limits,
		PathApprover: func(_ context.Context, p, kind string) error {
			if p != path || kind != "media" {
				return ErrDenied
			}
			return nil
		},
		Active: func(_ context.Context, node int, work string, generation uint64) bool {
			return active.Load() && node == 7 && work == "work" && generation == 3
		},
		VerifyRequest: func(r *http.Request, node int) error {
			if r.Header.Get("X-Test-Node") != strconv.Itoa(node) {
				return ErrDenied
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, path, active
}

func requestSource(a *Authority, source workerwire.Source, method, byteRange string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, source.URL, nil)
	r.Header.Set("X-Test-Node", strconv.Itoa(source.NodeID))
	r.Header.Set("X-Silo-Source-Grant", source.Grant)
	r.Header.Set("If-Match", source.ETag)
	if byteRange != "" {
		r.Header.Set("Range", byteRange)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestAcquireRequiresCatalogApprovalAndHidesPath(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatalf("Acquire approved source: %v", err)
	}
	if source.Size != 10 || source.NodeID != 7 || source.WorkID != "work" || source.Generation != 3 || source.Grant == "" || source.ETag == "" || source.Ref.Revision == "" {
		t.Fatalf("incomplete source: %+v", source)
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), path) || strings.Contains(string(encoded), filepath.Base(path)) {
		t.Fatalf("descriptor exposed path: %s", encoded)
	}
	if _, err := a.Acquire(t.Context(), 7, "work", 3, filepath.Join(t.TempDir(), "unapproved"), "media"); !errors.Is(err, ErrDenied) {
		t.Fatalf("unapproved acquire = %v", err)
	}
	other, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	if source.Ref != other.Ref || source.ETag != other.ETag || source.Grant == other.Grant {
		t.Fatal("renewable authority changed stable source identity or reused a grant")
	}
}

func TestAuthenticatedRangeAndHead(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{ChunkBytes: 3})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, rangeValue, body, contentRange string
		code                                   int
	}{
		{"GET", "", "0123456789", "", 200},
		{"GET", "bytes=2-5", "2345", "bytes 2-5/10", 206},
		{"GET", "bytes=7-", "789", "bytes 7-9/10", 206},
		{"GET", "bytes=-3", "789", "bytes 7-9/10", 206},
		{"GET", "bytes=100-", "", "bytes */10", 416},
		{"GET", "bytes=3-1", "", "", 400},
		{"GET", "bytes=0-1,3-4", "", "", 400},
		{"GET", "bytes=+1-3", "", "", 400},
		{"GET", "bytes=-+3", "", "", 400},
		{"GET", "bytes=1-+3", "", "", 400},
		{"GET", "bytes= 1-3", "", "", 400},
		{"HEAD", "bytes=100-", "", "", 200},
	} {
		t.Run(tc.method+tc.rangeValue, func(t *testing.T) {
			w := requestSource(a, source, tc.method, tc.rangeValue)
			if w.Code != tc.code {
				t.Fatalf("status = %d, want %d", w.Code, tc.code)
			}
			if tc.code < 400 && w.Body.String() != tc.body {
				t.Fatalf("body = %q, want %q", w.Body.String(), tc.body)
			}
			if w.Header().Get("Content-Range") != tc.contentRange {
				t.Fatalf("range = %q", w.Header().Get("Content-Range"))
			}
			if tc.code < 400 && (w.Header().Get("ETag") != source.ETag || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Encoding") != "identity") {
				t.Fatalf("unsafe headers: %v", w.Header())
			}
		})
	}
}

func TestGrantProofAndExactRevisionRequired(t *testing.T) {
	a, path, active := testAuthority(t, "0123456789", Limits{})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
		code   int
	}{
		{"wrong-proof", func(r *http.Request) { r.Header.Set("X-Test-Node", "other") }, 401},
		{"missing-grant", func(r *http.Request) { r.Header.Del("X-Silo-Source-Grant") }, 403},
		{"wrong-grant", func(r *http.Request) { r.Header.Set("X-Silo-Source-Grant", "other") }, 403},
		{"missing-revision", func(r *http.Request) { r.Header.Del("If-Match") }, 412},
		{"wrong-revision", func(r *http.Request) { r.Header.Set("If-Match", `"other"`) }, 412},
		{"wrong-node", func(r *http.Request) {
			r.URL.Path = strings.Replace(r.URL.Path, "/7/", "/8/", 1)
			r.Header.Set("X-Test-Node", "8")
		}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", source.URL, nil)
			r.Header.Set("X-Test-Node", "7")
			r.Header.Set("X-Silo-Source-Grant", source.Grant)
			r.Header.Set("If-Match", source.ETag)
			tc.change(r)
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, r)
			if w.Code != tc.code || strings.Contains(w.Body.String(), "0123456789") {
				t.Fatalf("status/body = %d/%q", w.Code, w.Body.String())
			}
		})
	}
	active.Store(false)
	if w := requestSource(a, source, "GET", ""); w.Code != 403 {
		t.Fatalf("revoked work status = %d", w.Code)
	}
}

type mutatingWriter struct {
	http.ResponseWriter
	after func()
	once  atomic.Bool
}

func (w *mutatingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if w.once.CompareAndSwap(false, true) {
		w.after()
	}
	return n, err
}

func TestMutationDuringReadTerminatesBodyAndRevokesGrant(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789abcdef", Limits{ChunkBytes: 4})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.Handler().ServeHTTP(&mutatingWriter{ResponseWriter: w, after: func() {
			if err := os.WriteFile(path, []byte("fedcba9876543210"), 0o600); err != nil {
				t.Error(err)
			}
		}}, r)
	}))
	defer server.Close()
	r, err := http.NewRequest("GET", server.URL+strings.TrimPrefix(source.URL, source.Origin), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Test-Node", "7")
	r.Header.Set("X-Silo-Source-Grant", source.Grant)
	r.Header.Set("If-Match", source.ETag)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	bytes, err := io.ReadAll(response.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(bytes) != "0123" {
		t.Fatalf("mutated stream = %q, %v", bytes, err)
	}
	if w := requestSource(a, source, "GET", ""); w.Code != 410 {
		t.Fatalf("mutated grant remains usable: %d", w.Code)
	}
}

func TestRevocationCancellationAndQuotaRelease(t *testing.T) {
	a, path, active := testAuthority(t, "0123456789abcdef", Limits{ChunkBytes: 4, MaxGrants: 1, MaxSourceBytes: 16, MaxReaders: 1})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acquire(t.Context(), 7, "work", 3, path, "media"); !errors.Is(err, ErrQuota) {
		t.Fatalf("grant quota = %v", err)
	}
	r := httptest.NewRequest("GET", source.URL, nil)
	r.Header.Set("X-Test-Node", "7")
	r.Header.Set("X-Silo-Source-Grant", source.Grant)
	r.Header.Set("If-Match", source.ETag)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(&mutatingWriter{ResponseWriter: w, after: cancel}, r.WithContext(ctx))
	if w.Body.String() != "0123" {
		t.Fatalf("cancel continued input: %q", w.Body.String())
	}
	if got := requestSource(a, source, "GET", ""); got.Code != 200 {
		t.Fatalf("canceled reader retained quota: %d", got.Code)
	}
	active.Store(false)
	if _, err := a.Renew(t.Context(), 7, "work", 3, source.Grant); !errors.Is(err, ErrInactive) {
		t.Fatalf("renew revoked work = %v", err)
	}
	active.Store(true)
	if err := a.RevokeWork(t.Context(), 7, "work", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Renew(t.Context(), 7, "work", 3, source.Grant); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked grant revived = %v", err)
	}
	if _, err := a.Acquire(t.Context(), 7, "work", 3, path, "media"); !errors.Is(err, ErrInactive) {
		t.Fatalf("revoked generation re-admitted = %v", err)
	}
}

func TestRenewReopensSamePinAndRejectsReplacement(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := a.Renew(t.Context(), 7, "work", 3, source.Grant)
	if err != nil {
		t.Fatal(err)
	}
	if renewed != source {
		t.Fatal("renewal changed input/lease identity")
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	if err := os.WriteFile(replacement, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Renew(t.Context(), 7, "work", 3, source.Grant); !errors.Is(err, ErrChanged) {
		t.Fatalf("replacement renewal = %v", err)
	}
	if got := requestSource(a, source, "GET", ""); got.Code != 410 {
		t.Fatalf("replacement remains readable: %d", got.Code)
	}
}

func TestExpiryAndReleaseAreNotRenewalAuthority(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	now := time.Now()
	a.options.Now = func() time.Time { return now }
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(121 * time.Second)
	if got := requestSource(a, source, "HEAD", ""); got.Code != 410 {
		t.Fatalf("expired grant status=%d", got.Code)
	}
	if _, err := a.Renew(t.Context(), 7, "work", 3, source.Grant); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired renewal = %v", err)
	}
	a.options.Now = time.Now
	newSource, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Release(t.Context(), 8, "work", 3, newSource.Grant); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-node release = %v", err)
	}
	if err := a.Release(t.Context(), 7, "work", 3, newSource.Grant); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(t.Context(), 7, "work", 3, newSource.Grant); err != nil {
		t.Fatal(err)
	}
	if got := requestSource(a, newSource, "GET", ""); got.Code != 410 {
		t.Fatalf("released grant status=%d", got.Code)
	}
}

func TestNewRejectsMissingAuthority(t *testing.T) {
	for _, options := range []Options{{}, {Store: NewMemoryStore(), Origin: "http://core.example"}} {
		if _, err := New(options); err == nil {
			t.Fatalf("missing authority admitted: %s", options.Origin)
		}
	}
}

func TestStreamingRenewalRefreshesExpiryBeforeRejectingOldSnapshot(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789abcdef", Limits{ChunkBytes: 4})
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	a.options.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", source.URL, nil)
	r.Header.Set("X-Test-Node", "7")
	r.Header.Set("X-Silo-Source-Grant", source.Grant)
	r.Header.Set("If-Match", source.ETag)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(&mutatingWriter{ResponseWriter: w, after: func() {
		// A replica renews while this stream still holds the initial expiry.
		next := a.options.Now().Add(121 * time.Second)
		if err := a.options.Store.Extend(t.Context(), source.Grant, next.Add(DefaultTTL)); err != nil {
			t.Error(err)
		}
		clock.Store(next.UnixNano())
	}}, r)
	if w.Body.String() != "0123456789abcdef" {
		t.Fatalf("active renewed stream was truncated: %q", w.Body.String())
	}
}

func TestStreamingRevocationAndReleaseTerminateReads(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(fmt.Sprint("release=", release), func(t *testing.T) {
			a, path, active := testAuthority(t, "0123456789abcdef", Limits{ChunkBytes: 4})
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			a.options.Now = func() time.Time { return time.Unix(0, clock.Load()) }
			source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", source.URL, nil)
			r.Header.Set("X-Test-Node", "7")
			r.Header.Set("X-Silo-Source-Grant", source.Grant)
			r.Header.Set("If-Match", source.ETag)
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(&mutatingWriter{ResponseWriter: w, after: func() {
				if release {
					if err := a.Release(t.Context(), 7, "work", 3, source.Grant); err != nil {
						t.Error(err)
					}
					clock.Add(int64(time.Second))
				} else {
					active.Store(false)
				}
			}}, r)
			if w.Body.String() != "0123" {
				t.Fatalf("revoked stream continued: %q", w.Body.String())
			}
		})
	}
}

func TestConcurrentReadersRespectQuota(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{MaxReaders: 1, ChunkBytes: 4})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r := httptest.NewRequest("GET", source.URL, nil)
	r.Header.Set("X-Test-Node", "7")
	r.Header.Set("X-Silo-Source-Grant", source.Grant)
	r.Header.Set("If-Match", source.ETag)
	go func() {
		defer close(finished)
		a.Handler().ServeHTTP(&mutatingWriter{ResponseWriter: httptest.NewRecorder(), after: func() {
			close(started)
			<-release
		}}, r)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first reader did not start")
	}
	if w := requestSource(a, source, "GET", ""); w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Errorf("overlapping reader = %d", w.Code)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("first reader did not finish")
	}
	if w := requestSource(a, source, "GET", ""); w.Code != 200 {
		t.Fatalf("finished reader retained quota: %d", w.Code)
	}
}

func TestCoreReplicaReopensExactPinAndRejectsPreservedMtimeMutation(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(a.options)
	if err != nil {
		t.Fatal(err)
	}
	if w := requestSource(b, source, "GET", "bytes=1-3"); w.Code != 206 || w.Body.String() != "123" {
		t.Fatalf("replica same pin = %d/%q", w.Code, w.Body.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("abcdefghij"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	// Filesystems can share one coarse ctime tick across adjacent writes. Wait
	// for the observable metadata change this contract pins, not a fixed sleep.
	initial, err := identityOf(info)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		currentInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		current, err := identityOf(currentInfo)
		if err != nil {
			t.Fatal(err)
		}
		if current.CTimeNano != initial.CTimeNano {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("filesystem ctime never observed the rewrite")
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if w := requestSource(b, source, "GET", ""); w.Code != 410 {
		t.Fatalf("replica accepted preserved-mtime mutation = %d", w.Code)
	}
	if w := requestSource(a, source, "GET", ""); w.Code != 410 {
		t.Fatalf("mutation revocation was not shared = %d", w.Code)
	}
}
