package workersource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogApprovalIsBoundedAcrossRangesAndRetargets(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	a.options.Now = func() time.Time { return time.Unix(0, now.Load()) }
	var calls atomic.Int64
	var denied atomic.Bool
	var unavailable atomic.Bool
	a.options.PathApprover = func(context.Context, string, string) error {
		calls.Add(1)
		if unavailable.Load() {
			return ErrUnavailable
		}
		if denied.Load() {
			return ErrDenied
		}
		return nil
	}
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	for range 100 {
		if w := requestSource(a, source, "GET", "bytes=1-3"); w.Code != 206 {
			t.Fatalf("range HTTP%d", w.Code)
		}
	}
	if got := calls.Load(); got > 1 {
		t.Fatalf("catalog lookups scale with ranges: %d", got)
	}
	denied.Store(true)
	now.Add(int64(250 * time.Millisecond))
	if w := requestSource(a, source, "GET", "bytes=1-3"); w.Code != 403 {
		t.Fatalf("retarget HTTP%d", w.Code)
	}
	denied.Store(false)
	if w := requestSource(a, source, "GET", "bytes=1-3"); w.Code != 206 {
		t.Fatalf("denial cached: HTTP%d", w.Code)
	}
	now.Add(int64(catalogCheckInterval))
	unavailable.Store(true)
	if w := requestSource(a, source, "GET", "bytes=1-3"); w.Code != 503 {
		t.Fatalf("catalog failure HTTP%d", w.Code)
	}
	unavailable.Store(false)
	if w := requestSource(a, source, "GET", "bytes=1-3"); w.Code != 206 {
		t.Fatalf("catalog error cached/deleted grant: HTTP%d", w.Code)
	}
}

func TestCatalogRetargetInterruptsLongSourceRead(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789abcdef", Limits{ChunkBytes: 4})
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	a.options.Now = func() time.Time { return time.Unix(0, now.Load()) }
	var denied atomic.Bool
	a.options.PathApprover = func(context.Context, string, string) error {
		if denied.Load() {
			return ErrDenied
		}
		return nil
	}
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", source.URL, nil)
	r.Header.Set("X-Test-Node", "7")
	r.Header.Set("X-Silo-Source-Grant", source.Grant)
	r.Header.Set("If-Match", source.ETag)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(&mutatingWriter{ResponseWriter: w, after: func() { denied.Store(true); now.Add(int64(catalogCheckInterval)) }}, r)
	if w.Body.String() != "0123" {
		t.Fatalf("retargeted source continued: %q", w.Body.String())
	}
}

func TestCatalogCacheExpiresFromQueryStartAndCoalescesMisses(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	a.options.Now = func() time.Time { return time.Unix(0, now.Load()) }
	var calls atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	a.options.PathApprover = func(context.Context, string, string) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			if err := a.catalogAllowed(t.Context(), path, "media"); err != nil {
				t.Error(err)
			}
		})
	}
	<-started
	close(release)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent approvals: %d", calls.Load())
	}
	now.Add(int64(catalogCheckInterval))
	if err := a.catalogAllowed(t.Context(), path, "media"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("cache did not expire")
	}
	// Query latency must consume, rather than extend, the cache's lifetime.
	now.Add(int64(catalogCheckInterval))
	a.options.PathApprover = func(context.Context, string, string) error {
		calls.Add(1)
		now.Add(int64(catalogCheckInterval))
		return nil
	}
	if err := a.catalogAllowed(t.Context(), path, "media"); err != nil {
		t.Fatal(err)
	}
	if err := a.catalogAllowed(t.Context(), path, "media"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("slow query extended cache lifetime: %d", calls.Load())
	}
}

type observedSourceFile struct {
	*os.File
	stats     atomic.Int64
	afterRead func()
}

func (f *observedSourceFile) Stat() (os.FileInfo, error) { f.stats.Add(1); return f.File.Stat() }
func (f *observedSourceFile) ReadAt(data []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(data, off)
	if f.afterRead != nil {
		f.afterRead()
	}
	return n, err
}

func rewritePreservingPortableIdentity(t *testing.T, path string, pin FileIdentity) func() {
	t.Helper()
	initial, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Observe the filesystem clock advance on a sibling before the
	// streaming watcher starts. The actual equal-size rewrite remains
	// inside ReadAt, followed by restoring the original mtime.
	probe := path + ".clock"
	if err := os.WriteFile(probe, []byte("clock"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := os.Stat(probe)
		if err != nil {
			t.Fatal(err)
		}
		clock, err := identityOf(info)
		if err != nil {
			t.Fatal(err)
		}
		if clock.CTimeNano > pin.CTimeNano {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("filesystem clock did not advance")
		}
		if err := os.Chtimes(probe, initial.ModTime(), initial.ModTime()); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	return func() {
		if err := os.WriteFile(path, []byte("fedcba9876543210"), 0600); err != nil {
			t.Error(err)
		}
		if err := os.Chtimes(path, initial.ModTime(), initial.ModTime()); err != nil {
			t.Error(err)
			return
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Error(err)
			return
		}
		current, err := identityOf(info)
		if err != nil {
			t.Error(err)
			return
		}
		if current.Size != pin.Size || current.MTimeNano != pin.MTimeNano {
			t.Error("fixture changed portable identity")
			return
		}
		if current.CTimeNano != pin.CTimeNano {
			return
		}
		t.Error("filesystem ctime did not observe the gated rewrite")
		return
	}
}

func TestSourceChunksCheckFullPinAfterReadBeforeWrite(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		t.Run(strconv.FormatBool(mutate), func(t *testing.T) {
			a, path, _ := testAuthority(t, "0123456789abcdef", Limits{ChunkBytes: 4})
			source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
			if err != nil {
				t.Fatal(err)
			}
			record, err := a.options.Store.Get(t.Context(), source.Grant)
			if err != nil {
				t.Fatal(err)
			}
			file, _, err := openPin(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			observed := &observedSourceFile{File: file}
			if mutate {
				observed.afterRead = rewritePreservingPortableIdentity(t, path, record.Identity)
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", source.URL, nil)
			a.streamSource(w, r, 7, record, observed, 0, source.Size, 200)
			if mutate {
				if w.Body.Len() != 0 {
					t.Fatalf("bytes written after read-time mutation: %q", w.Body.String())
				}
			} else {
				if w.Body.String() != "0123456789abcdef" {
					t.Fatal(w.Body.String())
				}
				if got := observed.stats.Load(); got != 5 {
					t.Fatalf("descriptor stats per4 chunks+final=%d, want5", got)
				}
			}
		})
	}
}

type foreignOwnerStore struct{ Store }

func (s foreignOwnerStore) Get(ctx context.Context, grant string) (Record, error) {
	r, err := s.Store.Get(ctx, grant)
	if err != nil {
		return r, err
	}
	encoded, _ := json.Marshal(r)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &fields)
	fields["owner_id"] = json.RawMessage(`"another-owner"`)
	fields["owner_url"] = json.RawMessage(`"http://127.0.0.1:1"`)
	encoded, _ = json.Marshal(fields)
	_ = json.Unmarshal(encoded, &r)
	// A different mount namespace has a different device for the same file.
	r.Identity.Device ^= 1
	return r, nil
}

func TestForeignSourceOwnerDoesNotDeleteSharedPin(t *testing.T) {
	a, path, _ := testAuthority(t, "0123456789", Limits{})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	shared := a.options.Store
	a.options.Store = foreignOwnerStore{shared}
	if w := requestSource(a, source, http.MethodGet, ""); w.Code != 503 {
		t.Fatalf("foreign pin HTTP%d, want retryable 503", w.Code)
	}
	if _, err = shared.Get(t.Context(), source.Grant); err != nil {
		t.Fatalf("foreign replica deleted source: %v", err)
	}
	if _, err = a.Renew(t.Context(), 7, "work", 3, source.Grant); err == nil {
		t.Fatal("foreign owner renewed locally")
	}
	if _, err = shared.Get(t.Context(), source.Grant); err != nil {
		t.Fatalf("foreign renewal deleted source: %v", err)
	}
}

func TestConfiguredSourceReadersExceedDefaultWithoutQueueingOverflow(t *testing.T) {
	const readers = 40
	a, path, _ := testAuthority(t, "0123456789", Limits{MaxReaders: readers, ChunkBytes: 4})
	source, err := a.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}, readers), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var group sync.WaitGroup
	for range readers {
		group.Go(func() {
			r := httptest.NewRequest("GET", source.URL, nil)
			r.Header.Set("X-Test-Node", "7")
			r.Header.Set("X-Silo-Source-Grant", source.Grant)
			r.Header.Set("If-Match", source.ETag)
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(&mutatingWriter{ResponseWriter: w, after: func() { started <- struct{}{}; <-release }}, r)
			if w.Code != 200 || w.Body.String() != "0123456789" {
				t.Errorf("reader HTTP%d %q", w.Code, w.Body.String())
			}
		})
	}
	for range readers {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("configured reader capacity was not available")
		}
	}
	if w := requestSource(a, source, "GET", ""); w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("overflow HTTP%d", w.Code)
	}
	once.Do(func() { close(release) })
	group.Wait()
}
