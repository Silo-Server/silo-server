package apiv2

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type AdminAutoscanWebhookLifecycleService interface {
	CreateAdminAutoscanSourceWebhook(context.Context, string) (handlers.AdminAutoscanSourceView, error)
	RotateAdminAutoscanSourceWebhook(context.Context, string) (handlers.AdminAutoscanSourceView, error)
	DeleteAdminAutoscanSourceWebhook(context.Context, string) error
}
type AdminSourceWebhookInput struct {
	ID string `path:"id" minLength:"1" maxLength:"256"`
}
type AdminSourceWebhookOutput struct{ Body AdminAutoscanSource }

func sourceWebhookProblem(err error) error {
	switch {
	case errors.Is(err, handlers.ErrAdminSourceWebhookUnavailable):
		return unavailable("source webhooks")
	case errors.Is(err, handlers.ErrAdminSourceWebhookMode):
		return NewProblem(TypeValidationFailed, "Source is not in webhook delivery mode.")
	case errors.Is(err, autoscan.ErrNotFound):
		return NewProblem(TypeNotFound, "Source or webhook endpoint not found.")
	case err != nil:
		return NewProblem(TypeInternalError, "Webhook change could not be confirmed. Refresh source state before another explicit submission.")
	default:
		return nil
	}
}
func registerAdminSourceWebhookLifecycle(reg *Registry) {
	operation := func(method, path, id, summary string) Operation {
		return Operation{Operation: humaOp(method, Prefix+path, id, "admin-autoscan", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	}
	Register(reg, operation("POST", "/admin/autoscan/sources/{id}/webhook", "createAdminAutoscanSourceWebhook", "Create an endpoint if missing, preserving an existing token. Readback is current state, not a revision receipt; no automatic replay or external provider request."), func(ctx context.Context, in *AdminSourceWebhookInput) (*AdminSourceWebhookOutput, error) {
		if reg.deps.AdminSourceWebhookLifecycle == nil {
			return nil, unavailable("source webhooks")
		}
		row, err := reg.deps.AdminSourceWebhookLifecycle.CreateAdminAutoscanSourceWebhook(ctx, in.ID)
		if err != nil {
			return nil, sourceWebhookProblem(err)
		}
		return &AdminSourceWebhookOutput{Body: adminAutoscanSourceOf(row)}, nil
	})
	Register(reg, operation("POST", "/admin/autoscan/sources/{id}/webhook/rotate", "rotateAdminAutoscanSourceWebhook", "Replace the stored endpoint token, invalidating its old URL. Current readback may omit an unrevealable URL; no automatic replay, durable receipt or provider update."), func(ctx context.Context, in *AdminSourceWebhookInput) (*AdminSourceWebhookOutput, error) {
		if reg.deps.AdminSourceWebhookLifecycle == nil {
			return nil, unavailable("source webhooks")
		}
		row, err := reg.deps.AdminSourceWebhookLifecycle.RotateAdminAutoscanSourceWebhook(ctx, in.ID)
		if err != nil {
			return nil, sourceWebhookProblem(err)
		}
		return &AdminSourceWebhookOutput{Body: adminAutoscanSourceOf(row)}, nil
	})
	Register(reg, operation("DELETE", "/admin/autoscan/sources/{id}/webhook", "deleteAdminAutoscanSourceWebhook", "Remove an endpoint; missing returns 404. No automatic replay across a replacement endpoint, cancellation of queued work, durable receipt or provider update."), func(ctx context.Context, in *AdminSourceWebhookInput) (*struct{}, error) {
		if reg.deps.AdminSourceWebhookLifecycle == nil {
			return nil, unavailable("source webhooks")
		}
		return nil, sourceWebhookProblem(reg.deps.AdminSourceWebhookLifecycle.DeleteAdminAutoscanSourceWebhook(ctx, in.ID))
	})
}
