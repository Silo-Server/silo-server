package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// AdminServerRestartService is the slice of *handlers.ServerControlHandler the
// restart command uses.
type AdminServerRestartService interface {
	RequestAdminServerRestart(context.Context, handlers.AdminServerRestartRequest) (handlers.AdminServerRestartResult, error)
}
type AdminServerRestartBody struct {
	Reason  string `json:"reason,omitempty" maxLength:"128" doc:"Reason recorded on the playback notice; empty records server_restart_requested"`
	Title   string `json:"title,omitempty" maxLength:"256" doc:"Notice title shown to active playback sessions"`
	Message string `json:"message,omitempty" maxLength:"1024" doc:"Notice body shown to active playback sessions"`
}
type AdminServerRestartInput struct {
	// Body is required but may be empty ({}); the defaults notify sessions with
	// the standard restarting notice.
	Body AdminServerRestartBody
}
type AdminServerRestart struct {
	Status           string `json:"status" enum:"restart_requested,already_requested" doc:"restart_requested on the first accepted request; already_requested while this process is already shutting down"`
	Message          string `json:"message"`
	NotifiedSessions int    `json:"notified_sessions" doc:"Playback sessions that acknowledged the restart notice before shutdown was requested"`
}
type AdminServerRestartOutput struct{ Body AdminServerRestart }

func registerAdminServerRestart(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/server/restart", "requestAdminServerRestart", "admin-settings",
		"Notify active playback sessions and ask this API process to shut down gracefully so its supervisor restarts it. Process-local: cluster peers are not restarted. A repeat while shutdown is pending answers already_requested and starts nothing new. No completion receipt; observe the new process through server status."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyCoalescing}
	op.DefaultStatus = http.StatusAccepted
	op.MaxBodyBytes = 64 << 10
	Register(reg, op, func(ctx context.Context, in *AdminServerRestartInput) (*AdminServerRestartOutput, error) {
		if reg.deps.AdminServerRestart == nil {
			return nil, unavailable("server restart")
		}
		result, err := reg.deps.AdminServerRestart.RequestAdminServerRestart(ctx, handlers.AdminServerRestartRequest{Reason: in.Body.Reason, Title: in.Body.Title, Message: in.Body.Message})
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &AdminServerRestartOutput{Body: AdminServerRestart{Status: result.Status, Message: result.Message, NotifiedSessions: result.NotifiedSessions}}, nil
	})
}
