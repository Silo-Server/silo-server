package nodecontrol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// fakeCatalog answers the catalog checks. With nil maps every source is
// active; once a map is set, only the ids it lists, at exactly their paths.
type fakeCatalog struct {
	mu     sync.Mutex
	files  map[int]string
	themes map[int64]string
	err    error
}

func (c *fakeCatalog) IsActiveFile(_ context.Context, id int, path string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return false, c.err
	}
	if c.files == nil {
		return true, nil
	}
	listed, ok := c.files[id]
	return ok && listed == path, nil
}

func (c *fakeCatalog) IsActiveTheme(_ context.Context, id int64, path string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return false, c.err
	}
	if c.themes == nil {
		return true, nil
	}
	listed, ok := c.themes[id]
	return ok && listed == path, nil
}

func (c *fakeCatalog) set(change func(*fakeCatalog)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	change(c)
}

// stubGrants mints readable grants without a stat, for resolve tests whose
// recipes name files that do not exist.
type stubGrants struct{}

func (stubGrants) ForFile(_ context.Context, nodeID int, file *models.MediaFile) (string, error) {
	return fmt.Sprintf("grant:%d:file:%d:%s", nodeID, file.ID, file.FilePath), nil
}

func (stubGrants) ForTheme(_ context.Context, nodeID int, themeID int64, path string) (string, error) {
	return fmt.Sprintf("grant:%d:theme:%d:%s", nodeID, themeID, path), nil
}

func (stubGrants) Verify(string) (nodesource.Grant, error) {
	return nodesource.Grant{}, nodesource.ErrInvalidGrant
}

func (stubGrants) Renew(nodesource.Grant) (string, error) { return "", nodesource.ErrNoGrantKey }

// Resolve grants the calling node the recipe's source, and refuses with
// source_changed once the catalog no longer has that exact file.
func TestResolveGrantsTheCallingNodeItsSource(t *testing.T) {
	core := newResolveCore(t, twoNodes, nil)
	token := signClaims(t, nativeCard("viewer-1", "viewer-1-plan").ToClaims(), resolveSecret)

	resolution, status, reason := core.resolve(nodeA, ResolvePurposeHLS, "viewer-1-plan", token)
	if status != http.StatusOK {
		t.Fatalf("resolve = %d %q", status, reason)
	}
	if want := fmt.Sprintf("grant:%d:file:42:/media/movie.mkv", nodeA); resolution.SourceGrant != want {
		t.Fatalf("source grant = %q, want %q", resolution.SourceGrant, want)
	}

	for name, files := range map[string]map[int]string{
		"moved":                   {42: "/media/renamed.mkv"},
		"removed":                 {},
		"another row at the path": {43: "/media/movie.mkv"},
	} {
		core.catalog.set(func(c *fakeCatalog) { c.files = files })
		_, status, reason := core.resolve(nodeA, ResolvePurposeHLS, "viewer-1-plan", token)
		if status != http.StatusGone || reason != ResolveSourceChanged {
			t.Fatalf("%s: resolve = %d %q, want 410 %s", name, status, reason, ResolveSourceChanged)
		}
	}

	core.catalog.set(func(c *fakeCatalog) { c.files, c.err = nil, errors.New("catalog down") })
	if _, status, _ := core.resolve(nodeA, ResolvePurposeHLS, "viewer-1-plan", token); status != http.StatusServiceUnavailable {
		t.Fatalf("catalog down: resolve = %d, want 503", status)
	}
}

// openFixture is the core's sources/open route with real grants over files
// in a temporary library.
type openFixture struct {
	*controlFixture
	grants  *nodesource.Grants
	catalog *fakeCatalog
	keyA    string
	keyB    string
}

func newOpenFixture(t *testing.T) *openFixture {
	t.Helper()
	f := &openFixture{controlFixture: newControlFixture(t), catalog: &fakeCatalog{files: map[int]string{}}}
	f.grants = nodesource.NewGrants(func() string { return "open-test-secret" })
	f.keyA, f.keyB = f.addNode(nodeA, "node-a"), f.addNode(nodeB, "node-b")
	f.server.opts.Grants, f.server.opts.Files, f.server.opts.Themes = f.grants, f.catalog, f.catalog
	return f
}

// library writes a catalog file and returns its path.
func (f *openFixture) library(id int, name, content string) string {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.catalog.set(func(c *fakeCatalog) { c.files[id] = path })
	return path
}

func (f *openFixture) mint(nodeID, fileID int, path string) string {
	f.t.Helper()
	grant, err := f.grants.ForFile(f.t.Context(), nodeID, &models.MediaFile{ID: fileID, FilePath: path})
	if err != nil {
		f.t.Fatal(err)
	}
	return grant
}

func (f *openFixture) open(nodeID int, key, grant string) (int, nodesource.OpenResponse, http.Header) {
	f.t.Helper()
	got := f.serve(nodeID, key, http.MethodPost, PathSourcesOpen, nodesource.OpenRequest{Grant: grant}, nil)
	var answer nodesource.OpenResponse
	if got.Code == http.StatusOK {
		if err := json.Unmarshal(got.Body.Bytes(), &answer); err != nil {
			f.t.Fatal(err)
		}
	}
	return got.Code, answer, got.Header()
}

func TestSourceOpenApprovesAndRenewsTheGrantedRevision(t *testing.T) {
	f := newOpenFixture(t)
	path := f.library(42, "movie.mkv", "movie bytes")
	grant := f.mint(nodeA, 42, path)
	minted, err := f.grants.Verify(grant)
	if err != nil {
		t.Fatal(err)
	}

	status, answer, header := f.open(nodeA, f.keyA, grant)
	if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("open = %d, Cache-Control %q", status, header.Get("Cache-Control"))
	}
	if answer.Path != path || answer.Size != int64(len("movie bytes")) || answer.ModTimeUnixNano != minted.ModTimeUnixNano || answer.ETag != minted.ETag() {
		t.Fatalf("answer = %+v, want the minted revision %+v", answer, minted)
	}
	renewed, err := f.grants.Verify(answer.Grant)
	if err != nil || renewed.Node != nodeA || !renewed.Revision().Same(minted.Revision()) || renewed.Path != path {
		t.Fatalf("renewed grant = %+v %v", renewed, err)
	}

	// Renewal is the same call with the current grant.
	if status, again, _ := f.open(nodeA, f.keyA, answer.Grant); status != http.StatusOK || again.ETag != answer.ETag {
		t.Fatalf("renew = %d %+v", status, again)
	}
}

func TestSourceOpenRefusals(t *testing.T) {
	f := newOpenFixture(t)
	path := f.library(42, "movie.mkv", "movie bytes")
	grant := f.mint(nodeA, 42, path)

	// Another node's grant and a tampered one are refused. (Expiry is
	// Verify's; see the nodesource tests.)
	if status, _, _ := f.open(nodeB, f.keyB, grant); status != http.StatusForbidden {
		t.Fatalf("another node's grant = %d, want 403", status)
	}
	payload, mac, _ := strings.Cut(grant, ".")
	decoded, _ := base64.RawURLEncoding.DecodeString(payload)
	tampered := base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(string(decoded), `"node":3`, `"node":12`, 1))) + "." + mac
	if status, _, _ := f.open(nodeB, f.keyB, tampered); status != http.StatusForbidden {
		t.Fatalf("tampered grant = %d, want 403", status)
	}

	// The catalog moved or dropped the file.
	f.catalog.set(func(c *fakeCatalog) { c.files[42] = path + ".moved" })
	if status, _, _ := f.open(nodeA, f.keyA, grant); status != http.StatusGone {
		t.Fatalf("moved in the catalog = %d, want 410", status)
	}
	f.catalog.set(func(c *fakeCatalog) { c.files[42] = path })

	// A sub-microsecond change of the modification time is the same revision;
	// a larger one, or another size, is not.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	base := info.ModTime().Truncate(time.Microsecond)
	setMtime := func(at time.Time) {
		t.Helper()
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	setMtime(base.Add(100 * time.Nanosecond))
	grant = f.mint(nodeA, 42, path)
	setMtime(base.Add(900 * time.Nanosecond))
	if status, _, _ := f.open(nodeA, f.keyA, grant); status != http.StatusOK {
		t.Fatalf("sub-microsecond mtime change = %d, want 200", status)
	}
	setMtime(base.Add(time.Microsecond + 100*time.Nanosecond))
	if status, _, _ := f.open(nodeA, f.keyA, grant); status != http.StatusPreconditionFailed {
		t.Fatalf("mtime one microsecond later = %d, want 412", status)
	}
	setMtime(base.Add(100 * time.Nanosecond))
	if err := os.WriteFile(path, []byte("replaced movie bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	setMtime(base.Add(100 * time.Nanosecond))
	if status, _, _ := f.open(nodeA, f.keyA, grant); status != http.StatusPreconditionFailed {
		t.Fatalf("another size = %d, want 412", status)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := f.open(nodeA, f.keyA, grant); status != http.StatusGone {
		t.Fatalf("file removed = %d, want 410", status)
	}

	// A file the server could not examine is not gone: the node retries a 503
	// and ends its job on a 410. A symlink loop stands in for an I/O error.
	if err := os.Symlink(filepath.Base(path), path); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := f.open(nodeA, f.keyA, grant); status != http.StatusServiceUnavailable {
		t.Fatalf("unexaminable file = %d, want 503", status)
	}

	f.server.opts.Grants = nodesource.NewGrants(func() string { return "" })
	if status, _, _ := f.open(nodeA, f.keyA, grant); status != http.StatusServiceUnavailable {
		t.Fatalf("no grant key = %d, want 503", status)
	}
}
