package catalogseed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// RemoteBundleTimeout bounds one download of a catalog seed from a remote URL.
const RemoteBundleTimeout = 10 * time.Minute

// MaxRemoteBundleBytes caps the compressed size of a catalog seed downloaded
// from a remote URL. Embeddings dominate an export: each title's 1536-value
// embedding is about 7 KB once gzipped, so a 100,000-title library with its
// episodes and files stays well under 1 GiB. The importer holds the whole
// bundle and its decompressed JSON in memory, so a larger download would
// exhaust memory on most hosts before the import could finish.
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
// admin import. Only admins name the URL, so it may point at a host on the
// server's local network, like an import source an admin configures.
func DownloadRemoteBundle(ctx context.Context, remoteURL string) ([]byte, error) {
	return downloadRemoteBundle(ctx, remoteBundleClient, remoteURL, MaxRemoteBundleBytes)
}

func downloadRemoteBundle(ctx context.Context, client *http.Client, remoteURL string, maxBytes int64) ([]byte, error) {
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

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading remote catalog seed: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrRemoteBundleTooLarge
	}
	return data, nil
}
