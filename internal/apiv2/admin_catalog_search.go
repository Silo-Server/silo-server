package apiv2

import (
	"context"

	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
)

type AdminCatalogSearchService interface {
	GetCatalogSearchStatus(context.Context) catalogsvc.CatalogSearchRuntimeStatus
}
type AdminCatalogSearchOutput struct {
	Body AdminCatalogSearchRuntimeStatus
}
type AdminCatalogSearchRuntimeStatus struct {
	ConfiguredProvider string                             `json:"configured_provider"`
	ActiveProvider     string                             `json:"active_provider"`
	Degraded           bool                               `json:"degraded"`
	DegradedReason     string                             `json:"degraded_reason,omitempty"`
	Meilisearch        AdminCatalogSearchMeiliStatus      `json:"meilisearch"`
	Index              AdminCatalogSearchIndexStateStatus `json:"index"`
	Tasks              []AdminCatalogSearchTaskLink       `json:"tasks"`
	Semantic           AdminCatalogSearchSemanticStatus   `json:"semantic"`
}
type AdminCatalogSearchSemanticStatus struct {
	Ready             bool                                 `json:"ready"`
	DisabledReason    string                               `json:"disabled_reason,omitempty"`
	CoverageRatio     float64                              `json:"vector_coverage_ratio"`
	CoverageUpdatedAt *Instant                             `json:"coverage_updated_at,omitempty"`
	PerType           []AdminCatalogSearchTypeCoverage     `json:"per_type,omitempty"`
	Capability        AdminCatalogSearchSemanticCapability `json:"capability"`
}
type AdminCatalogSearchTypeCoverage struct {
	Type          string  `json:"type"`
	Eligible      int     `json:"eligible"`
	Vectorized    int     `json:"vectorized"`
	CoverageRatio float64 `json:"vector_coverage_ratio"`
	Ready         bool    `json:"ready"`
}
type AdminCatalogSearchSemanticCapability struct {
	OK         bool   `json:"ok"`
	Reason     string `json:"reason,omitempty"`
	Embedder   string `json:"embedder,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
}
type AdminCatalogSearchMeiliStatus struct {
	Configured       bool     `json:"configured"`
	Healthy          bool     `json:"healthy"`
	CircuitState     string   `json:"circuit_state"`
	CircuitReason    string   `json:"circuit_reason,omitempty"`
	CircuitUntil     *Instant `json:"circuit_until,omitempty"`
	LastFallback     string   `json:"last_fallback,omitempty"`
	TimeoutMS        int      `json:"timeout_ms"`
	MatchingStrategy string   `json:"matching_strategy"`
	IndexTypes       []string `json:"index_types,omitempty"`
	SemanticEnabled  bool     `json:"semantic_enabled"`
	BinaryQuantized  bool     `json:"binary_quantized"`
	SemanticRatio    float64  `json:"semantic_ratio"`
	Embedder         string   `json:"embedder"`
}
type AdminCatalogSearchIndexStateStatus struct {
	ActiveIndexUID        string `json:"active_index_uid"`
	SchemaVersion         int    `json:"schema_version"`
	ExpectedSchemaVersion int    `json:"expected_schema_version"`
	RebuildRequired       bool   `json:"rebuild_required"`
	DocumentCount         int    `json:"document_count"`
	VectorDocumentCount   int    `json:"vector_document_count"`
	PendingEvents         int    `json:"pending_events"`
	// DeadLetteredEvents counts outbox events that exhausted their retries and
	// were dropped; each is an item whose index document is stale until the
	// next rebuild.
	DeadLetteredEvents   int      `json:"dead_lettered_events"`
	LastRebuildAt        *Instant `json:"last_rebuild_at,omitempty"`
	LastSyncAt           *Instant `json:"last_sync_at,omitempty"`
	LastProcessedEventID ID       `json:"last_processed_event_id"`
}
type AdminCatalogSearchTaskLink struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Href string `json:"href"`
}

func adminCatalogSearchRuntimeStatusOf(in catalogsvc.CatalogSearchRuntimeStatus) AdminCatalogSearchRuntimeStatus {
	out := AdminCatalogSearchRuntimeStatus{}
	out.ConfiguredProvider = in.ConfiguredProvider
	out.ActiveProvider = in.ActiveProvider
	out.Degraded = in.Degraded
	out.DegradedReason = in.DegradedReason
	out.Meilisearch = adminCatalogSearchMeiliStatusOf(in.Meilisearch)
	out.Index = adminCatalogSearchIndexStateStatusOf(in.Index)
	out.Tasks = make([]AdminCatalogSearchTaskLink, 0, len(in.Tasks))
	for _, item := range in.Tasks {
		out.Tasks = append(out.Tasks, adminCatalogSearchTaskLinkOf(item))
	}
	out.Semantic = adminCatalogSearchSemanticStatusOf(in.Semantic)
	return out
}
func adminCatalogSearchSemanticStatusOf(in catalogsvc.CatalogSearchSemanticStatus) AdminCatalogSearchSemanticStatus {
	out := AdminCatalogSearchSemanticStatus{}
	out.Ready = in.Ready
	out.DisabledReason = in.DisabledReason
	out.CoverageRatio = in.CoverageRatio
	out.CoverageUpdatedAt = instantPtr(in.CoverageUpdatedAt)
	out.PerType = make([]AdminCatalogSearchTypeCoverage, 0, len(in.PerType))
	for _, item := range in.PerType {
		out.PerType = append(out.PerType, adminCatalogSearchTypeCoverageOf(item))
	}
	out.Capability = adminCatalogSearchSemanticCapabilityOf(in.Capability)
	return out
}
func adminCatalogSearchTypeCoverageOf(in catalogsvc.CatalogSearchTypeCoverage) AdminCatalogSearchTypeCoverage {
	out := AdminCatalogSearchTypeCoverage{}
	out.Type = in.Type
	out.Eligible = in.Eligible
	out.Vectorized = in.Vectorized
	out.CoverageRatio = in.CoverageRatio
	out.Ready = in.Ready
	return out
}
func adminCatalogSearchSemanticCapabilityOf(in catalogsvc.CatalogSearchSemanticCapability) AdminCatalogSearchSemanticCapability {
	out := AdminCatalogSearchSemanticCapability{}
	out.OK = in.OK
	out.Reason = in.Reason
	out.Embedder = in.Embedder
	out.Dimensions = in.Dimensions
	return out
}
func adminCatalogSearchMeiliStatusOf(in catalogsvc.CatalogSearchMeiliStatus) AdminCatalogSearchMeiliStatus {
	out := AdminCatalogSearchMeiliStatus{}
	out.Configured = in.Configured
	out.Healthy = in.Healthy
	out.CircuitState = in.CircuitState
	out.CircuitReason = in.CircuitReason
	out.CircuitUntil = instantPtr(in.CircuitUntil)
	out.LastFallback = in.LastFallback
	out.TimeoutMS = in.TimeoutMS
	out.MatchingStrategy = in.MatchingStrategy
	out.IndexTypes = in.IndexTypes
	out.SemanticEnabled = in.SemanticEnabled
	out.BinaryQuantized = in.BinaryQuantized
	out.SemanticRatio = in.SemanticRatio
	out.Embedder = in.Embedder
	return out
}
func adminCatalogSearchIndexStateStatusOf(in catalogsvc.CatalogSearchIndexStateStatus) AdminCatalogSearchIndexStateStatus {
	out := AdminCatalogSearchIndexStateStatus{}
	out.ActiveIndexUID = in.ActiveIndexUID
	out.SchemaVersion = in.SchemaVersion
	out.ExpectedSchemaVersion = in.ExpectedSchemaVersion
	out.RebuildRequired = in.RebuildRequired
	out.DocumentCount = in.DocumentCount
	out.VectorDocumentCount = in.VectorDocumentCount
	out.PendingEvents = in.PendingEvents
	out.DeadLetteredEvents = in.DeadLetteredEvents
	out.LastRebuildAt = instantPtr(in.LastRebuildAt)
	out.LastSyncAt = instantPtr(in.LastSyncAt)
	out.LastProcessedEventID = IDFromInt(in.LastProcessedEventID)
	return out
}
func adminCatalogSearchTaskLinkOf(in catalogsvc.CatalogSearchTaskLink) AdminCatalogSearchTaskLink {
	out := AdminCatalogSearchTaskLink{}
	out.Key = in.Key
	out.Name = in.Name
	out.Href = in.Href
	return out
}
func registerAdminCatalogSearch(reg *Registry) {
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/catalog/search/status", "getAdminCatalogSearchStatus", "admin-catalog", "Read catalog search runtime status and its finite task links."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, _ *struct{}) (*AdminCatalogSearchOutput, error) {
		if reg.deps.AdminCatalogSearch == nil {
			return nil, unavailable("catalog search status")
		}
		return &AdminCatalogSearchOutput{Body: adminCatalogSearchRuntimeStatusOf(reg.deps.AdminCatalogSearch.GetCatalogSearchStatus(ctx))}, nil
	})
}
