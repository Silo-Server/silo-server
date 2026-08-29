// Package artworksource owns request-time source retrieval shared by durable
// materialization and resilient fallback delivery.
package artworksource

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"golang.org/x/time/rate"

	"github.com/Silo-Server/silo-server/internal/imageutil"
)

const (
	MaxDownloadBytes = 25 * 1024 * 1024
	DownloadTimeout  = 30 * time.Second
)

type VerifiedImage struct {
	Data      []byte
	MediaType string
}

func Fetch(ctx context.Context, client *http.Client, enforcePublic bool, rawURL string) ([]byte, error) {
	return FetchLimited(ctx, client, enforcePublic, rawURL, nil)
}

// FetchLimited adds a process-local bandwidth ceiling without weakening the
// SSRF, redirect, status, or object-size checks shared with materialization.
func FetchLimited(ctx context.Context, client *http.Client, enforcePublic bool, rawURL string, bandwidth *rate.Limiter) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	if enforcePublic {
		if err := ValidatePublicImageURL(parsed); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, DownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if client == nil {
		client = NewSecureHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	reader := io.LimitReader(resp.Body, MaxDownloadBytes+1)
	if bandwidth != nil {
		reader = &rateLimitedReader{ctx: ctx, reader: reader, limiter: bandwidth}
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(data)) > MaxDownloadBytes {
		return nil, fmt.Errorf("image exceeds %d byte limit", MaxDownloadBytes)
	}
	return data, nil
}

type rateLimitedReader struct {
	ctx     context.Context
	reader  io.Reader
	limiter *rate.Limiter
}

func (r *rateLimitedReader) Read(p []byte) (int, error) {
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	if err := r.limiter.WaitN(r.ctx, len(p)); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func FetchVerified(ctx context.Context, rawURL string) (VerifiedImage, error) {
	return FetchVerifiedLimited(ctx, rawURL, nil)
}

func FetchVerifiedLimited(ctx context.Context, rawURL string, bandwidth *rate.Limiter) (VerifiedImage, error) {
	data, err := FetchLimited(ctx, nil, true, rawURL, bandwidth)
	if err != nil {
		return VerifiedImage{}, err
	}
	mediaType, err := imageutil.ValidateImage(data)
	if err != nil {
		return VerifiedImage{}, err
	}
	return VerifiedImage{Data: data, MediaType: mediaType}, nil
}

func NewSecureHTTPClient() *http.Client {
	transport := &http.Transport{Proxy: nil, DialContext: secureImageDialContext, TLSHandshakeTimeout: 10 * time.Second}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return ValidatePublicImageURL(req.URL)
		},
	}
}

func ValidatePublicImageURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("empty URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL host is required")
	}
	if addr, err := netip.ParseAddr(host); err == nil && !isPublicAddr(addr) {
		return fmt.Errorf("private image host %q is not allowed", host)
	}
	return nil
}

func secureImageDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addr, err := resolvePublicAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: DownloadTimeout}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
}

func resolvePublicAddr(ctx context.Context, host string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if isPublicAddr(addr) {
			return addr, nil
		}
		return netip.Addr{}, fmt.Errorf("private image host %q is not allowed", host)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve image host %q: %w", host, err)
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if ok && isPublicAddr(addr) {
			return addr, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("image host %q did not resolve to a public address", host)
}

func isPublicAddr(addr netip.Addr) bool {
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	return addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() && !addr.IsLinkLocalMulticast() && !addr.IsMulticast() && !addr.IsUnspecified()
}
