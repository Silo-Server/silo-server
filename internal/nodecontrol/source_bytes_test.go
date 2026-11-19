package nodecontrol

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// read asks for the bytes of grant as nodeID, with header added.
func (f *openFixture) read(nodeID int, key, grant string, header http.Header) (int, string, http.Header) {
	f.t.Helper()
	h := http.Header{}
	if grant != "" {
		h.Set(nodesource.HeaderSourceGrant, grant)
	}
	for name, values := range header {
		h[name] = values
	}
	got := f.serve(nodeID, key, http.MethodGet, PathSources, nil, h)
	return got.Code, got.Body.String(), got.Header()
}

func TestSourceBytesServeTheGrantedRevision(t *testing.T) {
	f := newOpenFixture(t)
	const content = "0123456789abcdef"
	path := f.library(42, "movie.mkv", content)
	grant := f.mint(nodeA, 42, path)
	claims, err := f.grants.Verify(grant)
	if err != nil {
		t.Fatal(err)
	}
	etag := claims.ETag()
	sentBefore := testutil.ToFloat64(nodeSourceBytes.WithLabelValues(strconv.Itoa(nodeA)))

	status, body, header := f.read(nodeA, f.keyA, grant, nil)
	if status != http.StatusOK || body != content || header.Get("ETag") != etag || header.Get("Accept-Ranges") != "bytes" ||
		header.Get("Cache-Control") != "no-store" || header.Get("Content-Encoding") != "" {
		t.Fatalf("whole source = %d %q %v", status, body, header)
	}
	for _, tc := range []struct {
		name, rangeHeader, body, contentRange string
	}{
		{"range", "bytes=2-5", "2345", "bytes 2-5/16"},
		{"open range", "bytes=12-", "cdef", "bytes 12-15/16"},
		{"suffix", "bytes=-3", "def", "bytes 13-15/16"},
	} {
		status, body, header := f.read(nodeA, f.keyA, grant, http.Header{"Range": {tc.rangeHeader}, "If-Match": {etag}})
		if status != http.StatusPartialContent || body != tc.body || header.Get("Content-Range") != tc.contentRange || header.Get("ETag") != etag {
			t.Errorf("%s: %d %q %q", tc.name, status, body, header.Get("Content-Range"))
		}
	}
	if got := testutil.ToFloat64(nodeSourceBytes.WithLabelValues(strconv.Itoa(nodeA))) - sentBefore; got != float64(len(content)+4+4+3) {
		t.Errorf("bytes counted %v, want %d", got, len(content)+11)
	}

	// Past the end, and several ranges at once, are 416 with the size and
	// the revision, no-store like every answer (ServeContent's own error
	// path drops both headers).
	for _, rangeHeader := range []string{"bytes=16-", "bytes=0-1,4-5"} {
		if status, _, header := f.read(nodeA, f.keyA, grant, http.Header{"Range": {rangeHeader}}); status != http.StatusRequestedRangeNotSatisfiable ||
			header.Get("Content-Range") != "bytes */16" || header.Get("Cache-Control") != "no-store" || header.Get("ETag") != etag {
			t.Errorf("%s: %d %v, want 416 bytes */16, no-store and the ETag", rangeHeader, status, header)
		}
	}
	// If-Match on another revision fails; the conditions that could widen
	// or empty the answer are ignored.
	if status, _, header := f.read(nodeA, f.keyA, grant, http.Header{"If-Match": {`"file-42-1-1"`}}); status != http.StatusPreconditionFailed ||
		header.Get("Cache-Control") != "no-store" || header.Get("ETag") != "" {
		t.Errorf("If-Match another revision = %d %v, want 412, no-store, no ETag", status, header)
	}
	for name, value := range map[string]string{
		"If-None-Match":       etag,
		"If-Modified-Since":   time.Now().Add(time.Hour).UTC().Format(http.TimeFormat),
		"If-Unmodified-Since": time.Unix(0, 0).UTC().Format(http.TimeFormat),
		"If-Range":            `"another"`,
	} {
		status, body, _ := f.read(nodeA, f.keyA, grant, http.Header{name: {value}, "Range": {"bytes=0-3"}})
		if status != http.StatusPartialContent || body != "0123" {
			t.Errorf("%s: %d %q, want the range regardless", name, status, body)
		}
	}
}

func TestSourceBytesRefusals(t *testing.T) {
	f := newOpenFixture(t)
	path := f.library(42, "movie.mkv", "0123456789")
	grant := f.mint(nodeA, 42, path)

	if status, _, _ := f.read(nodeA, f.keyA, "", nil); status != http.StatusForbidden {
		t.Fatalf("no grant = %d, want 403", status)
	}
	if status, _, _ := f.read(nodeB, f.keyB, grant, nil); status != http.StatusForbidden {
		t.Fatalf("another node's grant = %d, want 403", status)
	}
	// The route reads no catalog: a file the catalog dropped is still
	// served until the next renewal refuses it.
	f.catalog.set(func(c *fakeCatalog) { delete(c.files, 42) })
	if status, _, _ := f.read(nodeA, f.keyA, grant, nil); status != http.StatusOK {
		t.Fatalf("dropped from the catalog = %d, want 200 (no catalog read)", status)
	}

	// The whole life: a grant past MaxGrantAge is refused, even before its
	// own expiry would end it.
	minted := time.Now()
	clock := minted
	aging := nodesource.NewGrantsWithClock(func() string { return "open-test-secret" }, func() time.Time { return clock })
	f.server.opts.Grants = aging
	old := f.mint(nodeA, 42, path)
	if status, _, _ := f.read(nodeA, f.keyA, old, nil); status != http.StatusOK {
		t.Fatalf("fresh grant = %d", status)
	}
	// Renewed every 20 minutes, as a node does, up to the cap.
	renewed := old
	for clock = minted.Add(nodesource.RenewInterval); clock.Before(minted.Add(nodesource.MaxGrantAge)); clock = clock.Add(nodesource.RenewInterval) {
		claims, err := aging.Verify(renewed)
		if err != nil {
			t.Fatalf("renewable grant at %s: %v", clock.Sub(minted), err)
		}
		if renewed, err = aging.Renew(claims); err != nil {
			t.Fatal(err)
		}
	}
	clock = minted.Add(nodesource.MaxGrantAge)
	if status, _, _ := f.read(nodeA, f.keyA, renewed, nil); status != http.StatusForbidden {
		t.Fatalf("grant at its whole life = %d, want 403", status)
	}
	f.server.opts.Grants = f.grants

	// Changed and removed.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := f.read(nodeA, f.keyA, grant, nil); status != http.StatusPreconditionFailed {
		t.Fatalf("changed = %d, want 412", status)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if status, _, header := f.read(nodeA, f.keyA, grant, nil); status != http.StatusGone || header.Get("ETag") != "" {
		t.Fatalf("removed = %d, want 410 without an ETag", status)
	}
	// A file that cannot be opened is not gone: a 410 ends the node's job,
	// a 503 is retried. A symlink loop stands in for an I/O error.
	if err := os.Symlink(filepath.Base(path), path); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := f.read(nodeA, f.keyA, grant, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("unopenable = %d, want 503", status)
	}

	f.server.opts.Grants = nodesource.NewGrants(func() string { return "" })
	if status, _, _ := f.read(nodeA, f.keyA, grant, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("no grant key = %d, want 503", status)
	}
}

// The core serves from the descriptor it checked: a file replaced at the
// path after the check is never served, and the checked bytes are.
func TestSourceBytesServeTheCheckedDescriptor(t *testing.T) {
	f := newOpenFixture(t)
	path := f.library(42, "movie.mkv", "original bytes")
	grant := f.mint(nodeA, 42, path)
	f.server.afterSourceOpen = func() {
		replacement := filepath.Join(filepath.Dir(path), "replacement.mkv")
		if err := os.WriteFile(replacement, []byte("a replacement of another size"), 0o600); err != nil {
			t.Error(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Error(err)
		}
	}
	if status, body, _ := f.read(nodeA, f.keyA, grant, nil); status != http.StatusOK || body != "original bytes" {
		t.Fatalf("read = %d %q, want the checked bytes", status, body)
	}
	f.server.afterSourceOpen = nil
	if status, _, _ := f.read(nodeA, f.keyA, grant, nil); status != http.StatusPreconditionFailed {
		t.Fatalf("next read of the replaced file = %d, want 412", status)
	}
}
