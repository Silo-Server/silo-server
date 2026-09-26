package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type NotificationRelayService interface {
	RegisterNotificationRelay(context.Context, string) (handlers.NotificationRelayView, error)
	ClearNotificationRelay(context.Context) error
}

type NotificationRelayRegisterInput struct {
	RawBody []byte
	Body    struct {
		RelayURL string `json:"relay_url,omitempty"`
	}
}
type NotificationRelayRegistration struct {
	RelayURL         string   `json:"relay_url"`
	DeploymentID     ID       `json:"deployment_id"`
	KeyPrefix        string   `json:"key_prefix"`
	APIKeyConfigured bool     `json:"api_key_configured"`
	RelayRequestID   string   `json:"relay_request_id,omitempty"`
	APNsTopics       []string `json:"apns_topics,omitempty"`
	ExpiresAt        Instant  `json:"expires_at"`
}
type NotificationRelayRegisterOutput struct{ Body NotificationRelayRegistration }

func notificationRelayProblem(err error) *Problem {
	p := serviceProblem(err)
	if e, ok := errors.AsType[*handlers.APIError](err); ok && e.RetryAfter > 0 && p.GetHeaders().Get("Retry-After") == "" {
		return p.WithRetryAfter(e.RetryAfter)
	}
	return p
}

func registerNotificationRelay(reg *Registry) {
	register := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/notifications/push/relay/register", "registerAdminNotificationRelay", "admin", "Register or rotate the configured push relay credential."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, register, func(ctx context.Context, in *NotificationRelayRegisterInput) (*NotificationRelayRegisterOutput, error) {
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		svc := reg.deps.NotificationRelay
		if svc == nil {
			return nil, unavailable("push relay")
		}
		view, err := svc.RegisterNotificationRelay(ctx, in.Body.RelayURL)
		if err != nil {
			return nil, notificationRelayProblem(err)
		}
		return &NotificationRelayRegisterOutput{Body: NotificationRelayRegistration{RelayURL: view.RelayURL, DeploymentID: ID(view.DeploymentID), KeyPrefix: view.KeyPrefix, APIKeyConfigured: true, RelayRequestID: view.RelayRequestID, APNsTopics: view.APNsTopics, ExpiresAt: NewInstant(view.ExpiresAt)}}, nil
	})
	clear := Operation{Operation: humaOp(http.MethodDelete, Prefix+"/admin/notifications/push/relay", "clearAdminNotificationRelay", "admin", "Clear the local push relay credential."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	clear.DefaultStatus = http.StatusNoContent
	Register(reg, clear, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		svc := reg.deps.NotificationRelay
		if svc == nil {
			return nil, unavailable("push relay")
		}
		if err := svc.ClearNotificationRelay(ctx); err != nil {
			return nil, notificationRelayProblem(err)
		}
		return nil, nil
	})
}
