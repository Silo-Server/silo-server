package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type AdminNodesReadService interface {
	ReadAdminNodes(context.Context) ([]*nodepool.Node, error)
}
type AdminNode struct {
	ConfigETag                       string          `json:"config_etag,omitempty" doc:"Original stored configuration validator for guarded edit/delete; excludes live health samples."`
	ID                               ID              `json:"id"`
	Name                             string          `json:"name"`
	Type                             string          `json:"type" enum:"proxy,transcode"`
	URL                              string          `json:"url" doc:"Node address as configured; not validated as a URI"`
	Enabled                          bool            `json:"enabled"`
	PublicURL                        *string         `json:"public_url,omitempty" doc:"Client-facing node address as configured; not validated as a URI"`
	Healthy                          bool            `json:"healthy"`
	ActiveJobs                       int             `json:"active_jobs"`
	Group                            *string         `json:"group"`
	MaxJobs                          *int            `json:"max_jobs"`
	MaxBandwidthKbps                 *int            `json:"max_bandwidth_kbps"`
	EgressKbps                       int             `json:"egress_kbps"`
	LastHealthCheck                  NullableInstant `json:"last_health_check"`
	CreatedAt                        Instant         `json:"created_at"`
	Capabilities                     json.RawMessage `json:"capabilities,omitempty" doc:"Last stored worker capability document, preserving its owning protocol schema."`
	CapabilitiesHash                 *string         `json:"capabilities_hash,omitempty"`
	CapabilitiesRefreshedAt          *Instant        `json:"capabilities_refreshed_at,omitempty"`
	LastStats                        json.RawMessage `json:"last_stats,omitempty" doc:"Worker resource sample stored at the last health check."`
	HWAccelOverride                  *string         `json:"hw_accel_override,omitempty"`
	HWDeviceOverride                 *string         `json:"hw_device_override,omitempty"`
	DownloadArtifactDirOverride      *string         `json:"download_artifact_dir_override,omitempty" doc:"This node's own prepared-download directory; absent when inherited."`
	DownloadArtifactMaxBytesOverride *int64          `json:"download_artifact_max_bytes_override,omitempty" doc:"This node's own prepared-download budget in bytes (0 for none); absent when inherited."`
	CapabilityDrift                  *string         `json:"capability_drift,omitempty"`
	CapabilityDriftBaseline          json.RawMessage `json:"capability_drift_baseline,omitempty"`
	AdvertisedCapabilitiesHash       *string         `json:"advertised_capabilities_hash,omitempty" doc:"Absent when not checked in this process; empty when checked but no hash was advertised."`
	PhysicalGPUKeys                  []string        `json:"physical_gpu_keys,omitempty"`
	DrainRequested                   bool            `json:"drain_requested" doc:"An administrator asked this transcode node to drain: new work is not placed on it while its sessions finish."`
	DrainState                       string          `json:"drain_state" enum:"none,draining,drained" doc:"What the node reported on its last health check: draining while it refuses new work (after a drain request or SIGTERM), drained once it has none left and can be stopped without cutting a stream."`
	// NetworkAccess is keyed by provider slug ("tailscale"). Only proxy nodes
	// report it; a transcode node never does because clients never reach one.
	NetworkAccess map[string]AdminNodeNetworkAccess `json:"network_access,omitempty" doc:"Last network access provider status the node reported on its health check, keyed by provider slug. Omitted when the node reports no providers."`
}

// AdminNodeNetworkAccess is one provider's status as the node last reported
// it. Stream URLs for clients arriving through that provider are built on
// origin while state is connected.
type AdminNodeNetworkAccess struct {
	State     string   `json:"state" doc:"disconnected | awaiting_authorization | connecting | connected | error"`
	Origin    string   `json:"origin,omitempty" doc:"scheme://host[:port] clients on the provider's overlay use to reach this node. Only used while state is connected."`
	Hostname  string   `json:"hostname,omitempty" doc:"Overlay DNS name of the node."`
	UpdatedAt *Instant `json:"updated_at,omitempty" doc:"When the node last heard from the provider, on the node's clock."`
}
type AdminNodesListInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}
type AdminNodesListOutput struct{ Body Collection[AdminNode] }
type adminNodePosition struct {
	Type string
	Name string
	ID   int
}

func nodePosition(n *nodepool.Node) adminNodePosition {
	return adminNodePosition{Type: n.Type, Name: n.Name, ID: n.ID}
}
func compareNodePosition(a, b adminNodePosition) int {
	return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
}
func adminNodeOf(n *nodepool.Node) AdminNode {
	out := AdminNode{ID: IDFromInt(int64(n.ID)), Name: n.Name, Type: n.Type, URL: n.URL, Enabled: n.Enabled, PublicURL: n.PublicURL, Healthy: n.Healthy, ActiveJobs: n.ActiveJobs, Group: n.Group, MaxJobs: n.MaxJobs, MaxBandwidthKbps: n.MaxBandwidthKbps, EgressKbps: n.EgressKbps, CreatedAt: NewInstant(n.CreatedAt), Capabilities: n.Capabilities, CapabilitiesHash: n.CapabilitiesHash, LastStats: n.LastStats, HWAccelOverride: n.HWAccelOverride, HWDeviceOverride: n.HWDeviceOverride, DownloadArtifactDirOverride: n.DownloadArtifactDirOverride, DownloadArtifactMaxBytesOverride: n.DownloadArtifactMaxBytesOverride, CapabilityDrift: n.CapabilityDrift, CapabilityDriftBaseline: n.CapabilityDriftBaseline, AdvertisedCapabilitiesHash: n.AdvertisedCapabilitiesHash, PhysicalGPUKeys: n.PhysicalGPUKeys, DrainRequested: n.DrainRequested, DrainState: adminNodeDrainState(n)}
	if n.LastHealthCheck != nil {
		out.LastHealthCheck = NullableInstant{Valid: true, Time: NewInstant(*n.LastHealthCheck)}
	}
	if n.CapabilitiesRefreshedAt != nil {
		out.CapabilitiesRefreshedAt = new(NewInstant(*n.CapabilitiesRefreshedAt))
	}
	if len(n.NetworkAccess) > 0 {
		out.NetworkAccess = make(map[string]AdminNodeNetworkAccess, len(n.NetworkAccess))
		for provider, status := range n.NetworkAccess {
			entry := AdminNodeNetworkAccess{State: status.State, Origin: status.Origin, Hostname: status.Hostname}
			if !status.UpdatedAt.IsZero() {
				entry.UpdatedAt = new(NewInstant(status.UpdatedAt))
			}
			out.NetworkAccess[provider] = entry
		}
	}
	return out
}

// AdminNodeSummary is the monitoring view of a node: health, load and its
// last resource sample, without the addresses, paths, capability documents and
// configuration overrides the full row carries. Scoped API keys read nodes
// only through it.
type AdminNodeSummary struct {
	ID               ID                  `json:"id"`
	Name             string              `json:"name"`
	Type             string              `json:"type" enum:"proxy,transcode"`
	Enabled          bool                `json:"enabled"`
	Healthy          bool                `json:"healthy"`
	ActiveJobs       int                 `json:"active_jobs"`
	Group            *string             `json:"group"`
	MaxJobs          *int                `json:"max_jobs"`
	MaxBandwidthKbps *int                `json:"max_bandwidth_kbps"`
	EgressKbps       int                 `json:"egress_kbps"`
	LastHealthCheck  NullableInstant     `json:"last_health_check"`
	CreatedAt        Instant             `json:"created_at"`
	DrainRequested   bool                `json:"drain_requested" doc:"An administrator asked this transcode node to drain."`
	DrainState       string              `json:"drain_state" enum:"none,draining,drained" doc:"What the node reported on its last health check."`
	Resources        *AdminNodeResources `json:"resources,omitempty" doc:"Resource sample stored at the last health check; absent when the node reported none."`
}

// AdminNodeResources is a node's resource sample in the same shape as the API
// host's own (/admin/system/resources), with mount paths removed.
type AdminNodeResources struct {
	SampledAt   *Instant                  `json:"sampled_at,omitempty"`
	System      *AdminSystemStats         `json:"system,omitempty"`
	GPU         []AdminGPUStats           `json:"gpu"`
	Attribution *AdminResourceAttribution `json:"attribution,omitempty"`
}
type AdminNodeSummariesOutput struct{ Body Collection[AdminNodeSummary] }

func adminNodeSummaryOf(n *nodepool.Node) AdminNodeSummary {
	out := AdminNodeSummary{ID: IDFromInt(int64(n.ID)), Name: n.Name, Type: n.Type, Enabled: n.Enabled, Healthy: n.Healthy, ActiveJobs: n.ActiveJobs, Group: n.Group, MaxJobs: n.MaxJobs, MaxBandwidthKbps: n.MaxBandwidthKbps, EgressKbps: n.EgressKbps, CreatedAt: NewInstant(n.CreatedAt), DrainRequested: n.DrainRequested, DrainState: adminNodeDrainState(n), Resources: adminNodeResourcesOf(n.LastStats)}
	if n.LastHealthCheck != nil {
		out.LastHealthCheck = NullableInstant{Valid: true, Time: NewInstant(*n.LastHealthCheck)}
	}
	return out
}

// adminNodeResourcesOf decodes only the sample fields of a node's stored
// health blob, so its build identity, download-storage measurement and any
// field a newer or misbehaving node adds never reach the summary, and drops
// mount paths a node should already have withheld.
func adminNodeResourcesOf(raw json.RawMessage) *AdminNodeResources {
	if len(raw) == 0 {
		return nil
	}
	var stored struct {
		System      *nodemetrics.SystemStats         `json:"system"`
		GPU         []nodemetrics.GPUStats           `json:"gpu"`
		Attribution *nodemetrics.ResourceAttribution `json:"attribution"`
		SampledAt   time.Time                        `json:"sampled_at"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil
	}
	s := nodemetrics.Snapshot{System: stored.System, GPU: stored.GPU, Attribution: stored.Attribution, SampledAt: stored.SampledAt}.RedactPaths()
	if s.System == nil && len(s.GPU) == 0 && s.Attribution == nil {
		return nil
	}
	out := &AdminNodeResources{System: adminSystemStatsOf(s.System), GPU: adminGPUStatsOf(s.GPU), Attribution: adminResourceAttribution(s.Attribution)}
	if !s.SampledAt.IsZero() {
		out.SampledAt = new(NewInstant(s.SampledAt))
	}
	return out
}

func registerAdminNodesRead(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/nodes", "listAdminNodes", "admin-nodes", "Page configured nodes and their last stored observations; no worker probe is performed."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminNodesListInput) (*AdminNodesListOutput, error) {
		items, next, err := pageAdminNodes(ctx, reg, cursors, "listAdminNodes", in, func(n *nodepool.Node) AdminNode {
			item := adminNodeOf(n)
			if n.AdminRevision > 0 {
				item.ConfigETag = adminNodeConfigurationTag(ctx, n.ID, n.AdminRevision).String()
			}
			return item
		})
		if err != nil {
			return nil, err
		}
		return &AdminNodesListOutput{Body: Paginated(items, next)}, nil
	})
	summary := Operation{Operation: humaOp("GET", Prefix+"/admin/nodes/summary", "listAdminNodeSummaries", "admin-nodes", "Page nodes with their health, load and last resource sample, without addresses, paths or configuration overrides; no worker probe is performed."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, summary, func(ctx context.Context, in *AdminNodesListInput) (*AdminNodeSummariesOutput, error) {
		items, next, err := pageAdminNodes(ctx, reg, cursors, "listAdminNodeSummaries", in, adminNodeSummaryOf)
		if err != nil {
			return nil, err
		}
		return &AdminNodeSummariesOutput{Body: Paginated(items, next)}, nil
	})
}

// pageAdminNodes pages the configured nodes in type/name/id order, projecting
// each with view.
func pageAdminNodes[T any](ctx context.Context, reg *Registry, cursors *Cursors, operationID string, in *AdminNodesListInput, view func(*nodepool.Node) T) ([]T, string, error) {
	if reg.deps.AdminNodesRead == nil {
		return nil, "", unavailable("administrator nodes")
	}
	scope := CursorScope{OperationID: operationID, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: strconv.Itoa(in.Limit), Sort: "type,name", Tiebreaker: "id"}
	var after adminNodePosition
	if in.Cursor != "" {
		if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
			return nil, "", p
		}
		if after.ID <= 0 {
			return nil, "", NewProblem(TypeInvalidCursor, "Invalid node cursor")
		}
	}
	nodes, err := reg.deps.AdminNodesRead.ReadAdminNodes(ctx)
	if errors.Is(err, handlers.ErrAdminNodesUnavailable) {
		return nil, "", unavailable("administrator nodes")
	}
	if err != nil {
		return nil, "", NewProblem(TypeInternalError, "Nodes could not be listed")
	}
	// The owning store's order is type/name; ID makes duplicate names stable.
	nodes = slices.Clone(nodes)
	nodes = slices.DeleteFunc(nodes, func(n *nodepool.Node) bool { return n == nil })
	slices.SortFunc(nodes, func(a, b *nodepool.Node) int { return compareNodePosition(nodePosition(a), nodePosition(b)) })
	items := make([]T, 0, in.Limit)
	next := ""
	var last adminNodePosition
	for _, n := range nodes {
		position := nodePosition(n)
		if in.Cursor != "" && compareNodePosition(position, after) <= 0 {
			continue
		}
		if len(items) == in.Limit {
			next, err = cursors.Encode(scope, last)
			if err != nil {
				return nil, "", serviceProblem(err)
			}
			break
		}
		items = append(items, view(n))
		last = position
	}
	return items, next, nil
}
