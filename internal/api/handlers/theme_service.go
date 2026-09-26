package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

const (
	ThemeFileLimit    = 256 << 10
	ThemeCatalogLimit = 1 << 20
	catalogCacheTTL   = time.Hour
)

// ThemeCatalogResult carries the cache outcome separately from portable bytes.
// Bytes are read-only, including cache hits; transports must not mutate them.
type ThemeCatalogResult struct {
	Body         []byte
	Stale        bool
	CacheControl string
}

// DownloadThemeFile is shared by the frozen byte-preserving v1 transport and
// the typed v2 transport. Initial and redirected requests use the same allowlist.
func (h *ThemeHandler) DownloadThemeFile(ctx context.Context, rawURL string) ([]byte, error) {
	if rawURL == "" {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Missing url parameter")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Invalid URL")
	}
	if config.ValidateThemeRemoteURL(parsed) != nil {
		return nil, apiError(http.StatusForbidden, "host_not_allowed", "Theme downloads are only allowed over HTTPS from approved hosts")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Failed to create request")
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, apiError(http.StatusBadGateway, "download_failed", "Failed to fetch theme file")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(http.StatusBadGateway, "download_failed", "Theme file returned non-200")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ThemeFileLimit))
	if err != nil {
		return nil, apiError(http.StatusBadGateway, "download_failed", "Failed to read theme file")
	}
	if !json.Valid(body) {
		return nil, apiError(http.StatusBadGateway, "download_invalid", "Theme file is not valid JSON")
	}
	return body, nil
}

// LoadThemeCatalog preserves the configured-origin cache identity, bounded
// reads, and the bridge's deliberately limited stale fallback conditions.
func (h *ThemeHandler) LoadThemeCatalog(ctx context.Context) (*ThemeCatalogResult, error) {
	catalogURL, _ := h.settings.Get(ctx, "theme.catalog_url")
	if catalogURL == "" {
		catalogURL = config.DefaultThemeCatalogURL
	}
	parsed, err := url.Parse(catalogURL)
	if err != nil || config.ValidateThemeRemoteURL(parsed) != nil {
		return nil, apiError(http.StatusBadRequest, "catalog_url_invalid", "Theme catalog URL must use HTTPS on an approved GitHub host")
	}
	h.catalogMu.RLock()
	cached, age, cachedURL := h.catalogCache, time.Since(h.catalogFetched), h.catalogURL
	h.catalogMu.RUnlock()
	if cachedURL != catalogURL {
		cached = nil
	}
	if cached != nil && age < catalogCacheTTL {
		return &ThemeCatalogResult{Body: cached}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
	if err != nil {
		if cached != nil {
			return &ThemeCatalogResult{Body: cached, Stale: true}, nil
		}
		return nil, apiError(http.StatusServiceUnavailable, "catalog_unavailable", "Theme catalog is unavailable")
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		if cached != nil {
			return &ThemeCatalogResult{Body: cached, Stale: true}, nil
		}
		return nil, apiError(http.StatusServiceUnavailable, "catalog_unavailable", "Theme catalog is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if cached != nil {
			return &ThemeCatalogResult{Body: cached, Stale: true}, nil
		}
		return nil, apiError(http.StatusServiceUnavailable, "catalog_unavailable", "Theme catalog returned non-200")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ThemeCatalogLimit))
	if err != nil {
		return nil, apiError(http.StatusBadGateway, "catalog_read_error", "Failed to read catalog response")
	}
	if !json.Valid(body) {
		return nil, apiError(http.StatusBadGateway, "catalog_invalid", "Catalog response is not valid JSON")
	}
	h.catalogMu.Lock()
	h.catalogCache = body
	h.catalogFetched = time.Now()
	h.catalogURL = catalogURL
	h.catalogMu.Unlock()
	return &ThemeCatalogResult{Body: body, CacheControl: "public, max-age=300"}, nil
}

// RefreshThemeCatalog clears this node's cache and fetches immediately. It is
// synchronous cache invalidation, not a cluster-wide job or durable dispatch.
func (h *ThemeHandler) RefreshThemeCatalog(ctx context.Context) (*ThemeCatalogResult, error) {
	h.catalogMu.Lock()
	h.catalogCache = nil
	h.catalogFetched = time.Time{}
	h.catalogURL = ""
	h.catalogMu.Unlock()
	return h.LoadThemeCatalog(ctx)
}
