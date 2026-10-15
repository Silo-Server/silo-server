package nodecontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodecontrol/nodecontroltest"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

const sourceSecret = "source-grant-test-secret"

// sourceCatalog is the media file and theme catalog an open checks.
type sourceCatalog struct {
	mu     sync.Mutex
	files  map[int]string
	themes map[int64]string
	err    error
}

func (c *sourceCatalog) IsActiveFile(_ context.Context, id int, path string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.files[id] == path, c.err
}

func (c *sourceCatalog) IsActiveTheme(_ context.Context, id int64, path string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.themes[id] == path, c.err
}

type sourceFixture struct {
	grants  *nodesource.Grants
	catalog *sourceCatalog
	coreURL string
	key     string
	nodeID  int
	path    string
	data    []byte
}

func newSourceFixture(t *testing.T, nodeID int) *sourceFixture {
	t.Helper()
	f := &sourceFixture{
		grants:  nodesource.NewGrants(func() string { return sourceSecret }),
		catalog: &sourceCatalog{files: map[int]string{}, themes: map[int64]string{}},
		nodeID:  nodeID,
		data:    bytes.Repeat([]byte("0123456789"), 10_000),
	}
	f.path = filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(f.path, f.data, 0o600); err != nil {
		t.Fatal(err)
	}
	f.catalog.files[42] = f.path
	f.coreURL, f.key = nodecontroltest.SourceCore(t, nodeID, nodecontrol.Options{Grants: f.grants, Files: f.catalog, Themes: f.catalog})
	return f
}

func (f *sourceFixture) grant(t *testing.T, nodeID int) string {
	t.Helper()
	grant, err := f.grants.ForFile(t.Context(), nodeID, &models.MediaFile{ID: 42, FilePath: f.path})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

// sourceAnswer is a core answer with its body read.
type sourceAnswer struct {
	StatusCode    int
	Header        http.Header
	ContentLength int64
}

// do sends a request signed by the fixture's node and returns the answer and
// its body.
func (f *sourceFixture) do(t *testing.T, method, path string, body []byte, header http.Header) (sourceAnswer, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.coreURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		req.Body = http.NoBody
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if err := nodeauth.SignToCore(req, f.coreURL, f.nodeID, f.key); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return sourceAnswer{StatusCode: resp.StatusCode, Header: resp.Header, ContentLength: resp.ContentLength}, read
}

func (f *sourceFixture) open(t *testing.T, grant string) (int, nodesource.OpenResponse) {
	t.Helper()
	body, _ := json.Marshal(nodesource.OpenRequest{Grant: grant})
	resp, answer := f.do(t, http.MethodPost, nodesource.PathOpen, body, nil)
	var opened nodesource.OpenResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(answer, &opened); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, opened
}

func TestSourceOpenExchangesAGrantForTheApprovedSource(t *testing.T) {
	f := newSourceFixture(t, 7)
	grant := f.grant(t, 7)
	status, opened := f.open(t, grant)
	if status != http.StatusOK {
		t.Fatalf("open = %d", status)
	}
	original, _ := f.grants.Verify(grant)
	renewed, err := f.grants.Verify(opened.Grant)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Path != f.path || opened.Size != int64(len(f.data)) || opened.Mtime != original.Mtime || opened.ETag != original.ETag() {
		t.Fatalf("opened = %+v", opened)
	}
	if renewed.Revision() != original.Revision() || renewed.Node != 7 || renewed.Path != original.Path || renewed.Exp < original.Exp {
		t.Fatalf("renewed %+v from %+v", renewed, original)
	}
	// Renewing with the renewed grant keeps the same revision too.
	if status, again := f.open(t, opened.Grant); status != http.StatusOK || again.ETag != opened.ETag {
		t.Fatalf("second renewal = %d %+v", status, again)
	}
}

func TestSourceOpenRefusals(t *testing.T) {
	for name, test := range map[string]struct {
		setup  func(t *testing.T, f *sourceFixture) string
		status int
	}{
		"another node's grant": {func(t *testing.T, f *sourceFixture) string { return f.grant(t, 8) }, http.StatusForbidden},
		"tampered grant":       {func(t *testing.T, f *sourceFixture) string { return f.grant(t, 7) + "x" }, http.StatusForbidden},
		"another key": {func(t *testing.T, f *sourceFixture) string {
			other, _ := nodesource.NewGrants(func() string { return "other" }).ForFile(t.Context(), 7, &models.MediaFile{ID: 42, FilePath: f.path})
			return other
		}, http.StatusForbidden},
		"missing file": {func(t *testing.T, f *sourceFixture) string {
			grant := f.grant(t, 7)
			if err := os.Remove(f.path); err != nil {
				t.Fatal(err)
			}
			return grant
		}, http.StatusGone},
		"catalog moved the file": {func(t *testing.T, f *sourceFixture) string {
			grant := f.grant(t, 7)
			f.catalog.mu.Lock()
			f.catalog.files[42] = "/media/elsewhere.mkv"
			f.catalog.mu.Unlock()
			return grant
		}, http.StatusGone},
		"file left the catalog": {func(t *testing.T, f *sourceFixture) string {
			grant := f.grant(t, 7)
			f.catalog.mu.Lock()
			delete(f.catalog.files, 42)
			f.catalog.mu.Unlock()
			return grant
		}, http.StatusGone},
		"size changed": {func(t *testing.T, f *sourceFixture) string {
			grant := f.grant(t, 7)
			if err := os.WriteFile(f.path, append(f.data, 'x'), 0o600); err != nil {
				t.Fatal(err)
			}
			return grant
		}, http.StatusPreconditionFailed},
		"modification time changed": {func(t *testing.T, f *sourceFixture) string {
			grant := f.grant(t, 7)
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(f.path, later, later); err != nil {
				t.Fatal(err)
			}
			return grant
		}, http.StatusPreconditionFailed},
		"catalog unavailable": {func(t *testing.T, f *sourceFixture) string {
			f.catalog.mu.Lock()
			f.catalog.err = errors.New("db down")
			f.catalog.mu.Unlock()
			return f.grant(t, 7)
		}, http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSourceFixture(t, 7)
			if status, _ := f.open(t, test.setup(t, f)); status != test.status {
				t.Fatalf("open = %d, want %d", status, test.status)
			}
		})
	}
}

func TestSourceOpenChecksThemesAgainstTheirCatalog(t *testing.T) {
	f := newSourceFixture(t, 7)
	theme := filepath.Join(t.TempDir(), "theme.mp3")
	if err := os.WriteFile(theme, []byte("theme audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.catalog.themes[9] = theme
	grant, err := f.grants.ForTheme(t.Context(), 7, 9, theme)
	if err != nil {
		t.Fatal(err)
	}
	if status, opened := f.open(t, grant); status != http.StatusOK || opened.Path != theme || opened.ETag[:7] != `"theme-` {
		t.Fatalf("theme open = %d %+v", status, opened)
	}
	if err := os.WriteFile(theme, []byte("another theme audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.open(t, grant); status != http.StatusPreconditionFailed {
		t.Fatalf("changed theme open = %d, want 412", status)
	}
	grant, _ = f.grants.ForTheme(t.Context(), 7, 9, theme)
	f.catalog.mu.Lock()
	delete(f.catalog.themes, 9)
	f.catalog.mu.Unlock()
	if status, _ := f.open(t, grant); status != http.StatusGone {
		t.Fatalf("theme left the catalog: open = %d, want 410", status)
	}
}

func TestSourceServesTheGrantedRevisionWithRanges(t *testing.T) {
	f := newSourceFixture(t, 7)
	grant := f.grant(t, 7)
	claims, _ := f.grants.Verify(grant)
	etag := claims.ETag()
	size := int64(len(f.data))
	read := func(t *testing.T, method string, header http.Header) (sourceAnswer, []byte) {
		t.Helper()
		return f.do(t, method, nodesource.PathSources, nil, withGrant(header, grant))
	}
	for _, test := range []struct {
		name         string
		header       http.Header
		status       int
		start, end   int64
		contentRange string
	}{
		{"whole", nil, http.StatusOK, 0, size, ""},
		{"range", http.Header{"Range": {"bytes=10-19"}, "If-Match": {etag}}, http.StatusPartialContent, 10, 20, fmt.Sprintf("bytes 10-19/%d", size)},
		{"suffix", http.Header{"Range": {"bytes=-7"}}, http.StatusPartialContent, size - 7, size, fmt.Sprintf("bytes %d-%d/%d", size-7, size-1, size)},
		// Conditions other than If-Match never turn the answer into a
		// different one.
		{"if-none-match", http.Header{"If-None-Match": {etag}}, http.StatusOK, 0, size, ""},
		{"stale if-range", http.Header{"Range": {"bytes=10-19"}, "If-Range": {`"other"`}}, http.StatusPartialContent, 10, 20, fmt.Sprintf("bytes 10-19/%d", size)},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp, body := read(t, http.MethodGet, test.header)
			if resp.StatusCode != test.status || !bytes.Equal(body, f.data[test.start:test.end]) ||
				resp.Header.Get("ETag") != etag || resp.Header.Get("Content-Range") != test.contentRange ||
				resp.ContentLength != test.end-test.start || resp.Header.Get("Content-Encoding") != "" {
				t.Fatalf("status %d, %d bytes, headers %v", resp.StatusCode, len(body), resp.Header)
			}
		})
	}
	if resp, _ := read(t, http.MethodGet, http.Header{"Range": {fmt.Sprintf("bytes=%d-", size)}}); resp.StatusCode != http.StatusRequestedRangeNotSatisfiable ||
		resp.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", size) {
		t.Fatalf("416: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, body := read(t, http.MethodHead, nil); resp.StatusCode != http.StatusOK || len(body) != 0 || resp.ContentLength != size || resp.Header.Get("ETag") != etag {
		t.Fatalf("HEAD: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := read(t, http.MethodGet, http.Header{"If-Match": {`"file-42-1-1"`}}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("other If-Match: %d", resp.StatusCode)
	}

	other := f.grant(t, 8)
	if resp, _ := f.do(t, http.MethodGet, nodesource.PathSources, nil, withGrant(nil, other)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another node's grant: %d", resp.StatusCode)
	}
	if resp, _ := f.do(t, http.MethodGet, nodesource.PathSources, nil, withGrant(nil, grant+"x")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered grant: %d", resp.StatusCode)
	}
	if resp, _ := f.do(t, http.MethodGet, nodesource.PathSources, nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no grant: %d", resp.StatusCode)
	}

	replacement := filepath.Join(t.TempDir(), "replacement.mkv")
	if err := os.WriteFile(replacement, append(f.data, f.data...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, f.path); err != nil {
		t.Fatal(err)
	}
	if resp, _ := read(t, http.MethodGet, nil); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("replaced file: %d", resp.StatusCode)
	}
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	if resp, _ := read(t, http.MethodGet, nil); resp.StatusCode != http.StatusGone {
		t.Fatalf("removed file: %d", resp.StatusCode)
	}

	unsigned, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, f.coreURL+nodesource.PathSources, nil)
	unsigned.Header.Set(nodesource.HeaderSourceGrant, grant)
	resp, err := http.DefaultClient.Do(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned read: %d", resp.StatusCode)
	}
}

// withGrant is header with the source grant set.
func withGrant(header http.Header, grant string) http.Header {
	header = header.Clone()
	if header == nil {
		header = http.Header{}
	}
	header.Set(nodesource.HeaderSourceGrant, grant)
	return header
}

// The revision is precise to the second: a mount that reports the
// modification time more finely or more coarsely (sshfs keeps whole seconds,
// SMB 100 ns) than the one the grant was minted on sees the same revision.
func TestSourceRevisionIsPreciseToTheSecond(t *testing.T) {
	f := newSourceFixture(t, 7)
	minted := time.Date(2026, 5, 4, 3, 2, 1, 123_456_789, time.UTC)
	if err := os.Chtimes(f.path, minted, minted); err != nil {
		t.Fatal(err)
	}
	grant := f.grant(t, 7)
	claims, err := f.grants.Verify(grant)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf(`"file-42-%d-%d"`, len(f.data), minted.Unix()); claims.Mtime != minted.Unix() || claims.ETag() != want {
		t.Fatalf("grant mtime %d, etag %s; want %d, %s", claims.Mtime, claims.ETag(), minted.Unix(), want)
	}
	// The same second as another mount reports it.
	coarse := minted.Truncate(time.Second)
	if err := os.Chtimes(f.path, coarse, coarse); err != nil {
		t.Fatal(err)
	}
	status, opened := f.open(t, grant)
	if status != http.StatusOK || opened.Mtime != minted.Unix() || opened.ETag != claims.ETag() {
		t.Fatalf("open at another precision = %d %+v", status, opened)
	}
	if resp, body := f.do(t, http.MethodGet, nodesource.PathSources, nil, withGrant(http.Header{"Range": {"bytes=0-9"}}, opened.Grant)); resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, f.data[:10]) {
		t.Fatalf("read at another precision = %d", resp.StatusCode)
	}
	// Another second is another revision.
	later := minted.Add(time.Second)
	if err := os.Chtimes(f.path, later, later); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.open(t, grant); status != http.StatusPreconditionFailed {
		t.Fatalf("open a second later = %d, want 412", status)
	}
	if resp, _ := f.do(t, http.MethodGet, nodesource.PathSources, nil, withGrant(nil, grant)); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("read a second later = %d, want 412", resp.StatusCode)
	}
}

// Resolve checks the catalog before it grants a recipe's source, as open
// does: a recipe outlives the file it was made for.
func TestResolveRefusesASourceTheCatalogNoLongerHas(t *testing.T) {
	grants := nodesource.NewGrants(func() string { return sourceSecret })
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("movie"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := &sourceCatalog{files: map[int]string{42: path}, themes: map[int64]string{}}
	client := nodecontroltest.Resolver(t, 3, nodecontrol.Options{
		Nodes:        nodeURLs{urls: map[int]string{3: resolveNodeURL}},
		StreamSecret: func() string { return resolveSecret },
		Recipes:      &recipeMap{cards: map[string]playback.RecipeCard{}},
		Grants:       grants,
		Files:        catalog,
		Themes:       catalog,
	})
	card := nativeCard("viewer-1", "viewer-1-plan")
	card.InputPath = path
	resolve := func() error {
		_, err := client.Resolve(t.Context(), nodecontrol.ResolveRequest{
			TransportID: "viewer-1-plan", Purpose: nodecontrol.ResolvePurposeHLS, StreamToken: signClaims(t, card.ToClaims(), resolveSecret),
		})
		return err
	}
	if err := resolve(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(){
		"moved":   func() { catalog.files[42] = "/media/elsewhere.mkv" },
		"removed": func() { delete(catalog.files, 42) },
	} {
		catalog.mu.Lock()
		change()
		catalog.mu.Unlock()
		if err := resolve(); nodecontrol.Status(err) != http.StatusGone || nodecontrol.Reason(err) != nodecontrol.ResolveSourceChanged {
			t.Fatalf("%s: resolve = %v, want 410 %s", name, err, nodecontrol.ResolveSourceChanged)
		}
	}
	catalog.mu.Lock()
	catalog.files[42], catalog.err = path, errors.New("db down")
	catalog.mu.Unlock()
	if err := resolve(); nodecontrol.Status(err) != http.StatusServiceUnavailable {
		t.Fatalf("catalog unavailable: resolve = %v, want 503", err)
	}
}

// Resolve hands the node a grant for the recipe's own source.
func TestResolveMintsTheRecipeSourceGrant(t *testing.T) {
	grants := nodesource.NewGrants(func() string { return sourceSecret })
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("movie"), 0o600); err != nil {
		t.Fatal(err)
	}
	recipes := &recipeMap{cards: map[string]playback.RecipeCard{}}
	client := nodecontroltest.Resolver(t, 3, nodecontrol.Options{
		Nodes:        nodeURLs{urls: map[int]string{3: resolveNodeURL}},
		StreamSecret: func() string { return resolveSecret },
		Recipes:      recipes,
		Grants:       grants,
	})
	card := nativeCard("viewer-1", "viewer-1-plan")
	card.InputPath = path
	resolution, err := client.Resolve(t.Context(), nodecontrol.ResolveRequest{
		TransportID: "viewer-1-plan", Purpose: nodecontrol.ResolvePurposeHLS, StreamToken: signClaims(t, card.ToClaims(), resolveSecret),
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := grants.Verify(resolution.SourceGrant)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Node != 3 || claims.Kind != nodesource.KindFile || claims.ID != 42 || claims.Path != path {
		t.Fatalf("grant = %+v", claims)
	}

	// A recipe whose source is gone resolves nothing.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err = client.Resolve(t.Context(), nodecontrol.ResolveRequest{
		TransportID: "viewer-1-plan", Purpose: nodecontrol.ResolvePurposeHLS, StreamToken: signClaims(t, card.ToClaims(), resolveSecret),
	})
	if nodecontrol.Status(err) != http.StatusGone || nodecontrol.Reason(err) != nodecontrol.ResolveSourceChanged {
		t.Fatalf("gone source: %v", err)
	}
}

func TestResolveMintsAThemeGrant(t *testing.T) {
	grants := nodesource.NewGrants(func() string { return sourceSecret })
	recipes := &recipeMap{cards: map[string]playback.RecipeCard{}}
	client := nodecontroltest.Resolver(t, 11, nodecontrol.Options{
		Nodes:        nodeURLs{urls: map[int]string{11: resolveNodeURL}},
		StreamSecret: func() string { return resolveSecret },
		Recipes:      recipes,
		Grants:       grants,
	})
	path := filepath.Join(t.TempDir(), "theme.mp3")
	if err := os.WriteFile(path, []byte("theme-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	claims := remuxClaims(11, path)
	claims.TranscodeTransportID, claims.PlayMethod = "theme-transport", streamtoken.PlayMethodThemeAAC
	claims.TranscodeAudio, claims.AudioOnly, claims.TargetCodecAudio = true, true, themesongs.CodecAAC
	claims.ThemeID, claims.ThemeSize, claims.ThemeModifiedUnixNano = 5, info.Size(), info.ModTime().Truncate(time.Microsecond).UnixNano()
	recipes.cards["theme-transport"] = playback.RecipeCardFromClaims(&claims)
	resolution, err := client.Resolve(t.Context(), nodecontrol.ResolveRequest{
		TransportID: "theme-transport", Purpose: nodecontrol.ResolvePurposeRemux, StreamToken: signClaims(t, claims, resolveSecret),
	})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Verify(resolution.SourceGrant)
	if err != nil || grant.Kind != nodesource.KindTheme || grant.ID != 5 || grant.Node != 11 || grant.Path != path {
		t.Fatalf("theme grant = %+v, %v", grant, err)
	}
}

// Every answer a source read gives is in its x-silo-worker-protocols entry,
// with the media type the handler really sends.
func TestSourceReadAnswersMatchTheProtocol(t *testing.T) {
	f := newSourceFixture(t, 7)
	grant := f.grant(t, 7)
	described := map[string]map[string]*huma.Response{}
	for _, op := range nodecontrol.Protocol(huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)) {
		if op.Path == nodesource.PathSources+"/" {
			described[op.Method] = op.Responses
		}
	}
	size := len(f.data)
	for _, test := range []struct {
		method string
		header http.Header
		status int
	}{
		{http.MethodGet, nil, http.StatusOK},
		{http.MethodGet, http.Header{"Range": {"bytes=10-19"}}, http.StatusPartialContent},
		{http.MethodHead, nil, http.StatusOK},
		{http.MethodHead, http.Header{"Range": {"bytes=10-19"}}, http.StatusPartialContent},
		{http.MethodGet, http.Header{"Range": {fmt.Sprintf("bytes=%d-", size)}}, http.StatusRequestedRangeNotSatisfiable},
		{http.MethodHead, http.Header{"If-Match": {`"other"`}}, http.StatusPreconditionFailed},
	} {
		resp, _ := f.do(t, test.method, nodesource.PathSources, nil, withGrant(test.header, grant))
		if resp.StatusCode != test.status {
			t.Fatalf("%s %v: status %d, want %d", test.method, test.header, resp.StatusCode, test.status)
		}
		response := described[test.method][strconv.Itoa(resp.StatusCode)]
		if response == nil {
			t.Fatalf("%s answers %d, which its protocol entry does not describe", test.method, resp.StatusCode)
		}
		if resp.StatusCode < http.StatusMultipleChoices {
			mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			if mediaType != "video/x-matroska" || response.Content[mediaType] == nil {
				t.Fatalf("%s %d sends %q, described as %v", test.method, resp.StatusCode, mediaType, slices.Collect(maps.Keys(response.Content)))
			}
		}
	}
}
