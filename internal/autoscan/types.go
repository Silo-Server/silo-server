package autoscan

import "time"

// Settings is the global autoscan configuration (singleton row).
type Settings struct {
	Enabled                    bool
	DefaultPollIntervalSeconds int
	DebounceSeconds            int
}

// Connection is an arr server the host can reach: either own credentials, or a
// live reference to a Requests integration (RequestIntegrationID set).
type Connection struct {
	ID                   string
	Name                 string
	Kind                 string
	BaseURL              string // own; empty when linked
	APIKeyRef            string // own; empty when linked
	RequestIntegrationID *string
}

// PathRewrite is a per-source prefix translation from a raw arr/source-namespace
// path to a Silo-native path. The merged scan_source contract moved rewrite
// ownership from the plugin to the host, so these are applied host-side before a
// raw path is resolved/enqueued.
type PathRewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ChangeScope tells the host how to interpret a scan_source path.
type ChangeScope string

const (
	ChangeScopeAuto    ChangeScope = "auto"
	ChangeScopeFile    ChangeScope = "file"
	ChangeScopeSubtree ChangeScope = "subtree"
)

// Change is one raw provider/source-namespace path returned by a scan_source
// plugin. The host applies PathRewrites before resolving the changed path.
type Change struct {
	SourcePath string      `json:"source_path"`
	Scope      ChangeScope `json:"scope"`
}

// DeliveryMode selects how a source's changes reach the host: the host polls
// the plugin (`poll`, the default), or the provider POSTs to a Silo webhook
// endpoint (`webhook`) and the plugin is never invoked.
const (
	DeliveryModePoll    = "poll"
	DeliveryModeWebhook = "webhook"
)

// Source ties a scan_source plugin capability to a connection plus the
// host-owned scheduling/bookkeeping state.
type Source struct {
	ID                  string
	PluginID            string
	CapabilityID        string
	ConnectionID        *string // nil until an operator binds a connection
	Enabled             bool
	DeliveryMode        string        // DeliveryModePoll or DeliveryModeWebhook
	PollIntervalSeconds *int          // nil => use settings default
	PathRewrites        []PathRewrite // host-owned raw->Silo prefix rewrites
	SourceConfig        map[string]string
	Label               string  // operator-set display label; "" = unset
	Marker              *string // opaque; nil on first run
	LastRunAt           *time.Time
	LastError           *string
}

// WebhookEndpoint is the admin-visible state of a source's webhook endpoint.
// The plaintext token and its lookup hash never live on this struct; creation
// and rotation return the token separately, and redisplay goes through
// RevealWebhookToken.
type WebhookEndpoint struct {
	SourceID         string
	SecretSuffix     string
	CreatedAt        time.Time
	RotatedAt        *time.Time
	LastReceivedAt   *time.Time
	LastErrorAt      *time.Time
	LastErrorMessage string
}

// WebhookDelivery is one durably accepted ARR webhook delivery waiting to be
// consumed. LockedBy is an opaque lease owner; repository completion/failure
// updates are ownership-guarded so an expired worker cannot finalize a delivery
// that another node reclaimed.
type WebhookDelivery struct {
	ID                int64
	SourceID          string
	ProviderEventType string
	Changes           []Change
	ReceivedAt        time.Time
	AttemptCount      int
	LockedBy          string
}

type EventStatus string

const (
	EventStatusRunning    EventStatus = "running"
	EventStatusSuccess    EventStatus = "success"
	EventStatusError      EventStatus = "error"
	EventStatusUnresolved EventStatus = "unresolved"
)

type Event struct {
	ID                int64
	SourceID          *string
	PluginID          string
	CapabilityID      string
	StartedAt         time.Time
	CompletedAt       time.Time
	DurationMS        int64
	Status            EventStatus
	DeliveryMode      string
	ProviderEventType string
	ChangesReturned   int
	ChangesResolved   int
	TargetsClaimed    int
	ScansCreated      int
	ScansReused       int
	ScansSuppressed   int
	ErrorMessage      string
	MarkerBefore      *string
	MarkerAfter       *string
}

type EventCreate struct {
	SourceID          string
	PluginID          string
	CapabilityID      string
	StartedAt         time.Time
	MarkerBefore      string
	DeliveryMode      string // "" defaults to DeliveryModePoll
	ProviderEventType string
	// SkipRunningCheck bypasses the running-event single-flight exclusion.
	// Poll cycles may be skipped when an event is already running (their
	// marker window is re-read next cycle); webhook deliveries carry no
	// marker, so dropping one loses it forever — they always set this.
	SkipRunningCheck bool
}

type EventFinish struct {
	ID              int64
	CompletedAt     time.Time
	Status          EventStatus
	ChangesReturned int
	ChangesResolved int
	TargetsClaimed  int
	ScansCreated    int
	ScansReused     int
	ScansSuppressed int
	ErrorMessage    string
	MarkerAfter     string
}

type EnqueueResult struct {
	Created int
	Reused  int
}

type EventListFilter struct {
	SourceID string
	Status   EventStatus
	Search   string
	Limit    int
	Offset   int
}

type ScanListFilter struct {
	Status string
	Search string
	Limit  int
	Offset int
}

type EventWithRuns struct {
	Event Event
	Runs  []ScanRunSummary
}

type ScanRunSummary struct {
	ID            string
	MediaFolderID int
	Mode          string
	Path          string
	Trigger       string
	Status        string
	ErrorMessage  string
	RequestedAt   *time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
}

type ScanWithEvent struct {
	ScanRunSummary
	AutoscanEventID  *int64
	SourceID         *string
	PluginID         string
	CapabilityID     string
	EventStatus      EventStatus
	EventCompletedAt *time.Time
}

type QueueSummary struct {
	Active   int
	Accepted int
	Running  int
}
