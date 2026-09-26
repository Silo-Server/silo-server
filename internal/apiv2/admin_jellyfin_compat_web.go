package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

// AdminJellyfinCompatWebService is the slice of *handlers.AdminHandler the
// Jellyfin Web asset commands use.
type AdminJellyfinCompatWebService interface {
	StartAdminJellyfinCompatWebInstall(context.Context, handlers.AdminJellyfinWebInstallRequest) (jellycompat.WebComponentStatus, error)
	StartAdminJellyfinCompatWebRemove(context.Context) (jellycompat.WebComponentStatus, error)
}
type AdminJellyfinWebInstallBody struct {
	Version   string `json:"version,omitempty" maxLength:"64" doc:"Jellyfin Web release to pin; empty resolves the release compatible with the emulated server version"`
	SourceURL string `json:"source_url,omitempty" maxLength:"2048" doc:"Official jellyfin-web source; empty uses the stored source URL"`
}
type AdminJellyfinWebInstallInput struct {
	// Body is required but may be empty ({}): an empty version pins the stored
	// or compatible release.
	Body AdminJellyfinWebInstallBody
}

// adminJellyfinWebStart renders one accepted local operation. A repeat of the
// same command while its operation still runs is answered with that running
// operation (coalescing); a running operation of the other kind is a conflict.
func adminJellyfinWebStart(kind jellycompat.WebComponentOperationKind, status jellycompat.WebComponentStatus, err error) (*AdminJellyfinCompatStatusOutput, error) {
	switch {
	case err == nil:
	case errors.Is(err, jellycompat.ErrWebComponentOperationActive):
		if op := status.Operation; op != nil && op.Kind == kind && op.State == jellycompat.WebComponentOperationRunning {
			break
		}
		return nil, NewProblem(TypeConflict, "A different Jellyfin Web operation is already running on this server.")
	case errors.Is(err, jellycompat.ErrWebInstallerUnavailable):
		return nil, NewProblem(TypeDependencyUnavailable, "The Jellyfin Web installer prerequisites are missing on this server.").WithRetryAfter(30)
	default:
		var apiErr *handlers.APIError
		if errors.As(err, &apiErr) {
			return nil, serviceProblem(err)
		}
		return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
			WithErrors(ProblemError{Location: locationBody, Code: codeInvalid, Detail: err.Error()})
	}
	return &AdminJellyfinCompatStatusOutput{Body: adminJellyfinCompatStatusOf(status)}, nil
}

func registerAdminJellyfinCompatWeb(reg *Registry) {
	command := func(suffix, id, summary string) Operation {
		o := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/jellyfin-compat/web/"+suffix, id, "admin-settings", summary), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyCoalescing}
		o.DefaultStatus = http.StatusAccepted
		o.Errors = append(o.Errors, http.StatusConflict)
		return o
	}
	install := command("install", "installAdminJellyfinCompatWeb",
		"Start installing or updating the Jellyfin Web assets on this server and answer the accepted local operation. One operation per install root: a repeated install while one runs returns the running operation; a running removal is a conflict. Progress is local to this replica, not a durable cluster-wide job.")
	install.MaxBodyBytes = 64 << 10
	Register(reg, install, func(ctx context.Context, in *AdminJellyfinWebInstallInput) (*AdminJellyfinCompatStatusOutput, error) {
		if reg.deps.AdminJellyfinCompatWeb == nil {
			return nil, unavailable("Jellyfin compatibility web")
		}
		status, err := reg.deps.AdminJellyfinCompatWeb.StartAdminJellyfinCompatWebInstall(ctx, handlers.AdminJellyfinWebInstallRequest{Version: in.Body.Version, SourceURL: in.Body.SourceURL})
		return adminJellyfinWebStart(jellycompat.WebComponentOperationInstall, status, err)
	})
	remove := command("remove", "removeAdminJellyfinCompatWeb",
		"Start removing the managed Jellyfin Web assets on this server, record web_enabled=false and answer the accepted local operation. A repeated removal while one runs returns the running operation; a running install is a conflict. Not a durable cluster-wide job.")
	Register(reg, remove, func(ctx context.Context, _ *struct{}) (*AdminJellyfinCompatStatusOutput, error) {
		if reg.deps.AdminJellyfinCompatWeb == nil {
			return nil, unavailable("Jellyfin compatibility web")
		}
		status, err := reg.deps.AdminJellyfinCompatWeb.StartAdminJellyfinCompatWebRemove(ctx)
		return adminJellyfinWebStart(jellycompat.WebComponentOperationRemove, status, err)
	})
}
