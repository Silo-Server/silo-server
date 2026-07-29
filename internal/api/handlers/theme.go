package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// ThemeSettingsReader is the subset of ServerSettingsStore needed by ThemeHandler.
type ThemeSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// ThemeHandler serves theme-related public endpoints.
type ThemeHandler struct {
	settings ThemeSettingsReader

	// Catalog proxy cache.
	catalogMu      sync.RWMutex
	catalogCache   []byte
	catalogFetched time.Time
	catalogURL     string
	httpClient     *http.Client
}

// NewThemeHandler creates a ThemeHandler.
func NewThemeHandler(settings ThemeSettingsReader) *ThemeHandler {
	return &ThemeHandler{
		settings: settings,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return config.ValidateThemeRemoteURL(req.URL)
			},
		},
	}
}

// adminCssResponse is returned by GET /theme/admin-css.
type adminCssResponse struct {
	Vars   string `json:"vars"`
	RawCSS string `json:"raw_css"`
}

// HandleAdminCSS returns the server-wide admin theme overrides.
// Public endpoint — no authentication required. This allows admin
// branding to apply before login (white-label).
func (h *ThemeHandler) HandleAdminCSS(w http.ResponseWriter, r *http.Request) {
	vars, _ := h.settings.Get(r.Context(), "ui.admin_theme_vars")
	rawCSS, _ := h.settings.Get(r.Context(), "ui.admin_custom_css")

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, adminCssResponse{
		Vars:   vars,
		RawCSS: rawCSS,
	})
}

// HandleDownload proxies a theme file download from an allowed host.
// This prevents the browser from directly fetching arbitrary URLs (SSRF).
func (h *ThemeHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing url parameter")
		return
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid URL")
		return
	}
	if config.ValidateThemeRemoteURL(parsed) != nil {
		writeError(w, http.StatusForbidden, "host_not_allowed", "Theme downloads are only allowed over HTTPS from approved hosts")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Failed to create request")
		return
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "download_failed", "Failed to fetch theme file")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "download_failed", "Theme file returned non-200")
		return
	}

	// Limit to 256 KB to prevent abuse.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		writeError(w, http.StatusBadGateway, "download_failed", "Failed to read theme file")
		return
	}

	if !json.Valid(body) {
		writeError(w, http.StatusBadGateway, "download_invalid", "Theme file is not valid JSON")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

const (
	catalogCacheTTL = 1 * time.Hour
)

// writeStaleCatalog serves a cached catalog response with the stale header.
func writeStaleCatalog(w http.ResponseWriter, cached []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Theme-Catalog-Stale", "true")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cached)
}

// HandleCatalog proxies the theme catalog from a remote URL with caching.
func (h *ThemeHandler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	// Read and validate the configured origin before consulting the cache. A
	// cache entry belongs to one URL and must never mask a saved URL change.
	catalogURL, _ := h.settings.Get(r.Context(), "theme.catalog_url")
	if catalogURL == "" {
		catalogURL = config.DefaultThemeCatalogURL
	}
	parsedCatalogURL, err := url.Parse(catalogURL)
	if err != nil || config.ValidateThemeRemoteURL(parsedCatalogURL) != nil {
		writeError(w, http.StatusBadRequest, "catalog_url_invalid", "Theme catalog URL must use HTTPS on an approved GitHub host")
		return
	}

	h.catalogMu.RLock()
	cached := h.catalogCache
	age := time.Since(h.catalogFetched)
	cachedURL := h.catalogURL
	h.catalogMu.RUnlock()
	if cachedURL != catalogURL {
		cached = nil
	}

	if cached != nil && age < catalogCacheTTL {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cached)
		return
	}

	// Fetch from upstream.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, catalogURL, nil)
	if err != nil {
		if cached != nil {
			writeStaleCatalog(w, cached)
			return
		}
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "Theme catalog is unavailable")
		return
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		if cached != nil {
			writeStaleCatalog(w, cached)
			return
		}
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "Theme catalog is unavailable")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if cached != nil {
			writeStaleCatalog(w, cached)
			return
		}
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "Theme catalog returned non-200")
		return
	}

	// Read body (limit to 1 MB).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadGateway, "catalog_read_error", "Failed to read catalog response")
		return
	}

	// Validate JSON.
	if !json.Valid(body) {
		writeError(w, http.StatusBadGateway, "catalog_invalid", "Catalog response is not valid JSON")
		return
	}

	// Cache the response.
	h.catalogMu.Lock()
	h.catalogCache = body
	h.catalogFetched = time.Now()
	h.catalogURL = catalogURL
	h.catalogMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// HandleCatalogRefresh clears the cached catalog so the next request fetches fresh from upstream.
func (h *ThemeHandler) HandleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	h.catalogMu.Lock()
	h.catalogCache = nil
	h.catalogFetched = time.Time{}
	h.catalogURL = ""
	h.catalogMu.Unlock()

	// Immediately fetch fresh data so the caller gets the updated catalog.
	h.HandleCatalog(w, r)
}
