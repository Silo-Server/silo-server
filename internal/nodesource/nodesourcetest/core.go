package nodesourcetest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// Core serves one file the way the core's source routes do, to an opener in
// core mode, so a test can run a consumer on a relayed input (a loopback relay
// URL) and compare it with the same consumer on the file's path.
type Core struct {
	Path string
	// Requests counts the source byte reads the core served.
	Requests atomic.Int64
	opener   *nodesource.Opener
	server   *httptest.Server
	etag     string
	size     int64
	mtimeNS  int64
}

// NewCore serves the file at path. The opener and the server close with the
// test.
func NewCore(t testing.TB, path string) *Core {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &Core{Path: path, size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
	c.etag = nodesource.Revision{Kind: nodesource.KindFile, ID: 1, Size: c.size, ModTimeUnixNano: c.mtimeNS}.ETag()
	c.server = httptest.NewServer(http.HandlerFunc(c.serve))
	c.opener = nodesource.NewOpener(c.open, slog.New(slog.DiscardHandler))
	c.opener.EnableCoreAccess(nodesource.CoreAccess{Fetch: c.fetch, Mode: func() string { return nodesource.ModeCore }})
	t.Cleanup(func() {
		_ = c.opener.Close()
		c.server.Close()
	})
	return c
}

// Open opens the file as a core-access source; its Input is a relay URL. It
// closes with the test.
func (c *Core) Open(t testing.TB) nodesource.Source {
	t.Helper()
	source, err := c.opener.Open(context.Background(), "test-grant")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source
}

func (c *Core) open(context.Context, string) (*nodesource.OpenResponse, error) {
	return &nodesource.OpenResponse{Grant: "test-grant", Path: c.Path, Size: c.size, ModTimeUnixNano: c.mtimeNS, ETag: c.etag}, nil
}

func (c *Core) fetch(ctx context.Context, read nodesource.ReadRequest) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.server.URL+nodesource.PathSources, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(nodesource.HeaderSourceGrant, read.Grant)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", read.Offset, read.Offset+read.Length-1))
	req.Header.Set("If-Match", read.ETag)
	req.Header.Set("Accept-Encoding", "identity")
	return c.server.Client().Do(req)
}

func (c *Core) serve(w http.ResponseWriter, r *http.Request) {
	c.Requests.Add(1)
	f, err := os.Open(c.Path)
	if err != nil {
		http.Error(w, "gone", http.StatusGone)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() != c.size || info.ModTime().UnixNano() != c.mtimeNS {
		http.Error(w, "changed", http.StatusPreconditionFailed)
		return
	}
	w.Header().Set("ETag", c.etag)
	http.ServeContent(w, r, "", info.ModTime(), io.ReadSeeker(f))
}
