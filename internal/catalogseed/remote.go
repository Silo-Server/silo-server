package catalogseed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// RemoteBundleTimeout bounds one download of a catalog seed from a remote URL.
const RemoteBundleTimeout = 10 * time.Minute

// MaxRemoteBundleBytes caps the compressed size of a catalog seed downloaded
// from a remote URL. Embeddings dominate an export: each title's 1536-value
// embedding is about 7 KB once gzipped, so a 100,000-title library with its
// episodes and files stays well under 1 GiB. The download goes to a temporary
// file, but the importer holds the decoded bundle in memory, so a larger
// bundle would exhaust memory on most hosts before the import could finish.
const MaxRemoteBundleBytes int64 = 2 << 30

var (
	// ErrInvalidRemoteURL reports a remote URL that is not an http(s) link to
	// a .json.gz file.
	ErrInvalidRemoteURL = errors.New("remote URL must point to an http(s) .json.gz file")
	// ErrRemoteAddressNotAllowed reports a remote URL, or a redirect from it,
	// that resolves to an address the server may not reach, such as a
	// link-local or cloud metadata address.
	ErrRemoteAddressNotAllowed = errors.New("remote URL points to an address this server may not reach")
	// ErrRemoteBundleTooLarge reports a remote catalog seed larger than
	// MaxRemoteBundleBytes.
	ErrRemoteBundleTooLarge = fmt.Errorf("remote catalog seed is larger than the %d GiB download limit", MaxRemoteBundleBytes>>30)
)

// remoteBundleClient checks every address it dials, redirect hops included.
// Downloads mark their context with netguard.WithPrivateAccess, so the
// server's local network is reachable and blocked ranges are not. See
// docs/architecture/outbound-address-guard.md.
var remoteBundleClient = netguard.NewClient(RemoteBundleTimeout)

// DownloadRemoteBundle fetches a catalog seed bundle from remoteURL for an
// admin import into a temporary file, and returns that file open at its
// start; closing it removes the file. Only admins name the URL, so it may
// point at a host on the server's local network, like an import source an
// admin configures.
func DownloadRemoteBundle(ctx context.Context, remoteURL string) (io.ReadCloser, error) {
	return downloadRemoteBundle(ctx, remoteBundleClient, remoteURL, MaxRemoteBundleBytes)
}

func downloadRemoteBundle(ctx context.Context, client *http.Client, remoteURL string, maxBytes int64) (io.ReadCloser, error) {
	parsed, err := url.Parse(remoteURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		!strings.HasSuffix(strings.ToLower(parsed.Path), ".json.gz") {
		return nil, ErrInvalidRemoteURL
	}

	req, err := http.NewRequestWithContext(netguard.WithPrivateAccess(ctx), http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building remote catalog seed request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrPrivateDestination) || errors.Is(err, netguard.ErrBlockedDestination) {
			return nil, fmt.Errorf("%w: %w", ErrRemoteAddressNotAllowed, err)
		}
		return nil, fmt.Errorf("downloading remote catalog seed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading remote catalog seed: unexpected status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, ErrRemoteBundleTooLarge
	}

	file, err := os.CreateTemp("", "silo-catalog-seed-*.json.gz")
	if err != nil {
		return nil, fmt.Errorf("creating temp file for remote catalog seed: %w", err)
	}
	download := &removeOnClose{File: file}
	written, err := io.Copy(file, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		_ = download.Close()
		return nil, fmt.Errorf("reading remote catalog seed: %w", err)
	}
	if written > maxBytes {
		_ = download.Close()
		return nil, ErrRemoteBundleTooLarge
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = download.Close()
		return nil, fmt.Errorf("rewinding remote catalog seed: %w", err)
	}
	return download, nil
}

// removeOnClose is a temporary file that Close also removes.
type removeOnClose struct {
	*os.File
}

func (f *removeOnClose) Close() error {
	closeErr := f.File.Close()
	if err := os.Remove(f.Name()); err != nil {
		return err
	}
	return closeErr
}
