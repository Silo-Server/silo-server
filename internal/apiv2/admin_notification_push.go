package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

const (
	testAdminApplePushOperation   = "testAdminApplePushNotification"
	testAdminAndroidPushOperation = "testAdminAndroidPushNotification"
)

// AdminNotificationPushService is the existing outbox-backed test dispatch.
// Each call creates a new test attempt; an HTTP retry is a new send request.
type AdminNotificationPushService interface {
	SendApplePushTest(context.Context, string, string) (*notifications.ApplePushTestResult, error)
	SendAndroidPushTest(context.Context, string, string) (*notifications.ApplePushTestResult, error)
}

type AdminNotificationPushTestInput struct {
	Body struct {
		ProfileID      ID `json:"profile_id" minLength:"1"`
		ServerDeviceID ID `json:"server_device_id,omitempty"`
	}
}

type AdminNotificationPushTestResult struct {
	AttemptID      ID     `json:"attempt_id"`
	PushDeviceID   ID     `json:"push_device_id"`
	ServerDeviceID ID     `json:"server_device_id"`
	Outcome        string `json:"outcome"`
	RelayRequestID string `json:"relay_request_id,omitempty"`
	UpstreamStatus *int   `json:"upstream_status,omitempty"`
	UpstreamReason string `json:"upstream_reason,omitempty"`
	FailureMessage string `json:"failure_message,omitempty"`
}
type AdminNotificationPushTestOutput struct {
	Body AdminNotificationPushTestResult
}

func registerAdminNotificationPush(reg *Registry) {
	for _, platform := range []struct {
		path, operation string
		apple           bool
	}{
		{"apple", testAdminApplePushOperation, true},
		{"fcm", testAdminAndroidPushOperation, false},
	} {
		op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/notifications/push/"+platform.path+"/test", platform.operation, "admin", "Dispatch one test push notification and report its current delivery outcome."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
		Register(reg, op, func(ctx context.Context, in *AdminNotificationPushTestInput) (*AdminNotificationPushTestOutput, error) {
			svc := reg.deps.AdminNotificationPush
			if svc == nil {
				return nil, unavailable("push delivery")
			}
			send := svc.SendAndroidPushTest
			if platform.apple {
				send = svc.SendApplePushTest
			}
			view, err := send(ctx, string(in.Body.ProfileID), string(in.Body.ServerDeviceID))
			if err != nil {
				switch {
				case errors.Is(err, notifications.ErrPushDeliveryInvalid):
					return nil, NewProblem(TypeValidationFailed, "Invalid push test target.")
				case errors.Is(err, notifications.ErrPushDeliveryNotFound):
					return nil, NewProblem(TypeNotFound, "Push device not found.")
				case errors.Is(err, notifications.ErrPushDeliveryUnavailable):
					return nil, unavailable("push delivery")
				default:
					return nil, serviceProblem(err)
				}
			}
			return &AdminNotificationPushTestOutput{Body: AdminNotificationPushTestResult{AttemptID: ID(view.AttemptID), PushDeviceID: ID(view.PushDeviceID), ServerDeviceID: ID(view.ServerDeviceID), Outcome: view.Outcome, RelayRequestID: view.RelayRequestID, UpstreamStatus: view.UpstreamStatus, UpstreamReason: view.UpstreamReason, FailureMessage: view.FailureMessage}}, nil
		})
	}
}
