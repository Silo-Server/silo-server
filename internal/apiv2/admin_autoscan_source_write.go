package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type AdminAutoscanSourceWriteService interface {
	CreateAdminAutoscanSource(context.Context, handlers.AdminAutoscanSourceWrite) (handlers.AdminAutoscanSourceView, error)
	UpdateAdminAutoscanSource(context.Context, string, handlers.AdminAutoscanSourceWrite) (handlers.AdminAutoscanSourceView, error)
}
type AdminAutoscanSourceWriteBody struct {
	ConnectionID        *string                    `json:"connection_id,omitempty" nullable:"true" maxLength:"256"`
	Enabled             bool                       `json:"enabled"`
	DeliveryMode        string                     `json:"delivery_mode,omitempty" enum:"poll,webhook"`
	PollIntervalSeconds *int                       `json:"poll_interval_seconds,omitempty" nullable:"true" minimum:"1" maximum:"2147483647"`
	PathRewrites        []AdminAutoscanPathRewrite `json:"path_rewrites" maxItems:"1000"`
	SourceConfig        map[string]string          `json:"source_config,omitempty"`
	Label               string                     `json:"label,omitempty" maxLength:"1024"`
}
type AdminAutoscanSourceCreateBody struct {
	AdminAutoscanSourceWriteBody
	PluginID     string `json:"plugin_id" minLength:"1" maxLength:"256"`
	CapabilityID string `json:"capability_id" minLength:"1" maxLength:"256"`
}
type AdminAutoscanSourceCreateInput struct{ Body AdminAutoscanSourceCreateBody }
type AdminAutoscanSourceUpdateInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"256"`
	Body AdminAutoscanSourceWriteBody
}
type AdminAutoscanSourceWriteOutput struct{ Body AdminAutoscanSource }

func sourceWriteInput(b AdminAutoscanSourceWriteBody) handlers.AdminAutoscanSourceWrite {
	in := handlers.AdminAutoscanSourceWrite{ConnectionID: b.ConnectionID, Enabled: b.Enabled, DeliveryMode: b.DeliveryMode, PollIntervalSeconds: b.PollIntervalSeconds, SourceConfig: b.SourceConfig, Label: b.Label}
	for _, r := range b.PathRewrites {
		in.PathRewrites = append(in.PathRewrites, autoscan.PathRewrite{From: r.From, To: r.To})
	}
	return in
}
func sourceWriteOutput(row handlers.AdminAutoscanSourceView, err error) (*AdminAutoscanSourceWriteOutput, error) {
	switch {
	case errors.Is(err, handlers.ErrAdminAutoscanSourceWriteUnavailable):
		return nil, unavailable("autoscan sources")
	case errors.Is(err, handlers.ErrAdminAutoscanSourceWriteInvalid):
		return nil, NewProblem(TypeValidationFailed, "Invalid source identity, delivery mode, interval, rewrites or provider configuration.")
	case errors.Is(err, autoscan.ErrNotFound):
		return nil, NewProblem(TypeNotFound, "Autoscan source not found.")
	case err != nil:
		return nil, NewProblem(TypeInternalError, "Source write could not be confirmed. Refresh sources before another explicit submission.")
	}
	if row.ID == "" {
		return nil, NewProblem(TypeInternalError, "Source write returned no identity; completion is uncertain.")
	}
	return &AdminAutoscanSourceWriteOutput{Body: adminAutoscanSourceOf(row)}, nil
}
func registerAdminAutoscanSourceWrites(reg *Registry) {
	create := Operation{Operation: humaOp("POST", Prefix+"/admin/autoscan/sources", "createAdminAutoscanSource", "admin-autoscan", "Create a stored source for an installed capability. No provider call, execution acknowledgement, replay identity or durable receipt."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	create.DefaultStatus = http.StatusCreated
	create.MaxBodyBytes = 64 << 10
	Register(reg, create, func(ctx context.Context, in *AdminAutoscanSourceCreateInput) (*AdminAutoscanSourceWriteOutput, error) {
		if reg.deps.AdminAutoscanSourceWrites == nil {
			return nil, unavailable("autoscan sources")
		}
		b := sourceWriteInput(in.Body.AdminAutoscanSourceWriteBody)
		b.PluginID, b.CapabilityID = in.Body.PluginID, in.Body.CapabilityID
		return sourceWriteOutput(reg.deps.AdminAutoscanSourceWrites.CreateAdminAutoscanSource(ctx, b))
	})
	update := Operation{Operation: humaOp("PUT", Prefix+"/admin/autoscan/sources/{id}", "updateAdminAutoscanSource", "admin-autoscan", "Replace source configuration while retaining stored plugin identity. Last write wins; webhook readback is current state, not a revision receipt. No automatic replay or execution acknowledgement."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	update.MaxBodyBytes = 64 << 10
	Register(reg, update, func(ctx context.Context, in *AdminAutoscanSourceUpdateInput) (*AdminAutoscanSourceWriteOutput, error) {
		if reg.deps.AdminAutoscanSourceWrites == nil {
			return nil, unavailable("autoscan sources")
		}
		return sourceWriteOutput(reg.deps.AdminAutoscanSourceWrites.UpdateAdminAutoscanSource(ctx, in.ID, sourceWriteInput(in.Body)))
	})
}
