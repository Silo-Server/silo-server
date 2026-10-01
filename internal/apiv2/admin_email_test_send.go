package apiv2

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminEmailTestService interface {
	SendAdminTestEmail(context.Context, string) (handlers.AdminEmailTestResult, error)
}
type AdminEmailTestInput struct {
	Body struct {
		To string `json:"to" minLength:"1" maxLength:"1024"`
	}
}
type AdminEmailTestResponse struct {
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
}
type AdminEmailTestOutput struct{ Body AdminEmailTestResponse }

func registerAdminEmailTest(reg *Registry) {
	op := Operation{Operation: humaOp("POST", Prefix+"/admin/email/test", "sendAdminTestEmail", "admin-settings", "Send one SMTP configuration test synchronously. No automatic retry, durable job, or delivery replay identity is provided."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, op, func(ctx context.Context, in *AdminEmailTestInput) (*AdminEmailTestOutput, error) {
		if reg.deps.AdminEmailTests == nil {
			return nil, unavailable("email")
		}
		result, err := reg.deps.AdminEmailTests.SendAdminTestEmail(ctx, in.Body.To)
		switch {
		case errors.Is(err, handlers.ErrAdminEmailUnavailable):
			return nil, unavailable("email")
		case errors.Is(err, handlers.ErrAdminEmailRecipient):
			return nil, NewProblem(TypeValidationFailed, "A valid recipient address is required")
		case err != nil:
			return nil, NewProblem(TypeInternalError, "Email test could not be performed")
		}
		return &AdminEmailTestOutput{Body: AdminEmailTestResponse{OK: result.OK, DurationMS: result.DurationMS, Message: result.Message}}, nil
	})
}
