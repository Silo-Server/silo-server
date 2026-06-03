package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	UpsertSource(ctx context.Context, s autoscan.Source) (autoscan.Source, error)
	DeleteSource(ctx context.Context, id string) error
}

// autoscanTriggerer is the subset of *autoscan.Service the handler needs.
type autoscanTriggerer interface {
	PollOnce(ctx context.Context) error
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

// validateConnectionInput enforces the invariant that migration 172 dropped from
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
		RequestIntegrationID: in.RequestIntegrationID,
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
		RequestIntegrationID: in.RequestIntegrationID,
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
	InstallationID      int                    `json:"installation_id"`
	CapabilityID        string                 `json:"capability_id"`
	ConnectionID        *string                `json:"connection_id"`
	Enabled             bool                   `json:"enabled"`
	PollIntervalSeconds *int                   `json:"poll_interval_seconds,omitempty"`
	PathRewrites        []autoscan.PathRewrite `json:"path_rewrites"`
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
	return autoscanSourceResponse{
		ID:                  s.ID,
		InstallationID:      s.InstallationID,
		CapabilityID:        s.CapabilityID,
		ConnectionID:        s.ConnectionID,
		Enabled:             s.Enabled,
		PollIntervalSeconds: s.PollIntervalSeconds,
		PathRewrites:        rewrites,
		LastRunAt:           s.LastRunAt,
		LastError:           s.LastError,
	}
}

func (h *AutoscanHandler) HandleListSources(w http.ResponseWriter, r *http.Request) {
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

// autoscanSourceInput is the source write payload. The (installation_id,
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
	// Look up the existing source so the (installation_id, capability_id)
	// identity is preserved on upsert; a missing source maps to 404.
	existing, err := h.repo.GetSource(r.Context(), id)
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	// connection_id is a full-state field: nil means unbind, a UUID string
	// means bind. A whitespace-only string is normalised to nil (unbound).
	var connArg *string
	if in.ConnectionID != nil {
		trimmed := strings.TrimSpace(*in.ConnectionID)
		if trimmed != "" {
			connArg = &trimmed
		}
	}
	// Enabling a source requires a bound connection: it can't be polled without
	// credentials.
	if in.Enabled && connArg == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "connection_id is required to enable a source")
		return
	}
	updated, err := h.repo.UpsertSource(r.Context(), autoscan.Source{
		InstallationID:      existing.InstallationID,
		CapabilityID:        existing.CapabilityID,
		ConnectionID:        connArg,
		Enabled:             in.Enabled,
		PollIntervalSeconds: in.PollIntervalSeconds,
		PathRewrites:        normalizePathRewrites(in.PathRewrites),
	})
	if err != nil {
		writeAutoscanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sourceResponse(updated))
}

// HandleDeleteSource removes a source row. This is the operator's escape hatch
// for an orphaned source (one whose scan_source plugin was uninstalled): the
// poll loop skips such rows quietly, and this endpoint clears them. An unknown
// id maps to 404 via autoscan.ErrNotFound.
func (h *AutoscanHandler) HandleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if err := h.repo.DeleteSource(r.Context(), id); err != nil {
		writeAutoscanError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// --- Status ---

type autoscanStatusSource struct {
	ID             string                 `json:"id"`
	InstallationID int                    `json:"installation_id"`
	CapabilityID   string                 `json:"capability_id"`
	ConnectionID   *string                `json:"connection_id"`
	Enabled        bool                   `json:"enabled"`
	PathRewrites   []autoscan.PathRewrite `json:"path_rewrites"`
	LastRunAt      *time.Time             `json:"last_run_at,omitempty"`
	LastError      *string                `json:"last_error,omitempty"`
}

type autoscanStatusResponse struct {
	Enabled bool                   `json:"enabled"`
	Sources []autoscanStatusSource `json:"sources"`
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
	trimmed := make([]autoscanStatusSource, 0, len(sources))
	for _, src := range sources {
		rewrites := src.PathRewrites
		if rewrites == nil {
			rewrites = []autoscan.PathRewrite{}
		}
		trimmed = append(trimmed, autoscanStatusSource{
			ID:             src.ID,
			InstallationID: src.InstallationID,
			CapabilityID:   src.CapabilityID,
			ConnectionID:   src.ConnectionID,
			Enabled:        src.Enabled,
			PathRewrites:   rewrites,
			LastRunAt:      src.LastRunAt,
			LastError:      src.LastError,
		})
	}
	writeJSON(w, http.StatusOK, autoscanStatusResponse{
		Enabled: s.Enabled,
		Sources: trimmed,
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
