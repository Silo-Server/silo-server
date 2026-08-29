package httpstream

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The benchmarks below cover the three shapes P0a's writer changes touch, as
// required by the design's "P0 is not zero-risk" note: a large sequential body
// (direct play), a progressively written body (remux), and many small requests
// (high-RPS HLS). They exist to catch a regression in the wrapper chain, not to
// produce an absolute throughput number — run them before and after a change to
// the writer chain and compare.

func benchMediaFile(b *testing.B, size int) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "bench.bin")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(int64(size)); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkDirectPlayReadFrom measures a whole-file transfer through the rolling
// deadline writer — the direct-play shape, and the one that depends on the
// io.ReaderFrom fast path surviving the wrapper chain.
func BenchmarkDirectPlayReadFrom(b *testing.B) {
	const size = 32 << 20
	path := benchMediaFile(b, size)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = f.Close() }()
		stat, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.ServeContent(NewRollingDeadlineWriter(w), r, "bench.bin", stat.ModTime(), f)
	}))
	defer srv.Close()

	client := srv.Client()
	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}
		_ = resp.Body.Close()
	}
}

// BenchmarkRemuxProgressiveWrite measures a body written incrementally and
// flushed, which is the remux shape: it never uses ReadFrom, so it isolates the
// per-Write overhead the wrapper chain adds.
func BenchmarkRemuxProgressiveWrite(b *testing.B) {
	const (
		chunk  = 64 << 10
		chunks = 512 // 32 MiB
	)
	buf := make([]byte, chunk)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sw := NewRollingDeadlineWriter(w)
		sw.WriteHeader(http.StatusOK)
		for i := 0; i < chunks; i++ {
			if _, err := sw.Write(buf); err != nil {
				return
			}
		}
		sw.Flush()
	}))
	defer srv.Close()

	client := srv.Client()
	b.SetBytes(chunk * chunks)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}
		_ = resp.Body.Close()
	}
}

// BenchmarkHLSSegmentRPS measures many small segment-sized responses, the shape
// where per-request wrapper allocation dominates and where a regression would
// show up as increased allocs/op rather than reduced throughput.
func BenchmarkHLSSegmentRPS(b *testing.B) {
	const segment = 512 << 10
	path := benchMediaFile(b, segment)
	modTime := time.Now()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = f.Close() }()
		http.ServeContent(NewRollingDeadlineWriter(w), r, "000.ts", modTime, f)
	}))
	defer srv.Close()

	client := srv.Client()
	b.SetBytes(segment)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.Get(srv.URL)
			if err != nil {
				b.Error(err)
				return
			}
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				b.Error(err)
				return
			}
			_ = resp.Body.Close()
		}
	})
}
