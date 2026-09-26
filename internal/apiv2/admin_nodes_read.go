package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type AdminNodesReadService interface {
	ReadAdminNodes(context.Context) ([]*nodepool.Node, error)
}
type AdminNode struct {
	ConfigETag                 string          `json:"config_etag,omitempty" doc:"Original stored configuration validator for guarded edit/delete; excludes live health samples."`
	ID                         ID              `json:"id"`
	Name                       string          `json:"name"`
	Type                       string          `json:"type" enum:"proxy,transcode"`
	URL                        string          `json:"url"`
	Enabled                    bool            `json:"enabled"`
	PublicURL                  *string         `json:"public_url,omitempty"`
	Healthy                    bool            `json:"healthy"`
	ActiveJobs                 int             `json:"active_jobs"`
	Group                      *string         `json:"group"`
	MaxJobs                    *int            `json:"max_jobs"`
	MaxBandwidthKbps           *int            `json:"max_bandwidth_kbps"`
	EgressKbps                 int             `json:"egress_kbps"`
	LastHealthCheck            NullableInstant `json:"last_health_check"`
	CreatedAt                  Instant         `json:"created_at"`
	Capabilities               json.RawMessage `json:"capabilities,omitempty" doc:"Last stored worker capability document, preserving its owning protocol schema."`
	CapabilitiesHash           *string         `json:"capabilities_hash,omitempty"`
	CapabilitiesRefreshedAt    *Instant        `json:"capabilities_refreshed_at,omitempty"`
	LastStats                  json.RawMessage `json:"last_stats,omitempty" doc:"Worker resource sample stored at the last health check."`
	HWAccelOverride            *string         `json:"hw_accel_override,omitempty"`
	HWDeviceOverride           *string         `json:"hw_device_override,omitempty"`
	CapabilityDrift            *string         `json:"capability_drift,omitempty"`
	CapabilityDriftBaseline    json.RawMessage `json:"capability_drift_baseline,omitempty"`
	AdvertisedCapabilitiesHash *string         `json:"advertised_capabilities_hash,omitempty" doc:"Absent when not checked in this process; empty when checked but no hash was advertised."`
	PhysicalGPUKeys            []string        `json:"physical_gpu_keys,omitempty"`
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
	out := AdminNode{ID: IDFromInt(int64(n.ID)), Name: n.Name, Type: n.Type, URL: n.URL, Enabled: n.Enabled, PublicURL: n.PublicURL, Healthy: n.Healthy, ActiveJobs: n.ActiveJobs, Group: n.Group, MaxJobs: n.MaxJobs, MaxBandwidthKbps: n.MaxBandwidthKbps, EgressKbps: n.EgressKbps, CreatedAt: NewInstant(n.CreatedAt), Capabilities: n.Capabilities, CapabilitiesHash: n.CapabilitiesHash, LastStats: n.LastStats, HWAccelOverride: n.HWAccelOverride, HWDeviceOverride: n.HWDeviceOverride, CapabilityDrift: n.CapabilityDrift, CapabilityDriftBaseline: n.CapabilityDriftBaseline, AdvertisedCapabilitiesHash: n.AdvertisedCapabilitiesHash, PhysicalGPUKeys: n.PhysicalGPUKeys}
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
func registerAdminNodesRead(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/nodes", "listAdminNodes", "admin-nodes", "Page configured nodes and their last stored observations; no worker probe is performed."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminNodesListInput) (*AdminNodesListOutput, error) {
		if reg.deps.AdminNodesRead == nil {
			return nil, unavailable("administrator nodes")
		}
		scope := CursorScope{OperationID: "listAdminNodes", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: strconv.Itoa(in.Limit), Sort: "type,name", Tiebreaker: "id"}
		var after adminNodePosition
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
			if after.ID <= 0 {
				return nil, NewProblem(TypeInvalidCursor, "Invalid node cursor")
			}
		}
		nodes, err := reg.deps.AdminNodesRead.ReadAdminNodes(ctx)
		if errors.Is(err, handlers.ErrAdminNodesUnavailable) {
			return nil, unavailable("administrator nodes")
		}
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Nodes could not be listed")
		}
		// The owning store's order is type/name; ID makes duplicate names stable.
		nodes = slices.Clone(nodes)
		nodes = slices.DeleteFunc(nodes, func(n *nodepool.Node) bool { return n == nil })
		slices.SortFunc(nodes, func(a, b *nodepool.Node) int { return compareNodePosition(nodePosition(a), nodePosition(b)) })
		items := make([]AdminNode, 0, in.Limit)
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
					return nil, serviceProblem(err)
				}
				break
			}
			item := adminNodeOf(n)
			if n.AdminRevision > 0 {
				item.ConfigETag = adminNodeConfigurationTag(ctx, n.ID, n.AdminRevision).String()
			}
			items = append(items, item)
			last = position
		}
		return &AdminNodesListOutput{Body: Paginated(items, next)}, nil
	})
}
