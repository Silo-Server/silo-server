package apiv2

import (
	"context"
	"net/http"
)

type AdminEpisodeMarkersService interface {
	RefreshEpisodeMarkers(context.Context, string, string) (string, error)
}
type AdminEpisodeMarkersInput struct {
	ID string `path:"id" minLength:"1" maxLength:"512"`
}
type AdminEpisodeMarkersStatus struct {
	Status string `json:"status" enum:"queued,already_running" doc:"Process-local analysis, not a persisted job."`
}
type AdminEpisodeMarkersOutput struct{ Body AdminEpisodeMarkersStatus }

const (
	refreshAdminEpisodeMarkersOperation = "refreshAdminEpisodeMarkers"
	redetectAdminEpisodeIntroOperation  = "redetectAdminEpisodeIntro"
)

// AdminMarkerCapabilities describes marker analysis support in this API
// build, not the marker settings or the libraries that decide whether an
// item is analyzed.
type AdminMarkerCapabilities struct {
	Capability
	MovieCredits bool `json:"movie_credits" doc:"The item refresh-markers and redetect-intro operations accept movies; local analysis looks for their end credits on a best-effort basis and never for intros"`
}
type AdminMarkerCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminMarkerCapabilities
}

func (c AdminMarkerCapabilities) capabilityState() string { return StateAvailable }

func registerAdminCatalogIntro(reg *Registry) {
	capabilities := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/markers/capabilities", "getAdminMarkerCapabilities", "admin-catalog", "Discover marker analysis supported by this build, such as local movie credits. Support does not promise that marker settings or a library allow analysis."), Class: ClassActingAdmin}
	Register(reg, capabilities, func(context.Context, *CapabilityInput) (*AdminMarkerCapabilitiesOutput, error) {
		return &AdminMarkerCapabilitiesOutput{Body: AdminMarkerCapabilities{MovieCredits: true}}, nil
	})
	for _, action := range []struct{ suffix, id, action string }{
		{"refresh-markers", refreshAdminEpisodeMarkersOperation, "refresh-v2"}, {"redetect-intro", redetectAdminEpisodeIntroOperation, "redetect"},
	} {
		op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/items/{id}/"+action.suffix, action.id, "admin-catalog", "Refresh episode or movie markers using configured sources, or explicitly rerun local marker detection; movies get best-effort credits only."), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}
		op.DefaultStatus = http.StatusAccepted
		Register(reg, op, func(ctx context.Context, in *AdminEpisodeMarkersInput) (*AdminEpisodeMarkersOutput, error) {
			if reg.deps.AdminEpisodeMarkers == nil {
				return nil, unavailable("episode marker analysis")
			}
			status, err := reg.deps.AdminEpisodeMarkers.RefreshEpisodeMarkers(ctx, in.ID, action.action)
			if err != nil {
				return nil, collectionProblem(err)
			}
			return &AdminEpisodeMarkersOutput{Body: AdminEpisodeMarkersStatus{Status: status}}, nil
		})
	}
}
