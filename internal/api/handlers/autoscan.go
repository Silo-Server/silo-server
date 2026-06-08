package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// autoscanStore is the subset of *autoscan.Repository the handler needs. The
// repository implements the full CRUD surface; this interface lets tests inject
// a fake.
type autoscanStore interface {
	GetSettings(ctx context.Context) (autoscan.Settings, error)
	UpdateSettings(ctx context.Context, s autoscan.Settings) (autoscan.Settings, error)
	ListConnections(ctx context.Context) ([]autoscan.Connection, error)
	CreateConnection(ctx context.Context, c autoscan.Connection) (autoscan.Connection, error)
	UpdateConnection(ctx context.Context, c autoscan.Connection) (autoscan.Connection, error)
	DeleteConnection(ctx context.Context, id string) error
	ListSources(ctx context.Context) ([]autoscan.Source, error)
	GetSource(ctx context.Context, id string) (autoscan.Source, error)
	CreateSource(ctx context.Context, s autoscan.Source) (autoscan.Source, error)
	UpdateSource(ctx context.Context, s autoscan.Source) (autoscan.Source, error)
	DeleteSource(ctx context.Context, id string) error
	ListAutoscanScans(ctx context.Context, filter autoscan.ScanListFilter) ([]autoscan.ScanWithEvent, error)
	CountAutoscanScans(ctx context.Context, filter autoscan.ScanListFilter) (int, error)
	ListEvents(ctx context.Context, filter autoscan.EventListFilter) ([]autoscan.EventWithRuns, error)
	CountEvents(ctx context.Context, filter autoscan.EventListFilter) (int, error)
	ListRunningEvents(ctx context.Context) ([]autoscan.Event, error)
	GetQueueSummary(ctx context.Context) (autoscan.QueueSummary, error)
	LatestEventAt(ctx context.Context) (*time.Time, error)
}

// autoscanTriggerer is the subset of *autoscan.Service the handler needs.
type autoscanTriggerer interface {
	PollOnce(ctx context.Context) error
	// ListAvailableScanSources enumerates installed scan_source capabilities for
	// the Add-source picker, and is also used to validate a create request
	// targets a currently-installed capability.
	ListAvailableScanSources(ctx context.Context) ([]autoscan.AvailableScanSource, error)
	// TestConnection probes an ad-hoc connection input; TestConnectionByID probes
	// an existing stored connection.
	TestConnection(ctx context.Context, c autoscan.Connection) (autoscan.ConnectionTestResult, error)
	TestConnectionByID(ctx context.Context, id string) (autoscan.ConnectionTestResult, error)
	// SuggestRewrites proposes path rewrites by matching the source's arr root
	// folders against Silo's media folders.
	SuggestRewrites(ctx context.Context, sourceID string) (autoscan.RewriteSuggestions, error)
}

// autoscanTriggerUpdater reconfigures the poll task's schedule when the default
// poll interval changes (satisfied by *taskmanager.TaskManager). It is optional:
// when nil, a settings change still persists but only takes effect on restart.
type autoscanTriggerUpdater interface {
	UpdateTriggers(key string, triggerConfigs []taskmanager.TriggerConfig) error
}

// autoscanPollTaskKey is the task-manager key for the autoscan poll task. It must
// match (*tasks.AutoscanPollTask).Key().
const autoscanPollTaskKey = "autoscan_poll"

// AutoscanHandler serves the admin-only autoscan settings/connections/sources/
// trigger API.
type AutoscanHandler struct {
	repo     autoscanStore
	svc      autoscanTriggerer
	triggers autoscanTriggerUpdater // optional; nil skips rescheduling
}

func NewAutoscanHandler(repo autoscanStore, svc autoscanTriggerer) *AutoscanHandler {
	return &AutoscanHandler{repo: repo, svc: svc}
}

// SetTriggerUpdater wires the optional poll-task rescheduler so a settings change
// re-applies the poll interval without a restart. Keeping it a setter (rather
// than a constructor arg) lets tests construct the handler without a task
// manager.
func (h *AutoscanHandler) SetTriggerUpdater(t autoscanTriggerUpdater) {
	h.triggers = t
}

// --- Settings ---

type autoscanSettingsResponse struct {
	Enabled                    bool `json:"enabled"`
	DefaultPollIntervalSeconds int  `json:"default_poll_interval_seconds"`
	DebounceSeconds            int  `json:"debounce_seconds"`
}

func settingsResponse(s autoscan.Settings) autoscanSettingsResponse {
	return autoscanSettingsResponse{
		Enabled:                    s.Enabled,
		DefaultPollIntervalSeconds: s.DefaultPollIntervalSeconds,
		DebounceSeconds:            s.DebounceSeconds,
	}
}

func (h *AutoscanHandler) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.repo.GetSettings(r.Context())
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse(settings))
}

func (h *AutoscanHandler) HandleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req autoscanSettingsResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.DefaultPollIntervalSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "default_poll_interval_seconds must be greater than 0")
		return
	}
	if req.DebounceSeconds < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "debounce_seconds must be 0 or greater")
		return
	}
	updated, err := h.repo.UpdateSettings(r.Context(), autoscan.Settings{
		Enabled:                    req.Enabled,
		DefaultPollIntervalSeconds: req.DefaultPollIntervalSeconds,
		DebounceSeconds:            req.DebounceSeconds,
	})
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	// Reschedule the poll task so a changed default_poll_interval_seconds takes
	// effect immediately rather than only after a restart. Optional: skip when no
	// task manager is wired (e.g. tests). A reschedule failure is non-fatal — the
	// new interval is persisted and will apply on the next restart regardless.
	if h.triggers != nil {
		intervalMs := int64(updated.DefaultPollIntervalSeconds) * 1000
		if terr := h.triggers.UpdateTriggers(autoscanPollTaskKey, []taskmanager.TriggerConfig{
			{Type: taskmanager.TriggerTypeInterval, IntervalMs: intervalMs},
		}); terr != nil {
			slog.WarnContext(r.Context(), "autoscan: reschedule poll task failed", "err", terr)
		}
	}
	writeJSON(w, http.StatusOK, settingsResponse(updated))
}

// --- Connections ---

// autoscanConnectionResponse is the client view of a connection. It NEVER
// includes api_key_ref or any resolved credential: callers manage credentials
// either by setting an api-key ref (write-only) or by linking a Requests
// integration.
type autoscanConnectionResponse struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	Kind                 string  `json:"kind"`
	BaseURL              string  `json:"base_url,omitempty"`
	RequestIntegrationID *string `json:"request_integration_id,omitempty"`
	// HasAPIKey reports whether the connection carries its own (write-only)
	// api-key ref, without disclosing the ref itself.
	HasAPIKey bool `json:"has_api_key"`
}

func connectionResponse(c autoscan.Connection) autoscanConnectionResponse {
	return autoscanConnectionResponse{
		ID:                   c.ID,
		Name:                 c.Name,
		Kind:                 c.Kind,
		BaseURL:              c.BaseURL,
		RequestIntegrationID: c.RequestIntegrationID,
		HasAPIKey:            strings.TrimSpace(c.APIKeyRef) != "",
	}
}

// autoscanConnectionInput is the write payload. api_key_ref is accepted (it is
// an opaque reference into platform settings, never a plaintext secret) but is
// never echoed back in any response.
type autoscanConnectionInput struct {
	Name                 string  `json:"name"`
	Kind                 string  `json:"kind"`
	BaseURL              string  `json:"base_url"`
	APIKeyRef            string  `json:"api_key_ref"`
	RequestIntegrationID *string `json:"request_integration_id"`
}

// validateConnectionInput enforces the invariant that migration 171 dropped from
// the DB CHECK and delegated to the application layer: a connection must carry
// either its own base_url or a live link to a Requests integration. A connection
// with neither is a both-NULL orphan that ConnectionResolver.Resolve would hand
// a plugin as an empty base URL. A whitespace-only request_integration_id counts
// as absent. Enforced on both create and update (an update can strip a
// connection to both-empty).
func validateConnectionInput(in autoscanConnectionInput) error {
	hasOwn := strings.TrimSpace(in.BaseURL) != ""
	hasLink := in.RequestIntegrationID != nil && strings.TrimSpace(*in.RequestIntegrationID) != ""
	if !hasOwn && !hasLink {
		return errors.New("connection requires base_url or request_integration_id")
	}
	return nil
}

// normalizeRequestIntegrationID trims whitespace from the optional Requests
// integration link and collapses an empty-after-trim value to nil. This keeps
// the persisted column either a real id or NULL — a pointer to "" or "  " must
// never be stored as a bogus link (the resolver guards empty at read time, but
// normalizing on write keeps the data consistent).
func normalizeRequestIntegrationID(ref *string) *string {
	if ref == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*ref)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (h *AutoscanHandler) HandleListConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := h.repo.ListConnections(r.Context())
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	out := make([]autoscanConnectionResponse, 0, len(conns))
	for _, c := range conns {
		out = append(out, connectionResponse(c))
	}
	writeJSON(w, http.StatusOK, struct {
		Connections []autoscanConnectionResponse `json:"connections"`
	}{Connections: out})
}

func (h *AutoscanHandler) HandleCreateConnection(w http.ResponseWriter, r *http.Request) {
	var in autoscanConnectionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if err := validateConnectionInput(in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	created, err := h.repo.CreateConnection(r.Context(), autoscan.Connection{
		Name:                 strings.TrimSpace(in.Name),
		Kind:                 strings.TrimSpace(in.Kind),
		BaseURL:              strings.TrimSpace(in.BaseURL),
		APIKeyRef:            strings.TrimSpace(in.APIKeyRef),
		RequestIntegrationID: normalizeRequestIntegrationID(in.RequestIntegrationID),
	})
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, connectionResponse(created))
}

func (h *AutoscanHandler) HandleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	var in autoscanConnectionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if err := validateConnectionInput(in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	updated, err := h.repo.UpdateConnection(r.Context(), autoscan.Connection{
		ID:                   id,
		Name:                 strings.TrimSpace(in.Name),
		Kind:                 strings.TrimSpace(in.Kind),
		BaseURL:              strings.TrimSpace(in.BaseURL),
		APIKeyRef:            strings.TrimSpace(in.APIKeyRef),
		RequestIntegrationID: normalizeRequestIntegrationID(in.RequestIntegrationID),
	})
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionResponse(updated))
}

func (h *AutoscanHandler) HandleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := h.repo.DeleteConnection(r.Context(), id); err != nil {
		writeAutoscanError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Sources ---

// autoscanSourceResponse is the client view of a source. It carries no
// credentials: the connection link is by id only.
type autoscanSourceResponse struct {
	ID                  string                 `json:"id"`
	PluginID            string                 `json:"plugin_id"`
	CapabilityID        string                 `json:"capability_id"`
	ConnectionID        *string                `json:"connection_id"`
	Enabled             bool                   `json:"enabled"`
	PollIntervalSeconds *int                   `json:"poll_interval_seconds,omitempty"`
	PathRewrites        []autoscan.PathRewrite `json:"path_rewrites"`
	SourceConfig        map[string]string      `json:"source_config"`
	Label               string                 `json:"label"`
	LastRunAt           *time.Time             `json:"last_run_at,omitempty"`
	LastError           *string                `json:"last_error,omitempty"`
}

func sourceResponse(s autoscan.Source) autoscanSourceResponse {
	// Always emit a (possibly empty) array, never JSON null, so clients can treat
	// path_rewrites as a stable list field.
	rewrites := s.PathRewrites
	if rewrites == nil {
		rewrites = []autoscan.PathRewrite{}
	}
	config := normalizeSourceConfig(s.SourceConfig)
	return autoscanSourceResponse{
		ID:                  s.ID,
		PluginID:            s.PluginID,
		CapabilityID:        s.CapabilityID,
		ConnectionID:        s.ConnectionID,
		Enabled:             s.Enabled,
		PollIntervalSeconds: s.PollIntervalSeconds,
		PathRewrites:        rewrites,
		SourceConfig:        config,
		Label:               s.Label,
		LastRunAt:           s.LastRunAt,
		LastError:           s.LastError,
	}
}

func (h *AutoscanHandler) HandleListSources(w http.ResponseWriter, r *http.Request) {
	// Sources are operator-created (no auto-seed): just list what exists. Use
	// GET /scan-source-plugins for the Add-source picker of installed capabilities.
	sources, err := h.repo.ListSources(r.Context())
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	out := make([]autoscanSourceResponse, 0, len(sources))
	for _, s := range sources {
		out = append(out, sourceResponse(s))
	}
	writeJSON(w, http.StatusOK, struct {
		Sources []autoscanSourceResponse `json:"sources"`
	}{Sources: out})
}

// --- Available scan-source plugins (Add-source picker) ---

type autoscanScanSourcePluginResponse struct {
	PluginID     string `json:"plugin_id"`
	CapabilityID string `json:"capability_id"`
	DisplayName  string `json:"display_name"`
}

// HandleListAvailableScanSources returns every installed scan_source capability
// an operator can create a source against (the Add-source picker list).
func (h *AutoscanHandler) HandleListAvailableScanSources(w http.ResponseWriter, r *http.Request) {
	available, err := h.svc.ListAvailableScanSources(r.Context())
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	out := make([]autoscanScanSourcePluginResponse, 0, len(available))
	for _, a := range available {
		out = append(out, autoscanScanSourcePluginResponse{
			PluginID:     a.PluginID,
			CapabilityID: a.CapabilityID,
			DisplayName:  a.DisplayName,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Plugins []autoscanScanSourcePluginResponse `json:"plugins"`
	}{Plugins: out})
}

// --- Create source ---

// autoscanCreateSourceInput is the create payload: it names the scan_source
// capability to bind (plugin_id + capability_id) plus the initial
// binding/scheduling fields. Unlike the update payload, identity is supplied
// here rather than read from an existing row.
type autoscanCreateSourceInput struct {
	PluginID            string                 `json:"plugin_id"`
	CapabilityID        string                 `json:"capability_id"`
	ConnectionID        *string                `json:"connection_id"`
	Enabled             bool                   `json:"enabled"`
	PollIntervalSeconds *int                   `json:"poll_interval_seconds"`
	PathRewrites        []autoscan.PathRewrite `json:"path_rewrites"`
	SourceConfig        map[string]string      `json:"source_config"`
	Label               string                 `json:"label"`
}

// HandleCreateSource creates a new source bound to an installed scan_source
// capability. Many sources may share one capability (e.g. one Sonarr plugin
// fronting several arr servers, one source per connection). The capability must
// currently be installed; enabling requires a bound connection.
func (h *AutoscanHandler) HandleCreateSource(w http.ResponseWriter, r *http.Request) {
	var in autoscanCreateSourceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	capID := strings.TrimSpace(in.CapabilityID)
	pluginID := strings.TrimSpace(in.PluginID)
	if pluginID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "plugin_id is required")
		return
	}
	if capID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "capability_id is required")
		return
	}
	if in.PollIntervalSeconds != nil && *in.PollIntervalSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "poll_interval_seconds must be greater than 0 when set")
		return
	}
	if err := validatePathRewrites(in.PathRewrites); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// Validate the (plugin_id, capability_id) is a currently-installed
	// scan_source capability — a source may only be created against an installed
	// plugin capability.
	available, err := h.svc.ListAvailableScanSources(r.Context())
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	if !scanSourceInstalled(available, pluginID, capID) {
		writeError(w, http.StatusBadRequest, "bad_request", "plugin_id + capability_id is not a currently-installed scan_source capability")
		return
	}
	connArg := normalizeConnectionID(in.ConnectionID)
	created, err := h.repo.CreateSource(r.Context(), autoscan.Source{
		PluginID:            pluginID,
		CapabilityID:        capID,
		ConnectionID:        connArg,
		Enabled:             in.Enabled,
		PollIntervalSeconds: in.PollIntervalSeconds,
		PathRewrites:        normalizePathRewrites(in.PathRewrites),
		SourceConfig:        normalizeSourceConfig(in.SourceConfig),
		Label:               autoscan.NormalizeSourceLabel(in.Label),
	})
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sourceResponse(created))
}

// scanSourceInstalled reports whether (pluginID, capabilityID) is in the
// set of currently-installed scan_source capabilities.
func scanSourceInstalled(available []autoscan.AvailableScanSource, pluginID, capabilityID string) bool {
	for _, a := range available {
		if a.PluginID == pluginID && a.CapabilityID == capabilityID {
			return true
		}
	}
	return false
}

// autoscanSourceInput is the source write payload. The (plugin_id,
// capability_id) identity is read from the existing source row, so only the
// schedulable/binding fields are accepted.
//
// connection_id is a *string so the caller can express three distinct states:
//   - field absent / JSON null  → nil  → unbind (clear the connection)
//   - non-empty string UUID     → bind to that connection
//
// The UI must always send the full desired state for all three fields.
// path_rewrites is full-state like connection_id: the UI always sends the
// complete desired list. Each entry needs a non-empty from and to.
type autoscanSourceInput struct {
	ConnectionID        *string                `json:"connection_id"`
	Enabled             bool                   `json:"enabled"`
	PollIntervalSeconds *int                   `json:"poll_interval_seconds"`
	PathRewrites        []autoscan.PathRewrite `json:"path_rewrites"`
	SourceConfig        map[string]string      `json:"source_config"`
	Label               string                 `json:"label"`
}

// validatePathRewrites lightly validates a source's rewrite list: each entry
// must carry a non-empty (trimmed) from and to. Returns an error suitable for a
// 400 response.
func validatePathRewrites(rewrites []autoscan.PathRewrite) error {
	for i, rw := range rewrites {
		if strings.TrimSpace(rw.From) == "" || strings.TrimSpace(rw.To) == "" {
			return fmt.Errorf("path_rewrites[%d]: from and to are both required", i)
		}
	}
	return nil
}

// normalizePathRewrites trims whitespace off each rewrite's from/to so stored
// rules match the same trimming applyRewrites does at poll time.
func normalizePathRewrites(rewrites []autoscan.PathRewrite) []autoscan.PathRewrite {
	out := make([]autoscan.PathRewrite, 0, len(rewrites))
	for _, rw := range rewrites {
		out = append(out, autoscan.PathRewrite{
			From: strings.TrimSpace(rw.From),
			To:   strings.TrimSpace(rw.To),
		})
	}
	return out
}

func normalizeSourceConfig(config map[string]string) map[string]string {
	out := make(map[string]string, len(config))
	for key, value := range config {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

func (h *AutoscanHandler) HandleUpdateSource(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	var in autoscanSourceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if in.PollIntervalSeconds != nil && *in.PollIntervalSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "poll_interval_seconds must be greater than 0 when set")
		return
	}
	if err := validatePathRewrites(in.PathRewrites); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// connection_id is a full-state field: nil means unbind, a UUID string
	// means bind. A whitespace-only string is normalised to nil (unbound).
	connArg := normalizeConnectionID(in.ConnectionID)
	// Update by id; identity (plugin_id, capability_id) is immutable and
	// preserved by the repo. A missing source maps to 404.
	updated, err := h.repo.UpdateSource(r.Context(), autoscan.Source{
		ID:                  id,
		ConnectionID:        connArg,
		Enabled:             in.Enabled,
		PollIntervalSeconds: in.PollIntervalSeconds,
		PathRewrites:        normalizePathRewrites(in.PathRewrites),
		SourceConfig:        normalizeSourceConfig(in.SourceConfig),
		Label:               autoscan.NormalizeSourceLabel(in.Label),
	})
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sourceResponse(updated))
}

// normalizeConnectionID maps a full-state connection_id input to a stored value:
// a nil pointer or a whitespace-only string means unbound (nil); a non-empty
// trimmed UUID binds to that connection.
func normalizeConnectionID(in *string) *string {
	if in == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*in)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// HandleDeleteSource removes a source row. This is the operator's escape hatch
// for an orphaned source (one whose scan_source plugin was uninstalled or no
// longer resolves cleanly). An unknown id maps to 404 via autoscan.ErrNotFound.
func (h *AutoscanHandler) HandleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := h.repo.DeleteSource(r.Context(), id); err != nil {
		writeAutoscanError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Test connection ---

// autoscanTestConnectionInput probes either an ad-hoc connection (own base_url +
// api_key_ref, or a linked request_integration_id) OR an existing stored
// connection by id. When connection_id is set, it takes precedence and the
// ad-hoc fields are ignored.
type autoscanTestConnectionInput struct {
	ConnectionID         *string `json:"connection_id"`
	BaseURL              string  `json:"base_url"`
	APIKeyRef            string  `json:"api_key_ref"`
	RequestIntegrationID *string `json:"request_integration_id"`
}

// autoscanTestConnectionResponse is the probe outcome: ok=true with the reported
// arr version on success, or ok=false with a human-readable error on failure
// (unreachable / 401 / non-200). A failed probe is still a 200 response — the
// failure lives in the payload, not the HTTP status.
type autoscanTestConnectionResponse struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// HandleTestConnection probes an arr connection's /api/v3/system/status.
func (h *AutoscanHandler) HandleTestConnection(w http.ResponseWriter, r *http.Request) {
	var in autoscanTestConnectionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	var (
		result autoscan.ConnectionTestResult
		err    error
	)
	if in.ConnectionID != nil && strings.TrimSpace(*in.ConnectionID) != "" {
		result, err = h.svc.TestConnectionByID(r.Context(), strings.TrimSpace(*in.ConnectionID))
	} else {
		// Ad-hoc connection: must carry own base_url or a linked integration,
		// same invariant as create/update.
		conn := autoscan.Connection{
			BaseURL:              strings.TrimSpace(in.BaseURL),
			APIKeyRef:            strings.TrimSpace(in.APIKeyRef),
			RequestIntegrationID: in.RequestIntegrationID,
		}
		hasOwn := conn.BaseURL != ""
		hasLink := conn.RequestIntegrationID != nil && strings.TrimSpace(*conn.RequestIntegrationID) != ""
		if !hasOwn && !hasLink {
			writeError(w, http.StatusBadRequest, "bad_request", "connection requires connection_id, base_url, or request_integration_id")
			return
		}
		result, err = h.svc.TestConnection(r.Context(), conn)
	}
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, autoscanTestConnectionResponse{
		OK:      result.OK,
		Version: result.Version,
		Error:   result.Err,
	})
}

// --- Rewrite suggestions (sync from arr) ---

// HandleRewriteSuggestions proposes path rewrites for a source by matching its
// bound connection's arr root folders against Silo's media folders. The
// operator applies chosen suggestions via the normal source PUT (path_rewrites).
func (h *AutoscanHandler) HandleRewriteSuggestions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	suggestions, err := h.svc.SuggestRewrites(r.Context(), id)
	if err != nil {
		if errors.Is(err, autoscan.ErrNoConnection) {
			writeError(w, http.StatusBadRequest, "bad_request", "source has no bound connection; bind one before syncing rewrites")
			return
		}
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, suggestions)
}

// --- Trigger ---

func (h *AutoscanHandler) HandleTrigger(w http.ResponseWriter, r *http.Request) {
	// Run the poll detached: don't block the request on a full cycle, and don't
	// tie the poll to the request context (which is cancelled once we respond).
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := h.svc.PollOnce(ctx); err != nil {
			slog.WarnContext(ctx, "autoscan: manual poll failed", "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, struct {
		Status string `json:"status"`
	}{Status: "ok"})
}

// --- Events ---

type autoscanEventScanRunResponse struct {
	ID           string     `json:"id"`
	LibraryID    int        `json:"library_id"`
	Mode         string     `json:"mode"`
	Path         string     `json:"path,omitempty"`
	Trigger      string     `json:"trigger"`
	Status       string     `json:"status"`
	RequestedAt  *time.Time `json:"requested_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
}

type autoscanEventResponse struct {
	ID              int64                          `json:"id"`
	SourceID        *string                        `json:"source_id"`
	PluginID        string                         `json:"plugin_id"`
	CapabilityID    string                         `json:"capability_id"`
	StartedAt       time.Time                      `json:"started_at"`
	CompletedAt     time.Time                      `json:"completed_at"`
	DurationMS      int64                          `json:"duration_ms"`
	Status          string                         `json:"status"`
	ChangesReturned int                            `json:"changes_returned"`
	ChangesResolved int                            `json:"changes_resolved"`
	TargetsClaimed  int                            `json:"targets_claimed"`
	ScansCreated    int                            `json:"scans_created"`
	ScansReused     int                            `json:"scans_reused"`
	ScansSuppressed int                            `json:"scans_suppressed"`
	ErrorMessage    string                         `json:"error_message,omitempty"`
	ScanRuns        []autoscanEventScanRunResponse `json:"scan_runs"`
}

type autoscanEventsResponse struct {
	Events []autoscanEventResponse `json:"events"`
	Total  int                     `json:"total"`
	Limit  int                     `json:"limit"`
	Offset int                     `json:"offset"`
}

func eventResponse(event autoscan.EventWithRuns) autoscanEventResponse {
	runs := make([]autoscanEventScanRunResponse, 0, len(event.Runs))
	for _, run := range event.Runs {
		runs = append(runs, autoscanEventScanRunResponse{
			ID:           run.ID,
			LibraryID:    run.MediaFolderID,
			Mode:         run.Mode,
			Path:         run.Path,
			Trigger:      run.Trigger,
			Status:       run.Status,
			RequestedAt:  run.RequestedAt,
			StartedAt:    run.StartedAt,
			CompletedAt:  run.CompletedAt,
			ErrorMessage: run.ErrorMessage,
		})
	}
	e := event.Event
	return autoscanEventResponse{
		ID:              e.ID,
		SourceID:        e.SourceID,
		PluginID:        e.PluginID,
		CapabilityID:    e.CapabilityID,
		StartedAt:       e.StartedAt,
		CompletedAt:     e.CompletedAt,
		DurationMS:      e.DurationMS,
		Status:          string(e.Status),
		ChangesReturned: e.ChangesReturned,
		ChangesResolved: e.ChangesResolved,
		TargetsClaimed:  e.TargetsClaimed,
		ScansCreated:    e.ScansCreated,
		ScansReused:     e.ScansReused,
		ScansSuppressed: e.ScansSuppressed,
		ErrorMessage:    e.ErrorMessage,
		ScanRuns:        runs,
	}
}

func (h *AutoscanHandler) HandleListEvents(w http.ResponseWriter, r *http.Request) {
	filter := autoscan.EventListFilter{
		SourceID: strings.TrimSpace(r.URL.Query().Get("source_id")),
		Status:   autoscan.EventStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
		Search:   strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:    50,
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		filter.Limit = limit
	}
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		offset, err := strconv.Atoi(rawOffset)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "offset must be a non-negative integer")
			return
		}
		filter.Offset = offset
	}
	switch filter.Status {
	case "", autoscan.EventStatusRunning, autoscan.EventStatusSuccess, autoscan.EventStatusError, autoscan.EventStatusUnresolved:
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "status must be running, success, error, or unresolved")
		return
	}

	events, err := h.repo.ListEvents(r.Context(), filter)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	total, err := h.repo.CountEvents(r.Context(), filter)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	out := make([]autoscanEventResponse, 0, len(events))
	for _, event := range events {
		out = append(out, eventResponse(event))
	}
	writeJSON(w, http.StatusOK, autoscanEventsResponse{
		Events: out,
		Total:  total,
		Limit:  filter.Limit,
		Offset: filter.Offset,
	})
}

type autoscanScanResponse struct {
	ID               string     `json:"id"`
	LibraryID        int        `json:"library_id"`
	Mode             string     `json:"mode"`
	Path             string     `json:"path,omitempty"`
	Trigger          string     `json:"trigger"`
	Status           string     `json:"status"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	RequestedAt      *time.Time `json:"requested_at,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	AutoscanEventID  *int64     `json:"autoscan_event_id,omitempty"`
	SourceID         *string    `json:"source_id,omitempty"`
	PluginID         string     `json:"plugin_id,omitempty"`
	CapabilityID     string     `json:"capability_id,omitempty"`
	EventStatus      string     `json:"event_status,omitempty"`
	EventCompletedAt *time.Time `json:"event_completed_at,omitempty"`
}

type autoscanScansResponse struct {
	Scans  []autoscanScanResponse `json:"scans"`
	Total  int                    `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

func autoscanScanRunResponse(scan autoscan.ScanWithEvent) autoscanScanResponse {
	return autoscanScanResponse{
		ID:               scan.ID,
		LibraryID:        scan.MediaFolderID,
		Mode:             scan.Mode,
		Path:             scan.Path,
		Trigger:          scan.Trigger,
		Status:           scan.Status,
		ErrorMessage:     scan.ErrorMessage,
		RequestedAt:      scan.RequestedAt,
		StartedAt:        scan.StartedAt,
		CompletedAt:      scan.CompletedAt,
		AutoscanEventID:  scan.AutoscanEventID,
		SourceID:         scan.SourceID,
		PluginID:         scan.PluginID,
		CapabilityID:     scan.CapabilityID,
		EventStatus:      string(scan.EventStatus),
		EventCompletedAt: scan.EventCompletedAt,
	}
}

func (h *AutoscanHandler) HandleListScans(w http.ResponseWriter, r *http.Request) {
	filter := autoscan.ScanListFilter{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Search: strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  50,
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		filter.Limit = limit
	}
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		offset, err := strconv.Atoi(rawOffset)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "offset must be a non-negative integer")
			return
		}
		filter.Offset = offset
	}
	switch filter.Status {
	case "", "accepted", "running", "completed", "failed", "cancelled":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "status must be accepted, running, completed, failed, or cancelled")
		return
	}

	scans, err := h.repo.ListAutoscanScans(r.Context(), filter)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	total, err := h.repo.CountAutoscanScans(r.Context(), filter)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	out := make([]autoscanScanResponse, 0, len(scans))
	for _, scan := range scans {
		out = append(out, autoscanScanRunResponse(scan))
	}
	writeJSON(w, http.StatusOK, autoscanScansResponse{
		Scans:  out,
		Total:  total,
		Limit:  filter.Limit,
		Offset: filter.Offset,
	})
}

// --- Status ---

type autoscanStatusSource struct {
	ID           string                 `json:"id"`
	PluginID     string                 `json:"plugin_id"`
	CapabilityID string                 `json:"capability_id"`
	ConnectionID *string                `json:"connection_id"`
	Enabled      bool                   `json:"enabled"`
	Label        string                 `json:"label"`
	PathRewrites []autoscan.PathRewrite `json:"path_rewrites"`
	LastRunAt    *time.Time             `json:"last_run_at,omitempty"`
	LastError    *string                `json:"last_error,omitempty"`
}

type autoscanRunningPollResponse struct {
	ID           int64     `json:"id"`
	SourceID     *string   `json:"source_id"`
	PluginID     string    `json:"plugin_id"`
	CapabilityID string    `json:"capability_id"`
	StartedAt    time.Time `json:"started_at"`
	ElapsedMS    int64     `json:"elapsed_ms"`
	MarkerBefore *string   `json:"marker_before,omitempty"`
}

type autoscanStatusResponse struct {
	Enabled       bool                          `json:"enabled"`
	Sources       []autoscanStatusSource        `json:"sources"`
	RunningPolls  []autoscanRunningPollResponse `json:"running_polls"`
	ActiveScans   int                           `json:"active_scans"`
	AcceptedScans int                           `json:"accepted_scans"`
	RunningScans  int                           `json:"running_scans"`
	LatestEventAt *time.Time                    `json:"latest_event_at,omitempty"`
}

func (h *AutoscanHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := h.repo.GetSettings(ctx)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	sources, err := h.repo.ListSources(ctx)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	queue, err := h.repo.GetQueueSummary(ctx)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	runningEvents, err := h.repo.ListRunningEvents(ctx)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	latestEventAt, err := h.repo.LatestEventAt(ctx)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	trimmed := make([]autoscanStatusSource, 0, len(sources))
	for _, src := range sources {
		rewrites := src.PathRewrites
		if rewrites == nil {
			rewrites = []autoscan.PathRewrite{}
		}
		trimmed = append(trimmed, autoscanStatusSource{
			ID:           src.ID,
			PluginID:     src.PluginID,
			CapabilityID: src.CapabilityID,
			ConnectionID: src.ConnectionID,
			Enabled:      src.Enabled,
			Label:        src.Label,
			PathRewrites: rewrites,
			LastRunAt:    src.LastRunAt,
			LastError:    src.LastError,
		})
	}
	now := time.Now()
	runningPolls := make([]autoscanRunningPollResponse, 0, len(runningEvents))
	for _, event := range runningEvents {
		elapsed := now.Sub(event.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		runningPolls = append(runningPolls, autoscanRunningPollResponse{
			ID:           event.ID,
			SourceID:     event.SourceID,
			PluginID:     event.PluginID,
			CapabilityID: event.CapabilityID,
			StartedAt:    event.StartedAt,
			ElapsedMS:    elapsed.Milliseconds(),
			MarkerBefore: event.MarkerBefore,
		})
	}
	writeJSON(w, http.StatusOK, autoscanStatusResponse{
		Enabled:       s.Enabled,
		Sources:       trimmed,
		RunningPolls:  runningPolls,
		ActiveScans:   queue.Active,
		AcceptedScans: queue.Accepted,
		RunningScans:  queue.Running,
		LatestEventAt: latestEventAt,
	})
}

// writeAutoscanError maps autoscan repository/service errors to HTTP status
// codes: a missing connection or source maps to 404, everything else to 500.
func writeAutoscanError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, autoscan.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Autoscan resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Autoscan operation failed")
}
