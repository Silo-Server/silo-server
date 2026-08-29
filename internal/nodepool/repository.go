package nodepool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// NodeTypeProxy identifies a proxy stream node.
	NodeTypeProxy = "proxy"
	// NodeTypeTranscode identifies a transcode stream node.
	NodeTypeTranscode = "transcode"
)

// Node represents a stream node in the database.
type Node struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	// PublicURL is the base URL streaming clients are given for this node,
	// when it differs from URL. URL is the backend address — what the server
	// and other nodes dial — so on a split network a proxy carries its
	// private address in URL and its client-facing one here. Nil means
	// clients use URL. Only ever read for proxy nodes: clients never talk to
	// transcode nodes.
	PublicURL        *string    `json:"public_url,omitempty"`
	Healthy          bool       `json:"healthy"`
	ActiveJobs       int        `json:"active_jobs"`
	Group            *string    `json:"group"`              // co-location group; nil = ungrouped
	MaxJobs          *int       `json:"max_jobs"`           // concurrent job cap; nil = unlimited
	MaxBandwidthKbps *int       `json:"max_bandwidth_kbps"` // egress cap in kilobits/s; nil = unlimited
	EgressKbps       int        `json:"egress_kbps"`        // health-reported rolling egress average
	LastHealthCheck  *time.Time `json:"last_health_check"`
	CreatedAt        time.Time  `json:"created_at"`
	// Capabilities is the node's last stored capability report, verbatim as the
	// node served it. Kept opaque here: nodepool must not depend on playback,
	// and readers that need fields parse the ones they need.
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	// CapabilitiesHash identifies Capabilities. The health sweep compares it
	// against the hash a node reports to decide whether to refetch.
	CapabilitiesHash *string `json:"capabilities_hash,omitempty"`
	// CapabilitiesRefreshedAt is when Capabilities was last fetched — the age of
	// the inventory, not of the last health check.
	CapabilitiesRefreshedAt *time.Time `json:"capabilities_refreshed_at,omitempty"`
	// LastStats is the node's resource sample from the last health check —
	// {"system":…,"gpu":…} — kept opaque for the same reason as Capabilities.
	// It is written by the same 30s health update that writes ActiveJobs, so it
	// is exactly as fresh as LastHealthCheck and never fresher. Absent for a
	// node that reports no sample.
	LastStats json.RawMessage `json:"last_stats,omitempty"`
	// HWAccelOverride and HWDeviceOverride are this node's own acceleration
	// policy. nil means the node inherits the cluster-wide playback.hw_accel /
	// playback.hw_device settings, which is the normal case; a value here is
	// what the node itself resolves against once it has reloaded its config.
	HWAccelOverride  *string `json:"hw_accel_override,omitempty"`
	HWDeviceOverride *string `json:"hw_device_override,omitempty"`
	// CapabilityDrift is an operator-facing note describing how this node's
	// hardware got worse at the last capability refetch: a backend that used to
	// pass its probe and now fails, or a render device that is gone. nil means
	// the last refetch found no regression, which is also how a recovered node
	// reads — the note is rewritten by every refetch and a clean report clears
	// it. It is written beside Capabilities in one statement, so it always
	// describes the report stored with it, and nothing routes on it.
	CapabilityDrift *string `json:"capability_drift,omitempty"`
	// CapabilityDriftBaseline records, machine-readably, the backends and device
	// identities CapabilityDrift is waiting on. Recovery cannot be derived from
	// the stored report alone — once a degraded report is stored every later
	// comparison is degraded-to-degraded — so the note keeps what it is standing
	// for. Non-nil exactly when CapabilityDrift is, except on a note written
	// before this column existed.
	CapabilityDriftBaseline json.RawMessage `json:"capability_drift_baseline,omitempty"`
	// AdvertisedCapabilitiesHash is the hash the node named on its last health
	// check, which is not always the one stored beside it: the sweep refetches
	// on a mismatch, and a refetch that keeps failing leaves the two apart while
	// the health check goes on succeeding every 30 seconds. Derived per sweep
	// rather than persisted — it is an observation about right now, and a
	// restarted API re-learns it on its first check.
	//
	// A pointer for three states, not two. nil is "nobody has checked this node
	// yet", which every node reads as until the first sweep after a restart, and
	// which says nothing about the stored report. A pointer to "" is the health
	// check answering with no hash at all — a node downgraded to a build that
	// predates capability reports — and that is a node no longer standing behind
	// what is stored for it, which a reader must be able to tell from silence.
	AdvertisedCapabilitiesHash *string `json:"advertised_capabilities_hash,omitempty"`
	// PhysicalGPUKeys identifies the actual GPUs behind this node, derived from
	// Capabilities rather than stored: it is a pure function of that payload, so
	// a column would only be a second copy that can disagree with it. Two nodes
	// sharing a key are backed by the same card — the case that makes
	// independent per-node capacity accounting wrong, and which no single node's
	// report can express. Last in the struct so it stays last on the wire, where
	// the admin node list has always carried it.
	PhysicalGPUKeys []string `json:"physical_gpu_keys,omitempty"`
}

// EffectiveHWAccel is the acceleration backend this node runs under: its own
// override when it carries one, and otherwise the cluster-wide setting passed
// in. It is what a dispatch path names in a start request so the request, the
// recipe card, and what the node actually runs agree.
//
// Deliberately not derived from the node's stored capability report: that
// report is a snapshot up to a capability-refresh interval old, and naming its
// resolved backend would pin a stale answer *and* suppress the node's own
// start-time resolution — a node honors a named backend verbatim, so "auto"
// has to survive this far to reach live device enumeration on the node.
func (n *Node) EffectiveHWAccel(clusterHWAccel string) string {
	if n == nil || n.HWAccelOverride == nil {
		return clusterHWAccel
	}
	if override := strings.TrimSpace(*n.HWAccelOverride); override != "" {
		return override
	}
	return clusterHWAccel
}

// ClientURL is the base URL to hand streaming clients for this node: the
// public URL when one is set, otherwise the backend URL — which is every node
// registered before the split and every deployment with one flat network.
// Normalized like every other node URL so builders can join paths directly.
func (n *Node) ClientURL() string {
	if n == nil {
		return ""
	}
	if n.PublicURL != nil {
		if public := strings.TrimSpace(*n.PublicURL); public != "" {
			return normalizeNodeURL(public)
		}
	}
	return normalizeNodeURL(n.URL)
}

// StoredCapabilities returns this node's last stored capability report, nil-safe
// like the Effective* accessors so a caller whose lookup came up empty prices a
// missing node and a missing report through one path.
func (n *Node) StoredCapabilities() json.RawMessage {
	if n == nil {
		return nil
	}
	return n.Capabilities
}

// EffectiveHWDevice is the device set this node runs under, resolved the same
// way as EffectiveHWAccel.
//
// Its readers care about the size of the set, not the paths: how many devices a
// node walks is what decides how long its cold capability probe takes, and a
// node overridden onto four devices needs several times the budget the cluster
// setting would price for it.
func (n *Node) EffectiveHWDevice(clusterHWDevice string) string {
	if n == nil || n.HWDeviceOverride == nil {
		return clusterHWDevice
	}
	if override := strings.TrimSpace(*n.HWDeviceOverride); override != "" {
		return override
	}
	return clusterHWDevice
}

// CreateNodeInput holds the fields for creating a new node.
type CreateNodeInput struct {
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url"`
	// PublicURL is meaningful for proxy nodes only; see Node.PublicURL.
	// Accepted on any node for symmetry with the acceleration overrides,
	// which are likewise scoped by what reads them rather than rejected.
	PublicURL        string `json:"public_url"`
	Group            string `json:"group"`              // empty = ungrouped
	MaxJobs          *int   `json:"max_jobs"`           // nil or <= 0 = unlimited
	MaxBandwidthKbps *int   `json:"max_bandwidth_kbps"` // nil or <= 0 = unlimited
}

// Validate checks required fields and allowed values.
func (i CreateNodeInput) Validate() error {
	if i.Name == "" {
		return errors.New("name is required")
	}
	if i.Type != NodeTypeProxy && i.Type != NodeTypeTranscode {
		return fmt.Errorf("type must be %q or %q", NodeTypeProxy, NodeTypeTranscode)
	}
	if i.URL == "" {
		return errors.New("url is required")
	}
	return nil
}

// UpdateNodeInput holds the fields for updating a node.
// The optional fields distinguish "leave unchanged" (nil) from "clear":
// an empty-string Group clears the group, an empty-string HWAccelOverride or
// HWDeviceOverride restores inheritance of the cluster-wide setting, and a
// non-positive MaxJobs or MaxBandwidthKbps clears that cap.
type UpdateNodeInput struct {
	Name *string `json:"name,omitempty"`
	URL  *string `json:"url,omitempty"`
	// PublicURL follows the override convention: empty string (or JSON null)
	// clears the column, so clients go back to using the backend URL.
	PublicURL        *string `json:"public_url,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
	Group            *string `json:"group,omitempty"`
	MaxJobs          *int    `json:"max_jobs,omitempty"`
	MaxBandwidthKbps *int    `json:"max_bandwidth_kbps,omitempty"`
	HWAccelOverride  *string `json:"hw_accel_override,omitempty"`
	HWDeviceOverride *string `json:"hw_device_override,omitempty"`
}

// UnmarshalJSON decodes an update body, mapping an explicit JSON null on the
// two acceleration overrides onto the empty-string clear sentinel the rest of
// this type uses. Plain decoding leaves a *string nil for an omitted field and
// for an explicit null alike, which would silently turn "go back to inheriting
// the cluster setting" into a no-op. Every other field decodes normally.
func (i *UpdateNodeInput) UnmarshalJSON(data []byte) error {
	type plain UpdateNodeInput
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*i = UpdateNodeInput(decoded)
	if isJSONNull(raw["hw_accel_override"]) {
		i.HWAccelOverride = new(string)
	}
	if isJSONNull(raw["hw_device_override"]) {
		i.HWDeviceOverride = new(string)
	}
	if isJSONNull(raw["public_url"]) {
		i.PublicURL = new(string)
	}
	return nil
}

// isJSONNull reports whether a field was present in the body with the literal
// value null (an absent field decodes to a nil RawMessage instead).
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// Validate checks the values an update may set. Only the acceleration override
// has a closed set of values; the database CHECK enforces the same list, and
// rejecting here turns a constraint violation into an operator-readable error.
func (i UpdateNodeInput) Validate() error {
	if i.HWAccelOverride == nil {
		return nil
	}
	value := normalizeHWAccelOverride(*i.HWAccelOverride)
	if value == nil {
		// The clear sentinel: inherit the cluster-wide setting again.
		return nil
	}
	if !slices.Contains(hwAccelOverrideValues, *value) {
		return fmt.Errorf("%w: hw_accel_override must be one of %s", ErrInvalidNodeInput,
			strings.Join(hwAccelOverrideValues, ", "))
	}
	return nil
}

// hwAccelOverrideValues mirrors the playback.hw_accel enum in
// internal/config/admin_settings.go and the CHECK constraint on
// stream_nodes.hw_accel_override: a per-node override may only name a backend
// the cluster-wide setting could also name.
var hwAccelOverrideValues = []string{hwAccelAuto, hwAccelQSV, hwAccelVAAPI, hwAccelNVENC, hwAccelNone}

const (
	// hwAccelAuto asks the node to resolve its own backend against live
	// hardware at session start; dispatch passes it through untouched for
	// exactly that reason.
	hwAccelAuto  = "auto"
	hwAccelQSV   = "qsv"
	hwAccelVAAPI = "vaapi"
	hwAccelNVENC = "nvenc"
	hwAccelNone  = "none"
)

// sameURL is true when an update leaves the row addressing the same worker.
// Trailing slashes are ignored on both sides because the pools normalize URLs
// and the column does not, so "http://n1/" becoming "http://n1" is not a move.
const sameURL = `rtrim(COALESCE($3, url), '/') = rtrim(url, '/')`

// normalizeGroup trims a group label and converts empty to NULL.
func normalizeGroup(group string) *string {
	g := strings.TrimSpace(group)
	if g == "" {
		return nil
	}
	return &g
}

// normalizeOverride trims an override value and converts empty to NULL, which
// is how a node goes back to inheriting the cluster-wide setting. Case is
// preserved: a render device path is a filesystem path, not an enum.
func normalizeOverride(value string) *string {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil
	}
	return &v
}

// normalizeHWAccelOverride is normalizeOverride for the acceleration enum,
// which is also lowercased. The cluster-wide playback.hw_accel accepts any
// casing (config.normalizeAdminEnum lowercases before comparing), and
// docs/admin-api.md promises the override takes the same values, so "QSV" from
// a third-party admin client must not be a 400 here when it is a 200 there.
func normalizeHWAccelOverride(value string) *string {
	return normalizeOverride(strings.ToLower(value))
}

// normalizeCap converts non-positive capacity values to NULL (unlimited).
func normalizeCap(v *int) *int {
	if v == nil || *v <= 0 {
		return nil
	}
	return v
}

// Repository provides CRUD operations for stream nodes.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a new node repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const nodeColumns = `id, name, type, url, public_url, enabled, healthy, active_jobs, node_group, max_jobs, max_bandwidth_kbps, egress_kbps, last_health_check, created_at, capabilities, capabilities_hash, capabilities_refreshed_at, last_stats, hw_accel_override, hw_device_override, capability_drift, capability_drift_baseline`

func scanNode(row pgx.Row) (*Node, error) {
	var n Node
	// jsonb is scanned as raw bytes rather than into json.RawMessage directly so
	// a NULL column stays nil instead of decoding through the JSON codec.
	var capabilities, lastStats, driftBaselineBytes []byte
	err := row.Scan(
		&n.ID, &n.Name, &n.Type, &n.URL, &n.PublicURL,
		&n.Enabled, &n.Healthy, &n.ActiveJobs,
		&n.Group, &n.MaxJobs,
		&n.MaxBandwidthKbps, &n.EgressKbps,
		&n.LastHealthCheck, &n.CreatedAt,
		&capabilities, &n.CapabilitiesHash, &n.CapabilitiesRefreshedAt,
		&lastStats,
		&n.HWAccelOverride, &n.HWDeviceOverride,
		&n.CapabilityDrift, &driftBaselineBytes,
	)
	if err != nil {
		return nil, err
	}
	if len(capabilities) > 0 {
		n.Capabilities = json.RawMessage(capabilities)
	}
	if len(lastStats) > 0 {
		n.LastStats = json.RawMessage(lastStats)
	}
	if len(driftBaselineBytes) > 0 {
		n.CapabilityDriftBaseline = json.RawMessage(driftBaselineBytes)
	}
	// Derived here so every reader of a stored row — the admin listing as much
	// as a pool load — sees the same identities without parsing the payload
	// again for itself.
	applyPhysicalGPUKeys(&n)
	return &n, nil
}

func scanNodes(rows pgx.Rows) ([]*Node, error) {
	var nodes []*Node
	for rows.Next() {
		// pgx.Rows satisfies pgx.Row, so both paths share one column list.
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// List returns all nodes ordered by type then name.
func (r *Repository) List(ctx context.Context) ([]*Node, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+nodeColumns+` FROM stream_nodes ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// ListEnabled returns all enabled nodes of a given type.
func (r *Repository) ListEnabled(ctx context.Context, nodeType string) ([]*Node, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+nodeColumns+` FROM stream_nodes WHERE type = $1 AND enabled = true ORDER BY name`,
		nodeType)
	if err != nil {
		return nil, fmt.Errorf("list enabled nodes: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

// GetByID returns a single node by ID.
func (r *Repository) GetByID(ctx context.Context, id int) (*Node, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+nodeColumns+` FROM stream_nodes WHERE id = $1`, id)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNodeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	return n, nil
}

// Create inserts a new node and returns it.
func (r *Repository) Create(ctx context.Context, input CreateNodeInput) (*Node, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	row := r.pool.QueryRow(ctx,
		`INSERT INTO stream_nodes (name, type, url, public_url, node_group, max_jobs, max_bandwidth_kbps)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+nodeColumns,
		input.Name, input.Type, input.URL, normalizeOverride(input.PublicURL), normalizeGroup(input.Group),
		normalizeCap(input.MaxJobs), normalizeCap(input.MaxBandwidthKbps))
	return scanNode(row)
}

// Update modifies a node's mutable fields. The optional fields use sentinel
// values to clear: an empty-string group, an empty-string acceleration
// override, and non-positive caps set the column to NULL (see UpdateNodeInput).
func (r *Repository) Update(ctx context.Context, id int, input UpdateNodeInput) (*Node, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	var group *string
	if input.Group != nil {
		group = normalizeGroup(*input.Group)
	}
	var maxJobs, maxBandwidth *int
	if input.MaxJobs != nil {
		maxJobs = normalizeCap(input.MaxJobs)
	}
	if input.MaxBandwidthKbps != nil {
		maxBandwidth = normalizeCap(input.MaxBandwidthKbps)
	}
	var hwAccelOverride, hwDeviceOverride *string
	if input.HWAccelOverride != nil {
		hwAccelOverride = normalizeHWAccelOverride(*input.HWAccelOverride)
	}
	if input.HWDeviceOverride != nil {
		hwDeviceOverride = normalizeOverride(*input.HWDeviceOverride)
	}
	var publicURL *string
	if input.PublicURL != nil {
		publicURL = normalizeOverride(*input.PublicURL)
	}
	row := r.pool.QueryRow(ctx,
		`UPDATE stream_nodes SET
			name = COALESCE($2, name),
			url = COALESCE($3, url),
			enabled = COALESCE($4, enabled),
			node_group = CASE WHEN $5::boolean THEN $6::text ELSE node_group END,
			max_jobs = CASE WHEN $7::boolean THEN $8::integer ELSE max_jobs END,
			max_bandwidth_kbps = CASE WHEN $9::boolean THEN $10::integer ELSE max_bandwidth_kbps END,
			hw_accel_override = CASE WHEN $11::boolean THEN $12::text ELSE hw_accel_override END,
			hw_device_override = CASE WHEN $13::boolean THEN $14::text ELSE hw_device_override END,
			public_url = CASE WHEN $15::boolean THEN $16::text ELSE public_url END,
			-- Everything below describes the worker the old URL addressed, so
			-- repointing the row at a different machine has to drop it. The
			-- caller publishes the returned row to the pools immediately, and
			-- these are exactly the fields placement reads: the GPU identities
			-- behind physical_gpu_keys and the scratch fill behind admission.
			-- Keeping them would route work onto the replacement using its
			-- predecessor's hardware until a health check and a capability
			-- fetch caught up. NULL is the same state a freshly registered node
			-- is in, which is the truth here.
			capabilities = CASE WHEN `+sameURL+` THEN capabilities END,
			capabilities_hash = CASE WHEN `+sameURL+` THEN capabilities_hash END,
			capabilities_refreshed_at = CASE WHEN `+sameURL+` THEN capabilities_refreshed_at END,
			last_stats = CASE WHEN `+sameURL+` THEN last_stats END,
			capability_drift = CASE WHEN `+sameURL+` THEN capability_drift END,
			capability_drift_baseline = CASE WHEN `+sameURL+` THEN capability_drift_baseline END
		 WHERE id = $1
		 RETURNING `+nodeColumns,
		id, input.Name, input.URL, input.Enabled,
		input.Group != nil, group,
		input.MaxJobs != nil, maxJobs,
		input.MaxBandwidthKbps != nil, maxBandwidth,
		input.HWAccelOverride != nil, hwAccelOverride,
		input.HWDeviceOverride != nil, hwDeviceOverride,
		input.PublicURL != nil, publicURL)
	n, err := scanNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNodeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update node: %w", err)
	}
	return n, nil
}

// Delete removes a node by ID.
func (r *Repository) Delete(ctx context.Context, id int) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM stream_nodes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete node: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// UpdateHealth updates a node's health status, active job count, reported
// egress bandwidth, and last resource sample.
//
// A nil lastStats writes NULL, which is what a node that reports no sample —
// an older build, or a non-Linux host — must produce. Passing the previous
// value through instead would leave a dead node's numbers on screen looking
// current.
// checkedURL fences the write the same way UpdateCapabilities does. The window
// is smaller — a health request is bounded at five seconds — but the
// consequence is not: last_stats carries the scratch fill that transcode
// admission reads, so one worker's disk reading landing on a row that now
// addresses another can exclude a healthy node or admit a full one.
func (r *Repository) UpdateHealth(ctx context.Context, id int, checkedURL string, healthy bool, activeJobs, egressKbps int, lastStats []byte) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE stream_nodes SET healthy = $2, active_jobs = $3, egress_kbps = $4, last_stats = $5, last_health_check = NOW()
		 WHERE id = $1 AND rtrim(url, '/') = rtrim($6, '/')`,
		id, healthy, activeJobs, egressKbps, lastStats, checkedURL)
	if err != nil {
		return fmt.Errorf("update node health: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNodeMoved
	}
	return nil
}

// UpdateCapabilities persists a freshly fetched capability report together with
// the hash that identifies it and the drift note comparing it against the
// previous one. The four columns are written in one statement so a reader never
// sees a payload beside a hash — or a drift note — from a different report.
//
// A nil drift writes NULL, which is how a node that has recovered stops being
// flagged: the note describes the last comparison, not a latched incident, so
// carrying the previous value forward would keep a repaired driver on screen as
// broken.
// fetchedFrom fences the write against the row having been repointed while the
// fetch was in flight. A capability fetch runs detached from the sweep and is
// bounded at two minutes, which is ample time for an administrator to edit the
// node's URL; an id-only write would then store one worker's GPU identities on
// a row that now addresses a different machine, and the planner would place
// shared-GPU work on that reading until another sweep corrected it. Trailing
// slashes are ignored on both sides because the pools normalize URLs and the
// column does not.
// It is also fenced on the report it believes it is replacing. Every API
// replica runs its own health sweep, so two can fetch successive reports from
// one node concurrently; without this a slower fetch of an older report lands
// after a newer one and overwrites it, taking the durable GPU identities and
// drift state back with it until some later sweep repairs them. Comparing
// against the hash the caller read before fetching makes the write a
// compare-and-set: whichever replica gets there first wins, and the loser
// discards a report that no longer describes the row it was derived from.
// Clock skew between replicas does not enter into it.
func (r *Repository) UpdateCapabilities(ctx context.Context, id int, fetchedFrom string, capabilities []byte, hash string, refreshedAt time.Time, drift *string, driftBaseline []byte, replacing *string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE stream_nodes SET capabilities = $2, capabilities_hash = $3, capabilities_refreshed_at = $4, capability_drift = $5, capability_drift_baseline = $7
		 WHERE id = $1 AND rtrim(url, '/') = rtrim($6, '/') AND capabilities_hash IS NOT DISTINCT FROM $8`,
		id, capabilities, hash, refreshedAt, drift, fetchedFrom, driftBaseline, replacing)
	if err != nil {
		return fmt.Errorf("update node capabilities: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Three ways to get here, and they do not mean the same thing. The row
		// being gone or repointed is terminal for this payload; another replica
		// having stored a different report is not — that replica's answer is the
		// current one, and this one's in-memory copy is now behind it. Telling
		// them apart costs one read and saves a replica sweeping forever against
		// a hash the row no longer has.
		current, err := r.GetByID(ctx, id)
		if err != nil || current == nil || !sameStoredURL(current.URL, fetchedFrom) {
			return ErrNodeMoved
		}
		return ErrCapabilitiesSuperseded
	}
	return nil
}

// sameStoredURL compares a stored node URL with the one a payload was fetched
// from, ignoring the trailing slash the pools normalize and the column does not.
func sameStoredURL(stored, fetchedFrom string) bool {
	return strings.TrimRight(stored, "/") == strings.TrimRight(fetchedFrom, "/")
}

// Sentinel errors.
var (
	ErrNodeNotFound = errors.New("stream node not found")
	// ErrInvalidNodeInput marks a caller-supplied value the store refuses, so
	// an API layer can answer 400 without string-matching the message.
	ErrInvalidNodeInput = errors.New("invalid node input")
	// ErrNodeMoved reports that a row no longer matches the identity a
	// long-running fetch was made against — it was deleted, or its URL was
	// edited to address a different worker. The result must be discarded rather
	// than published against whatever the row is now.
	ErrNodeMoved = errors.New("stream node no longer matches the fetched identity")

	// ErrCapabilitiesSuperseded reports that the row still addresses this
	// worker but another writer stored a different report first. The payload is
	// discarded; unlike ErrNodeMoved the caller has something to learn from the
	// row, because the report now on it is newer than the one it started from.
	ErrCapabilitiesSuperseded = errors.New("stream node capabilities were superseded by another writer")
)
