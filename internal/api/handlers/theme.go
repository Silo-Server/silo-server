package handlers

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// ThemeSettingsReader is the subset of ServerSettingsStore needed by ThemeHandler.
type ThemeSettingsReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// ThemeHandler serves the frozen /api/v1 theme routes. The web client has one
// look and reads none of them; they stay on the bridge surface, reporting
// whatever is stored, until v1 retires.
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

// AdminCSSView is returned by GET /theme/admin-css.
type AdminCSSView struct {
	Vars   string `json:"vars"`
	RawCSS string `json:"raw_css"`
}

// HandleAdminCSS returns the retired server-wide theme overrides. Public
// endpoint — no authentication required, as it was read before login.
func (h *ThemeHandler) HandleAdminCSS(w http.ResponseWriter, r *http.Request) {
	vars, _ := h.settings.Get(r.Context(), "ui.admin_theme_vars")
	rawCSS, _ := h.settings.Get(r.Context(), "ui.admin_custom_css")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, AdminCSSView{Vars: vars, RawCSS: rawCSS})
}

// HandleDownload retains the frozen portable JSON response.
func (h *ThemeHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	body, err := h.DownloadThemeFile(r.Context(), r.URL.Query().Get("url"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// HandleCatalog retains the bridge cache and stale response headers.
func (h *ThemeHandler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	result, err := h.LoadThemeCatalog(r.Context())
	writeThemeCatalog(w, result, err)
}

func (h *ThemeHandler) HandleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	result, err := h.RefreshThemeCatalog(r.Context())
	writeThemeCatalog(w, result, err)
}

func writeThemeCatalog(w http.ResponseWriter, result *ThemeCatalogResult, err error) {
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if result.Stale {
		w.Header().Set("X-Theme-Catalog-Stale", "true")
	}
	if result.CacheControl != "" {
		w.Header().Set("Cache-Control", result.CacheControl)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Body)
}
