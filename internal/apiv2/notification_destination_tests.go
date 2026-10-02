package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

const (
	testNotificationWebhookOperation       = "testNotificationWebhook"
	testNotificationServerChannelOperation = "testAdminNotificationServerChannel"
)

type NotificationDestinationTestService interface {
	TestNotificationWebhook(context.Context, string, string) (*notifications.WebhookTestResult, error)
	TestNotificationServerChannel(context.Context, string) (*notifications.WebhookTestResult, error)
}

type NotificationDestinationTestInput struct {
	ID ID `path:"id" minLength:"1"`
}
type NotificationDestinationTestResult struct {
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"http_status,omitzero"`
	DurationMS int64  `json:"duration_ms" minimum:"0"`
	Message    string `json:"message,omitempty"`
}
type NotificationDestinationTestOutput struct {
	Body NotificationDestinationTestResult
}

func notificationDestinationTestOutput(result *notifications.WebhookTestResult, err error) (*NotificationDestinationTestOutput, error) {
	if err != nil {
		switch {
		case errors.Is(err, notifications.ErrWebhookNotFound), errors.Is(err, notifications.ErrServerChannelNotFound):
			return nil, NewProblem(TypeNotFound, "Notification destination not found.")
		case errors.Is(err, notifications.ErrWebhooksDisabled), errors.Is(err, notifications.ErrServerChannelsDisabled):
			return nil, NewProblem(TypePermissionDenied, "Notification channel is disabled.")
		default:
			return nil, serviceProblem(err)
		}
	}
	return &NotificationDestinationTestOutput{Body: NotificationDestinationTestResult{OK: result.OK, HTTPStatus: result.HTTPStatus, DurationMS: result.DurationMS, Message: result.Message}}, nil
}

func registerNotificationDestinationTests(reg *Registry) {
	op := notificationOperation(http.MethodPost, "/webhooks/{id}/test", testNotificationWebhookOperation)
	op.Summary = "Send one synchronous sample to the acting profile's webhook."
	op.RetrySafety = RetrySafetyNonRetryable
	Register(reg, op, func(ctx context.Context, in *NotificationDestinationTestInput) (*NotificationDestinationTestOutput, error) {
		svc := reg.deps.NotificationDestinationTests
		if svc == nil {
			return nil, unavailable("notification test delivery")
		}
		return notificationDestinationTestOutput(svc.TestNotificationWebhook(ctx, profileFrom(ctx), string(in.ID)))
	})
	admin := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/notifications/server-channels/{id}/test", testNotificationServerChannelOperation, "admin", "Send one synchronous sample to a server notification channel."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	Register(reg, admin, func(ctx context.Context, in *NotificationDestinationTestInput) (*NotificationDestinationTestOutput, error) {
		svc := reg.deps.NotificationDestinationTests
		if svc == nil {
			return nil, unavailable("notification test delivery")
		}
		return notificationDestinationTestOutput(svc.TestNotificationServerChannel(ctx, string(in.ID)))
	})
}
