package apiv2

import (
	"context"
	"slices"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminServerStatusService interface {
	ReadAdminServerStatus(context.Context) handlers.AdminServerStatusSnapshot
}
type AdminServerHealthComponent struct {
	Configured bool     `json:"configured"`
	OK         *bool    `json:"ok,omitempty"`
	LatencyMS  *float64 `json:"latency_ms,omitempty"`
}
type AdminServerHealth struct {
	Postgres    AdminServerHealthComponent `json:"postgres"`
	Redis       AdminServerHealthComponent `json:"redis"`
	Errors24h   int64                      `json:"errors_24h"`
	Warnings24h int64                      `json:"warnings_24h"`
}
type AdminArtworkStorageStatus struct {
	Backend       string `json:"backend,omitempty"`
	Locked        bool   `json:"locked"`
	StatusKnown   bool   `json:"status_known" doc:"Whether the storage lock state was read successfully. When false, clients must not treat locked=false as permission to edit storage locations."`
	PrivateLocked bool   `json:"private_locked" doc:"Whether the private bucket's endpoint, bucket, and key prefix are locked: when a bucket is configured at startup, or once artwork is stored. Changing them then takes a managed storage transition."`
}
type AdminServerStatus struct {
	StartedAt              Instant           `json:"started_at"`
	RestartRequired        bool              `json:"restart_required"`
	RestartRequiredAt      *Instant          `json:"restart_required_at,omitempty"`
	RestartRequiredReason  string            `json:"restart_required_reason,omitempty"`
	RestartRequiredReasons []string          `json:"restart_required_reasons,omitempty"`
	RestartMarkCount       int               `json:"restart_mark_count"`
	RestartRequested       bool              `json:"restart_requested"`
	RestartRequestedAt     *Instant          `json:"restart_requested_at,omitempty"`
	Health                 AdminServerHealth `json:"health"`
	// ArtworkStorage reports the resolved backend and whether artwork.storage_backend is locked to it.
	ArtworkStorage AdminArtworkStorageStatus `json:"artwork_storage"`
}
type AdminServerStatusOutput struct{ Body AdminServerStatus }

func registerAdminServerStatus(reg *Registry) {
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/server/status", "getAdminServerStatus", "admin-settings", "Read process-local restart state and bounded dependency health; unhealthy configured services are reported in the body."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, _ *struct{}) (*AdminServerStatusOutput, error) {
		if reg.deps.AdminServerStatus == nil {
			return nil, unavailable("server status")
		}
		s := reg.deps.AdminServerStatus.ReadAdminServerStatus(ctx)
		return &AdminServerStatusOutput{Body: AdminServerStatus{StartedAt: NewInstant(s.StartedAt), RestartRequired: s.RestartRequired, RestartRequiredAt: instantPtr(s.RestartRequiredAt), RestartRequiredReason: s.RestartRequiredReason, RestartRequiredReasons: slices.Clone(s.RestartRequiredReasons), RestartMarkCount: s.RestartMarkCount, RestartRequested: s.RestartRequested, RestartRequestedAt: instantPtr(s.RestartRequestedAt), Health: AdminServerHealth{Postgres: AdminServerHealthComponent{Configured: s.Health.Postgres.Configured, OK: s.Health.Postgres.OK, LatencyMS: s.Health.Postgres.LatencyMS}, Redis: AdminServerHealthComponent{Configured: s.Health.Redis.Configured, OK: s.Health.Redis.OK, LatencyMS: s.Health.Redis.LatencyMS}, Errors24h: s.Health.Errors24h, Warnings24h: s.Health.Warnings24h}, ArtworkStorage: AdminArtworkStorageStatus{Backend: s.ArtworkStorage.Backend, Locked: s.ArtworkStorage.Locked, StatusKnown: s.ArtworkStorage.StatusKnown, PrivateLocked: s.ArtworkStorage.PrivateLocked}}}, nil
	})
}
