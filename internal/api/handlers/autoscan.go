package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	CreateWebhookEndpoint(ctx context.Context, sourceID string) (autoscan.WebhookEndpoint, string, error)
	RotateWebhookEndpoint(ctx context.Context, sourceID string) (autoscan.WebhookEndpoint, string, error)
	DeleteWebhookEndpoint(ctx context.Context, sourceID string) error
	GetWebhookEndpoint(ctx context.Context, sourceID string) (autoscan.WebhookEndpoint, error)
	ListWebhookEndpoints(ctx context.Context) ([]autoscan.WebhookEndpoint, error)
	RevealWebhookToken(ctx context.Context, sourceID string) (string, error)
	ResolveWebhookToken(ctx context.Context, token string) (autoscan.Source, autoscan.WebhookEndpoint, error)
	TouchWebhookReceived(ctx context.Context, sourceID string) error
	RecordWebhookError(ctx context.Context, sourceID, msg string) error
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
	// IngestChanges feeds webhook-delivered changes through the shared
	// rewrite/resolve/suppress/enqueue pipeline.
	IngestChanges(ctx context.Context, in autoscan.ChangeIngest) (autoscan.IngestResult, error)
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
// trigger API plus the public webhook delivery endpoint.
type AutoscanHandler struct {
	repo     autoscanStore
	svc      autoscanTriggerer
	triggers autoscanTriggerUpdater // optional; nil skips rescheduling
	// publicURL is the server's externally reachable base URL; when empty,
	// webhook URLs are returned as absolute paths for the UI to complete.
	publicURL string
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

// SetPublicURL wires the server's public base URL so webhook URLs are returned
// fully qualified. Optional: when unset, responses carry the absolute path and
// the admin UI prefixes its own origin.
func (h *AutoscanHandler) SetPublicURL(publicURL string) {
	h.publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
}

// --- Settings ---

type autoscanSettingsResponse = AdminAutoscanSettingsView

type AdminAutoscanSettingsView struct {
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

func (h *AutoscanHandler) ReadAdminAutoscanSettings(ctx context.Context) (AdminAutoscanSettingsView, error) {
	settings, err := h.repo.GetSettings(ctx)
	if err != nil {
		return AdminAutoscanSettingsView{}, err
	}
	return settingsResponse(settings), nil
}

// --- Connections ---

// autoscanConnectionResponse is the client view of a connection. It NEVER
// includes api_key_ref or any resolved credential: callers manage credentials
// either by setting an api-key ref (write-only) or by linking a Requests
// integration.
type autoscanConnectionResponse = AdminAutoscanConnectionView

type AdminAutoscanConnectionView struct {
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

// ReadAdminAutoscanConnections returns configured connections without resolving credentials.
func (h *AutoscanHandler) ReadAdminAutoscanConnections(ctx context.Context) ([]AdminAutoscanConnectionView, error) {
	conns, err := h.repo.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AdminAutoscanConnectionView, 0, len(conns))
	for _, c := range conns {
		out = append(out, connectionResponse(c))
	}
	return out, nil
}

// --- Sources ---

// autoscanSourceResponse is the client view of a source. It carries no
// credentials: the connection link is by id only. The webhook_* fields are
// admin-only setup surface; webhook_url deliberately stays redisplayable (the
// token's blast radius is spurious scans of already-registered library paths,
// and one-time display would force a rotation after any config loss).
type autoscanSourceResponse = AdminAutoscanSourceView

type AdminAutoscanSourceView struct {
	ID                      string                 `json:"id"`
	PluginID                string                 `json:"plugin_id"`
	CapabilityID            string                 `json:"capability_id"`
	ConnectionID            *string                `json:"connection_id"`
	Enabled                 bool                   `json:"enabled"`
	DeliveryMode            string                 `json:"delivery_mode"`
	PollIntervalSeconds     *int                   `json:"poll_interval_seconds,omitempty"`
	PathRewrites            []autoscan.PathRewrite `json:"path_rewrites"`
	SourceConfig            map[string]string      `json:"source_config"`
	Label                   string                 `json:"label"`
	LastRunAt               *time.Time             `json:"last_run_at,omitempty"`
	LastError               *string                `json:"last_error,omitempty"`
	WebhookConfigured       bool                   `json:"webhook_configured"`
	WebhookURL              string                 `json:"webhook_url,omitempty"`
	WebhookSecretSuffix     string                 `json:"webhook_secret_suffix,omitempty"`
	WebhookLastReceivedAt   *time.Time             `json:"webhook_last_received_at,omitempty"`
	WebhookLastErrorAt      *time.Time             `json:"webhook_last_error_at,omitempty"`
	WebhookLastErrorMessage string                 `json:"webhook_last_error_message,omitempty"`
}

func sourceResponse(s autoscan.Source) autoscanSourceResponse {
	// Always emit a (possibly empty) array, never JSON null, so clients can treat
	// path_rewrites as a stable list field.
	rewrites := s.PathRewrites
	if rewrites == nil {
		rewrites = []autoscan.PathRewrite{}
	}
	config := normalizeSourceConfig(s.SourceConfig)
	deliveryMode := s.DeliveryMode
	if deliveryMode == "" {
		deliveryMode = autoscan.DeliveryModePoll
	}
	return autoscanSourceResponse{
		ID:                  s.ID,
		PluginID:            s.PluginID,
		CapabilityID:        s.CapabilityID,
		ConnectionID:        s.ConnectionID,
		Enabled:             s.Enabled,
		DeliveryMode:        deliveryMode,
		PollIntervalSeconds: s.PollIntervalSeconds,
		PathRewrites:        rewrites,
		SourceConfig:        config,
		Label:               s.Label,
		LastRunAt:           s.LastRunAt,
		LastError:           s.LastError,
	}
}

// webhookURLFor builds the delivery URL for a token: fully qualified when the
// server knows its public URL, otherwise an absolute path the admin UI
// completes with its own origin.
//
// Minted in the v2 namespace. The URL is copied into an external system
// (Sonarr/Radarr and friends) and has to keep resolving after the /api/v1
// tombstone; the bridge serves both delivery routes, so a v1 admin client
// reading this value stays correct.
func (h *AutoscanHandler) webhookURLFor(token string) string {
	return h.publicURL + "/api/v2/autoscan/webhooks/" + token
}

// attachWebhookState decorates a source response with its endpoint status and
// redisplayable URL. A reveal failure (e.g. cipher/key drift) degrades to
// status-without-URL rather than failing the response.
func (h *AutoscanHandler) attachWebhookState(ctx context.Context, resp *autoscanSourceResponse, endpoint autoscan.WebhookEndpoint) {
	resp.WebhookConfigured = true
	resp.WebhookSecretSuffix = endpoint.SecretSuffix
	resp.WebhookLastReceivedAt = endpoint.LastReceivedAt
	resp.WebhookLastErrorAt = endpoint.LastErrorAt
	resp.WebhookLastErrorMessage = endpoint.LastErrorMessage
	token, err := h.repo.RevealWebhookToken(ctx, endpoint.SourceID)
	if err != nil {
		slog.WarnContext(ctx, "autoscan: reveal webhook token failed", "component", "api", "source_id", endpoint.SourceID, "err", err)
		return
	}
	resp.WebhookURL = h.webhookURLFor(token)
}

// sourceResponseWithWebhook builds a single source response including webhook
// endpoint state (used by the single-source create/update/webhook handlers).
func (h *AutoscanHandler) sourceResponseWithWebhook(ctx context.Context, s autoscan.Source) autoscanSourceResponse {
	resp := sourceResponse(s)
	endpoint, err := h.repo.GetWebhookEndpoint(ctx, s.ID)
	if err != nil {
		if !errors.Is(err, autoscan.ErrNotFound) {
			slog.WarnContext(ctx, "autoscan: load webhook endpoint failed", "component", "api", "source_id", s.ID, "err", err)
		}
		return resp
	}
	h.attachWebhookState(ctx, &resp, endpoint)
	return resp
}

// ReadAdminAutoscanSources reuses the batched endpoint query and existing token reveal.
func (h *AutoscanHandler) ReadAdminAutoscanSources(ctx context.Context) ([]AdminAutoscanSourceView, error) {
	// Sources are operator-created (no auto-seed): just list what exists. Use
	// GET /scan-source-plugins for the Add-source picker of installed capabilities.
	sources, err := h.repo.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	// One batched endpoint query for the whole listing (not per source).
	endpoints, err := h.repo.ListWebhookEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	endpointBySource := make(map[string]autoscan.WebhookEndpoint, len(endpoints))
	for _, e := range endpoints {
		endpointBySource[e.SourceID] = e
	}
	out := make([]autoscanSourceResponse, 0, len(sources))
	for _, s := range sources {
		resp := sourceResponse(s)
		if endpoint, ok := endpointBySource[s.ID]; ok {
			h.attachWebhookState(ctx, &resp, endpoint)
		}
		out = append(out, resp)
	}
	return out, nil
}

// --- Available scan-source plugins (Add-source picker) ---

// autoscanScanSourcePluginResponse describes one creatable scan source. The
// descriptor is additive: clients that predate it keep reading the same three
// identity fields.
type autoscanScanSourcePluginResponse struct {
	PluginID     string                        `json:"plugin_id"`
	CapabilityID string                        `json:"capability_id"`
	DisplayName  string                        `json:"display_name"`
	Description  string                        `json:"description,omitempty"`
	Descriptor   autoscan.ScanSourceDescriptor `json:"descriptor"`
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
	DeliveryMode        string                 `json:"delivery_mode"`
	PollIntervalSeconds *int                   `json:"poll_interval_seconds"`
	PathRewrites        []autoscan.PathRewrite `json:"path_rewrites"`
	SourceConfig        map[string]string      `json:"source_config"`
	Label               string                 `json:"label"`
}

// resolveDeliveryMode validates a requested delivery mode against the source
// identity: webhook mode exists only for the built-in ARR webhook identity
// (which cannot poll — it has no plugin), and an empty mode defaults to
// whichever mode the identity supports.
func resolveDeliveryMode(requested, pluginID, capabilityID string) (string, error) {
	isBuiltinWebhook := autoscan.IsBuiltinArrWebhookIdentity(pluginID, capabilityID)
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "":
		if isBuiltinWebhook {
			return autoscan.DeliveryModeWebhook, nil
		}
		return autoscan.DeliveryModePoll, nil
	case autoscan.DeliveryModePoll:
		if isBuiltinWebhook {
			return "", fmt.Errorf("the built-in Sonarr/Radarr webhook source has no plugin to poll; delivery_mode must be webhook")
		}
		return autoscan.DeliveryModePoll, nil
	case autoscan.DeliveryModeWebhook:
		if !isBuiltinWebhook {
			return "", fmt.Errorf("delivery_mode webhook is only valid for the built-in Sonarr/Radarr webhook source (%s/%s)",
				autoscan.BuiltinArrWebhookPluginID, autoscan.BuiltinArrWebhookCapabilityID)
		}
		return autoscan.DeliveryModeWebhook, nil
	default:
		return "", fmt.Errorf("delivery_mode must be poll or webhook")
	}
}

// validateWebhookProvider checks the optional source_config.webhook_provider
// value used by webhook payload parsing.
func validateWebhookProvider(config map[string]string) error {
	provider, ok := config["webhook_provider"]
	if !ok {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "auto", "sonarr", "radarr":
		return nil
	}
	return fmt.Errorf("source_config.webhook_provider must be auto, sonarr, or radarr")
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
	DeliveryMode        string                 `json:"delivery_mode"`
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

// --- Rewrite suggestions (sync from arr) ---

// --- Trigger ---

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
	DeliveryMode    string                         `json:"delivery_mode"`
	ProviderEvent   string                         `json:"provider_event_type,omitempty"`
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

type autoscanStatusResponse = AdminAutoscanStatusView

type AdminAutoscanStatusView struct {
	Enabled       bool                          `json:"enabled"`
	Sources       []autoscanStatusSource        `json:"sources"`
	RunningPolls  []autoscanRunningPollResponse `json:"running_polls"`
	ActiveScans   int                           `json:"active_scans"`
	AcceptedScans int                           `json:"accepted_scans"`
	RunningScans  int                           `json:"running_scans"`
	LatestEventAt *time.Time                    `json:"latest_event_at,omitempty"`
}

// ReadAdminAutoscanStatus retains the existing sequential queue/source observations.
func (h *AutoscanHandler) ReadAdminAutoscanStatus(ctx context.Context) (AdminAutoscanStatusView, error) {
	s, err := h.repo.GetSettings(ctx)
	if err != nil {
		return AdminAutoscanStatusView{}, err
	}
	sources, err := h.repo.ListSources(ctx)
	if err != nil {
		return AdminAutoscanStatusView{}, err
	}
	queue, err := h.repo.GetQueueSummary(ctx)
	if err != nil {
		return AdminAutoscanStatusView{}, err
	}
	runningEvents, err := h.repo.ListRunningEvents(ctx)
	if err != nil {
		return AdminAutoscanStatusView{}, err
	}
	latestEventAt, err := h.repo.LatestEventAt(ctx)
	if err != nil {
		return AdminAutoscanStatusView{}, err
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
	return autoscanStatusResponse{
		Enabled:       s.Enabled,
		Sources:       trimmed,
		RunningPolls:  runningPolls,
		ActiveScans:   queue.Active,
		AcceptedScans: queue.Accepted,
		RunningScans:  queue.Running,
		LatestEventAt: latestEventAt,
	}, nil
}

// ReadAdminAutoscanAvailableSources reuses discovery without reading source secrets or invoking a provider.
func (h *AutoscanHandler) ReadAdminAutoscanAvailableSources(ctx context.Context) ([]autoscan.AvailableScanSource, error) {
	return h.svc.ListAvailableScanSources(ctx)
}

// ReadAdminAutoscanRewriteSuggestions preserves the existing synchronous provider read.
func (h *AutoscanHandler) ReadAdminAutoscanRewriteSuggestions(ctx context.Context, id string) (autoscan.RewriteSuggestions, error) {
	return h.svc.SuggestRewrites(ctx, id)
}

// AdminAutoscanConnectionTestInput carries the existing advisory test intent.
type AdminAutoscanConnectionTestInput = autoscanTestConnectionInput

var ErrAdminAutoscanConnectionTestUnavailable = errors.New("autoscan connection test unavailable")

func (h *AutoscanHandler) TestAdminAutoscanConnection(ctx context.Context, in AdminAutoscanConnectionTestInput) (autoscan.ConnectionTestResult, error) {
	if h == nil || h.svc == nil {
		return autoscan.ConnectionTestResult{}, ErrAdminAutoscanConnectionTestUnavailable
	}
	if in.ConnectionID != nil && strings.TrimSpace(*in.ConnectionID) != "" {
		return h.svc.TestConnectionByID(ctx, strings.TrimSpace(*in.ConnectionID))
	}
	return h.svc.TestConnection(ctx, autoscan.Connection{BaseURL: strings.TrimSpace(in.BaseURL), APIKeyRef: strings.TrimSpace(in.APIKeyRef), RequestIntegrationID: in.RequestIntegrationID})
}

// AdminAutoscanConnectionCreateInput retains the existing write-only credential input.
type AdminAutoscanConnectionCreateInput = autoscanConnectionInput

var ErrAdminAutoscanConnectionCreateUnavailable = errors.New("autoscan connection creation unavailable")
var ErrAdminAutoscanConnectionCreateInvalid = errors.New("autoscan connection name and URL or integration are required")

func (h *AutoscanHandler) CreateAdminAutoscanConnection(ctx context.Context, in AdminAutoscanConnectionCreateInput) (AdminAutoscanConnectionView, error) {
	if h == nil || h.repo == nil {
		return AdminAutoscanConnectionView{}, ErrAdminAutoscanConnectionCreateUnavailable
	}
	if strings.TrimSpace(in.Name) == "" || validateConnectionInput(in) != nil {
		return AdminAutoscanConnectionView{}, ErrAdminAutoscanConnectionCreateInvalid
	}
	created, err := h.repo.CreateConnection(ctx, autoscan.Connection{Name: strings.TrimSpace(in.Name), Kind: strings.TrimSpace(in.Kind), BaseURL: strings.TrimSpace(in.BaseURL), APIKeyRef: strings.TrimSpace(in.APIKeyRef), RequestIntegrationID: normalizeRequestIntegrationID(in.RequestIntegrationID)})
	if err != nil {
		return AdminAutoscanConnectionView{}, err
	}
	return connectionResponse(created), nil
}

// AdminAutoscanConnectionUpdateInput retains the existing write-only credential input.
type AdminAutoscanConnectionUpdateInput = autoscanConnectionInput

var ErrAdminAutoscanConnectionUpdateUnavailable = errors.New("autoscan connection update unavailable")
var ErrAdminAutoscanConnectionUpdateInvalid = errors.New("autoscan connection name and URL or integration are required")

func (h *AutoscanHandler) UpdateAdminAutoscanConnection(ctx context.Context, id string, in AdminAutoscanConnectionUpdateInput) (AdminAutoscanConnectionView, error) {
	if h == nil || h.repo == nil {
		return AdminAutoscanConnectionView{}, ErrAdminAutoscanConnectionUpdateUnavailable
	}
	if strings.TrimSpace(in.Name) == "" || validateConnectionInput(in) != nil {
		return AdminAutoscanConnectionView{}, ErrAdminAutoscanConnectionUpdateInvalid
	}
	created, err := h.repo.UpdateConnection(ctx, autoscan.Connection{ID: strings.TrimSpace(id), Name: strings.TrimSpace(in.Name), Kind: strings.TrimSpace(in.Kind), BaseURL: strings.TrimSpace(in.BaseURL), APIKeyRef: strings.TrimSpace(in.APIKeyRef), RequestIntegrationID: normalizeRequestIntegrationID(in.RequestIntegrationID)})
	if err != nil {
		return AdminAutoscanConnectionView{}, err
	}
	return connectionResponse(created), nil
}

var ErrAdminAutoscanConnectionDeleteUnavailable = errors.New("autoscan connection deletion unavailable")

func (h *AutoscanHandler) DeleteAdminAutoscanConnection(ctx context.Context, id string) error {
	if h == nil || h.repo == nil {
		return ErrAdminAutoscanConnectionDeleteUnavailable
	}
	return h.repo.DeleteConnection(ctx, strings.TrimSpace(id))
}

var ErrAdminAutoscanScansUnavailable = errors.New("autoscan scan history unavailable")

func (h *AutoscanHandler) ReadAdminAutoscanScans(ctx context.Context, filter autoscan.ScanListFilter) ([]autoscan.ScanWithEvent, int, error) {
	if h == nil || h.repo == nil {
		return nil, 0, ErrAdminAutoscanScansUnavailable
	}
	rows, err := h.repo.ListAutoscanScans(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	total, err := h.repo.CountAutoscanScans(ctx, filter)
	return rows, total, err
}

var ErrAdminAutoscanEventsUnavailable = errors.New("autoscan event history unavailable")

func (h *AutoscanHandler) ReadAdminAutoscanEvents(ctx context.Context, filter autoscan.EventListFilter) ([]autoscan.EventWithRuns, int, error) {
	if h == nil || h.repo == nil {
		return nil, 0, ErrAdminAutoscanEventsUnavailable
	}
	rows, err := h.repo.ListEvents(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	total, err := h.repo.CountEvents(ctx, filter)
	return rows, total, err
}

var ErrAdminAutoscanSettingsWriteUnavailable = errors.New("autoscan settings writer unavailable")
var ErrAdminAutoscanSettingsWriteInvalid = errors.New("invalid autoscan settings")

const (
	autoscanRescheduleNotConfigured = "not_configured"
	autoscanRescheduleApplied       = "applied"
	autoscanRescheduleFailed        = "failed"
)

type AdminAutoscanSettingsWriteView struct {
	Settings        autoscan.Settings
	RescheduleState string
}

func (h *AutoscanHandler) UpdateAdminAutoscanSettings(ctx context.Context, input autoscan.Settings) (AdminAutoscanSettingsWriteView, error) {
	if h == nil || h.repo == nil {
		return AdminAutoscanSettingsWriteView{}, ErrAdminAutoscanSettingsWriteUnavailable
	}
	if input.DefaultPollIntervalSeconds <= 0 || input.DefaultPollIntervalSeconds > 2147483647 || input.DebounceSeconds < 0 || input.DebounceSeconds > 2147483647 {
		return AdminAutoscanSettingsWriteView{}, ErrAdminAutoscanSettingsWriteInvalid
	}
	updated, err := h.repo.UpdateSettings(ctx, input)
	if err != nil {
		return AdminAutoscanSettingsWriteView{}, err
	}
	out := AdminAutoscanSettingsWriteView{Settings: updated, RescheduleState: autoscanRescheduleNotConfigured}
	if h.triggers != nil {
		out.RescheduleState = autoscanRescheduleApplied
		if err := h.triggers.UpdateTriggers(autoscanPollTaskKey, []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(updated.DefaultPollIntervalSeconds) * 1000}}); err != nil {
			out.RescheduleState = autoscanRescheduleFailed
		}
	}
	return out, nil
}

var ErrAdminAutoscanSourceDeleteUnavailable = errors.New("autoscan source deletion unavailable")

func (h *AutoscanHandler) DeleteAdminAutoscanSource(ctx context.Context, id string) error {
	if h == nil || h.repo == nil {
		return ErrAdminAutoscanSourceDeleteUnavailable
	}
	return h.repo.DeleteSource(ctx, strings.TrimSpace(id))
}
