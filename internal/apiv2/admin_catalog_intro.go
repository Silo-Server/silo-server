package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminEpisodeMarkersService interface {
	RefreshEpisodeMarkers(context.Context, string, string) (string, error)
	RedetectItemMarkers(ctx context.Context, itemID, kind string) (string, error)
}
type AdminEpisodeMarkersInput struct {
	ID string `path:"id" minLength:"1" maxLength:"512"`
}
type AdminEpisodeMarkersStatus struct {
	Status string `json:"status" enum:"queued,already_running" doc:"Process-local analysis, not a persisted job."`
}
type AdminEpisodeMarkersOutput struct{ Body AdminEpisodeMarkersStatus }

// AdminItemMarkersRedetect selects the marker kinds to re-detect.
type AdminItemMarkersRedetect struct {
	Kind string `json:"kind,omitempty" enum:"intro,credits,all" default:"all" doc:"intro, credits, or all (both). A movie has credits only: all means credits, and intro is rejected." example:"credits"`
}

// AdminItemMarkersRedetectInput is the redetectAdminItemMarkers request.
type AdminItemMarkersRedetectInput struct {
	ID   string                    `path:"id" minLength:"1" maxLength:"512"`
	Body *AdminItemMarkersRedetect `required:"false" doc:"Absent re-detects all kinds"`
}

const (
	refreshAdminEpisodeMarkersOperation = "refreshAdminEpisodeMarkers"
	redetectAdminEpisodeIntroOperation  = "redetectAdminEpisodeIntro"
	redetectAdminItemMarkersOperation   = "redetectAdminItemMarkers"
)

// AdminMarkerCapabilities describes marker analysis support in this API
// build, not the marker settings or the libraries that decide whether an
// item is analyzed.
type AdminMarkerCapabilities struct {
	Capability
	MovieCredits          bool `json:"movie_credits" doc:"The item refresh-markers operation, and redetect-markers with kind credits or all, accept movies, and local analysis looks for their end credits on a best-effort basis; redetect-intro stays episode-only, since movies never get intros"`
	RedetectMarkers       bool `json:"redetect_markers" doc:"The item redetect-markers operation reruns local detection of the kind requested: intro, credits, or all"`
	DetectionKindSettings bool `json:"detection_kind_settings" doc:"Local detection honors the markers.detect_intros and markers.detect_credits server settings, which turn intro and credits detection on or off separately"`
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
		return &AdminMarkerCapabilitiesOutput{Body: AdminMarkerCapabilities{MovieCredits: true, RedetectMarkers: true, DetectionKindSettings: true}}, nil
	})
	for _, action := range []struct{ suffix, id, action, summary string }{
		{"refresh-markers", refreshAdminEpisodeMarkersOperation, "refresh-v2", "Refresh episode or movie markers using configured sources; movies get best-effort local credits only."},
		{"redetect-intro", redetectAdminEpisodeIntroOperation, "redetect", "Explicitly rerun local intro detection for an episode; other items, movies included, are rejected."},
	} {
		op := adminItemMarkersOperation(action.suffix, action.id, action.summary)
		if action.id == refreshAdminEpisodeMarkersOperation {
			// Local-mode refreshes answer 409 like redetect-markers when
			// detection cannot run or every missing kind is turned off.
			op.Errors = append(op.Errors, http.StatusConflict)
		}
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
	redetect := adminItemMarkersOperation("redetect-markers", redetectAdminItemMarkersOperation, "Explicitly rerun local detection of an episode's intro, credits, or both, or of a movie's best-effort credits.")
	// Disabled library detection, missing files, markers.mode off or
	// online, and requested kinds all turned off by markers.detect_intros
	// and markers.detect_credits answer 409.
	redetect.Errors = append(redetect.Errors, http.StatusConflict)
	Register(reg, redetect, func(ctx context.Context, in *AdminItemMarkersRedetectInput) (*AdminEpisodeMarkersOutput, error) {
		if reg.deps.AdminEpisodeMarkers == nil {
			return nil, unavailable("item marker analysis")
		}
		kind := handlers.RedetectMarkersAll
		if in.Body != nil && in.Body.Kind != "" {
			kind = in.Body.Kind
		}
		status, err := reg.deps.AdminEpisodeMarkers.RedetectItemMarkers(ctx, in.ID, kind)
		if err != nil {
			return nil, collectionProblem(err)
		}
		return &AdminEpisodeMarkersOutput{Body: AdminEpisodeMarkersStatus{Status: status}}, nil
	})
}

// adminItemMarkersOperation declares one of the item marker analysis
// actions, which answer 202 for process-local work.
func adminItemMarkersOperation(suffix, id, summary string) Operation {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/items/{id}/"+suffix, id, "admin-catalog", summary), Class: ClassActingAdmin, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}
	op.DefaultStatus = http.StatusAccepted
	return op
}
