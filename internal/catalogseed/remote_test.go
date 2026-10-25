package catalogseed

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// An admin may import from a host on the server's own network; the
// httptest server on loopback stands in for one.
func TestDownloadRemoteBundleFetchesFromLocalNetwork(t *testing.T) {
	want := []byte("bundle bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(want)
	}))
	defer server.Close()

	got, err := DownloadRemoteBundle(context.Background(), server.URL+"/seed.json.gz")
	if err != nil {
		t.Fatalf("DownloadRemoteBundle: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("DownloadRemoteBundle = %q, want %q", got, want)
	}
}

func TestDownloadRemoteBundleRefusesMetadataAddress(t *testing.T) {
	_, err := DownloadRemoteBundle(context.Background(), "http://169.254.169.254/latest/seed.json.gz")
	if !errors.Is(err, ErrRemoteAddressNotAllowed) || !errors.Is(err, netguard.ErrBlockedDestination) {
		t.Fatalf("metadata address error = %v, want ErrRemoteAddressNotAllowed wrapping ErrBlockedDestination", err)
	}
}

func TestDownloadRemoteBundleRefusesRedirectToMetadataAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/seed.json.gz", http.StatusFound)
	}))
	defer server.Close()

	_, err := DownloadRemoteBundle(context.Background(), server.URL+"/seed.json.gz")
	if !errors.Is(err, ErrRemoteAddressNotAllowed) {
		t.Fatalf("redirect to metadata error = %v, want ErrRemoteAddressNotAllowed", err)
	}
}

func TestDownloadRemoteBundleRejectsInvalidURL(t *testing.T) {
	for _, rawURL := range []string{"ftp://example.com/seed.json.gz", "https://example.com/seed.json", "https:///seed.json.gz", "::"} {
		if _, err := DownloadRemoteBundle(context.Background(), rawURL); !errors.Is(err, ErrInvalidRemoteURL) {
			t.Errorf("DownloadRemoteBundle(%q) = %v, want ErrInvalidRemoteURL", rawURL, err)
		}
	}
}

// A body over the cap is refused whether the server declares its length or
// streams it without one.
func TestDownloadRemoteBundleRejectsOversizeBody(t *testing.T) {
	const maxBytes = 1 << 10
	body := bytes.Repeat([]byte("x"), maxBytes+1)
	for name, declareLength := range map[string]bool{"content-length": true, "chunked": false} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if declareLength {
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				} else {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()

			_, err := downloadRemoteBundle(context.Background(), remoteBundleClient, server.URL+"/seed.json.gz", maxBytes)
			if !errors.Is(err, ErrRemoteBundleTooLarge) {
				t.Fatalf("oversize body error = %v, want ErrRemoteBundleTooLarge", err)
			}
		})
	}
}

func TestDownloadRemoteBundleAcceptsBodyAtCap(t *testing.T) {
	const maxBytes = 1 << 10
	body := bytes.Repeat([]byte("x"), maxBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	got, err := downloadRemoteBundle(context.Background(), remoteBundleClient, server.URL+"/seed.json.gz", maxBytes)
	if err != nil {
		t.Fatalf("body at cap: %v", err)
	}
	if len(got) != maxBytes {
		t.Fatalf("body at cap = %d bytes, want %d", len(got), maxBytes)
	}
}
