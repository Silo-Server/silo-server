package pluginsig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/Silo-Server/silo-server/internal/privatedir"
)

// tufCacheValidityDays is how long a refreshed TUF cache is used before the
// next verification refreshes it again. A refresh that fails falls back to
// the cache until its metadata expires.
const tufCacheValidityDays = 1

// tufFetchTimeout bounds one TUF metadata or target download. The files are
// a few kilobytes.
const tufFetchTimeout = 30 * time.Second

// TUFTrustRootOptions configures a TUFTrustRoot.
type TUFTrustRootOptions struct {
	// CacheDir holds the TUF metadata and the fetched trusted root. It is
	// created with mode 0700, and an existing one another local account could
	// control is refused, as for the plugin cache root.
	CacheDir string
	// Transport carries the TUF fetches. Requests carry no private-access
	// mark, so a netguard transport limits them to public addresses.
	Transport http.RoundTripper
	// RepositoryURL is the TUF mirror; empty means Sigstore's public-good
	// mirror.
	RepositoryURL string

	// embeddedRoot replaces sigstore-go's embedded root.json in tests, which
	// serve their own TUF repository.
	embeddedRoot []byte
}

// TUFTrustRoot loads Sigstore's public-good trusted root through TUF. Nothing
// is fetched until the first verification, so a server without network
// access still starts, and each node keeps its own cache. There is no bundled
// trusted_root.json to fall back on: with TUF unreachable and the cached
// metadata expired, TrustedMaterial fails.
type TUFTrustRoot struct {
	opts TUFTrustRootOptions
	// mu serializes access to the cache directory.
	mu sync.Mutex
}

// NewTUFTrustRoot returns a TUFTrustRoot for opts.
func NewTUFTrustRoot(opts TUFTrustRootOptions) *TUFTrustRoot {
	if opts.RepositoryURL == "" {
		opts.RepositoryURL = tuf.DefaultMirror
	}
	if opts.embeddedRoot == nil {
		opts.embeddedRoot = tuf.DefaultRoot()
	}
	return &TUFTrustRoot{opts: opts}
}

// TrustedMaterial returns the current trusted root. It refreshes TUF once the
// cache is older than a day; when that refresh fails it uses the cached
// metadata while it is unexpired, and otherwise fails. Fetches stop when ctx
// ends.
func (t *TUFTrustRoot) TrustedMaterial(ctx context.Context) (root.TrustedMaterial, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Another verification may have held the lock until ctx ended.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.opts.CacheDir == "" {
		return nil, errors.New("no TUF cache directory configured")
	}
	if err := privatedir.Ensure(t.opts.CacheDir); err != nil {
		return nil, fmt.Errorf("TUF cache dir %w", err)
	}
	trusted, err := t.load(ctx, false)
	if err == nil {
		return trusted, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	cached, cacheErr := t.load(ctx, true)
	if cacheErr != nil {
		return nil, errors.Join(err, cacheErr)
	}
	return cached, nil
}

// load builds a TUF client and reads the trusted root target. forceCache uses
// unexpired cached metadata without contacting the mirror; once that
// metadata has expired, go-tuf refreshes anyway and fails when it can't.
func (t *TUFTrustRoot) load(ctx context.Context, forceCache bool) (*root.TrustedRoot, error) {
	opts := &tuf.Options{
		Root:              t.anchorRoot(),
		CachePath:         t.opts.CacheDir,
		RepositoryBaseURL: t.opts.RepositoryURL,
		CacheValidity:     tufCacheValidityDays,
		ForceCache:        forceCache,
		Fetcher: &contextFetcher{ctx: ctx, client: &http.Client{
			Transport: t.opts.Transport,
			Timeout:   tufFetchTimeout,
		}},
	}
	client, err := tuf.New(opts)
	if err != nil {
		return nil, fmt.Errorf("load TUF metadata: %w", err)
	}
	trusted, err := root.GetTrustedRoot(client)
	if err != nil {
		return nil, fmt.Errorf("load trusted root: %w", err)
	}
	return trusted, nil
}

// anchorRoot is the root.json TUF starts from: the cached one when it is at
// least as new as the embedded one, otherwise the embedded one.
//
// go-tuf writes the root it is given over the cached root.json, so always
// passing the embedded root would throw away every rotation the cache had
// verified since. Once the embedded root expires, every load then needs the
// mirror, and the cache never serves offline. Anchoring on the cached root
// keeps TUF's checks: go-tuf requires it to be self-signed, persisted it only
// after verifying it against the root before it, refuses it once expired, and
// walks newer root versions from it on refresh. The cache directory is
// private to this process's user, as the plugin cache root is.
func (t *TUFTrustRoot) anchorRoot() []byte {
	embedded := t.opts.embeddedRoot
	cachedPath := filepath.Join(t.opts.CacheDir, tuf.URLToPath(t.opts.RepositoryURL), "root.json")
	cached, err := readFileLimited(cachedPath, 512<<10)
	if err != nil {
		return embedded
	}
	cachedRoot, err := metadata.Root().FromBytes(cached)
	if err != nil {
		return embedded
	}
	embeddedRoot, err := metadata.Root().FromBytes(embedded)
	if err != nil || cachedRoot.Signed.Version >= embeddedRoot.Signed.Version {
		return cached
	}
	return embedded
}

func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // a fixed name under the private TUF cache dir
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxBytes)
	}
	return data, nil
}

// contextFetcher is go-tuf's fetcher with the caller's context on every
// request, so a canceled verification stops waiting on the mirror. go-tuf's
// own fetcher builds requests without one. A fetcher is built per load,
// since the Fetcher interface carries no context.
type contextFetcher struct {
	ctx    context.Context //nolint:containedctx // go-tuf's Fetcher interface takes no context; one fetcher is built per load
	client *http.Client
}

func (f *contextFetcher) DownloadFile(urlPath string, maxLength int64, _ time.Duration) ([]byte, error) {
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, urlPath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// go-tuf reads a 404 for the next root version as "no newer root".
		return nil, &metadata.ErrDownloadHTTP{StatusCode: resp.StatusCode, URL: urlPath}
	}
	if header := resp.Header.Get("Content-Length"); header != "" {
		if length, err := strconv.ParseInt(header, 10, 64); err == nil && length > maxLength {
			return nil, &metadata.ErrDownloadLengthMismatch{Msg: fmt.Sprintf("download failed for %s, length %d is larger than expected %d", urlPath, length, maxLength)}
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLength+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxLength {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: fmt.Sprintf("download failed for %s, length %d is larger than expected %d", urlPath, len(data), maxLength)}
	}
	return data, nil
}
