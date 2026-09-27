package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

const createNotificationWebhookOperation = "createNotificationWebhook"
const createNotificationServerChannelOperation = "createAdminNotificationServerChannel"

type NotificationDestinationCreateService interface {
	CreateNotificationWebhook(context.Context, int, string, notifications.WebhookInput) (*notifications.Webhook, string, error)
	CreateNotificationServerChannel(context.Context, int, notifications.ServerChannelInput) (*notifications.ServerChannel, string, error)
}

type NotificationWebhookCreateInput struct {
	Body struct {
		Name                   string  `json:"name" minLength:"1"`
		URL                    string  `json:"url" minLength:"1"`
		Type                   *string `json:"type,omitempty" enum:"discord,generic"`
		NotifyFavorites        *bool   `json:"notify_favorites,omitempty"`
		NotifyWatchlist        *bool   `json:"notify_watchlist,omitempty"`
		NotifyContinueWatching *bool   `json:"notify_continue_watching,omitempty"`
		NotifyNextUp           *bool   `json:"notify_next_up,omitempty"`
		NotifyRequests         *bool   `json:"notify_requests,omitempty"`
	}
}
type NotificationServerChannelCreateInput struct {
	Body struct {
		Name                   string  `json:"name" minLength:"1"`
		URL                    string  `json:"url" minLength:"1"`
		Type                   *string `json:"type,omitempty" enum:"discord,generic"`
		Enabled                *bool   `json:"enabled,omitempty"`
		NotifyNewMovies        *bool   `json:"notify_new_movies,omitempty"`
		NotifyNewEpisodes      *bool   `json:"notify_new_episodes,omitempty"`
		NotifyNewAudiobooks    *bool   `json:"notify_new_audiobooks,omitempty"`
		NotifyNewEbooks        *bool   `json:"notify_new_ebooks,omitempty"`
		NotifyRequestSubmitted *bool   `json:"notify_request_submitted,omitempty"`
		NotifyRequestApproved  *bool   `json:"notify_request_approved,omitempty"`
		NotifyRequestDeclined  *bool   `json:"notify_request_declined,omitempty"`
		NotifyRequestFulfilled *bool   `json:"notify_request_fulfilled,omitempty"`
	}
}

// NotificationDestinationCreated contains the identity and the one-time
// signing secret. Configuration is read through the destination list.
type NotificationDestinationCreated struct {
	ID            ID     `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type" enum:"discord,generic"`
	URLHost       string `json:"url_host"`
	SigningSecret string `json:"signing_secret,omitempty"`
}
type NotificationDestinationCreatedOutput struct {
	Body NotificationDestinationCreated
}

func notificationDestinationCreateProblem(err error) error {
	switch {
	case errors.Is(err, notifications.ErrWebhooksDisabled), errors.Is(err, notifications.ErrServerChannelsDisabled):
		return NewProblem(TypePermissionDenied, "Notification channel is disabled.")
	case errors.Is(err, notifications.ErrWebhookLimit), errors.Is(err, notifications.ErrServerChannelLimit):
		return NewProblem(TypeValidationFailed, "Notification destination limit reached.")
	case errors.Is(err, notifications.ErrWebhookInvalid), errors.Is(err, notifications.ErrServerChannelInvalid):
		return NewProblem(TypeValidationFailed, "Invalid notification destination configuration.")
	default:
		return serviceProblem(err)
	}
}

func registerNotificationDestinationCreate(reg *Registry) {
	op := notificationOperation(http.MethodPost, "/webhooks", createNotificationWebhookOperation)
	op.Summary = "Create a profile webhook and reveal its signing secret once."
	op.DefaultStatus = http.StatusCreated
	op.RetrySafety = RetrySafetyNonRetryable
	Register(reg, op, func(ctx context.Context, in *NotificationWebhookCreateInput) (*NotificationDestinationCreatedOutput, error) {
		svc := reg.deps.NotificationDestinationCreate
		if svc == nil {
			return nil, unavailable("notification destinations")
		}
		b := in.Body
		row, secret, err := svc.CreateNotificationWebhook(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), notifications.WebhookInput{Name: &b.Name, URL: &b.URL, Type: b.Type, NotifyFavorites: b.NotifyFavorites, NotifyWatchlist: b.NotifyWatchlist, NotifyContinueWatching: b.NotifyContinueWatching, NotifyNextUp: b.NotifyNextUp, NotifyRequests: b.NotifyRequests})
		if err != nil {
			return nil, notificationDestinationCreateProblem(err)
		}
		return &NotificationDestinationCreatedOutput{Body: NotificationDestinationCreated{ID: ID(row.ID), Name: row.Name, Type: row.Type, URLHost: row.URLHost, SigningSecret: secret}}, nil
	})
	admin := Operation{Operation: humaOp(http.MethodPost, Prefix+"/admin/notifications/server-channels", createNotificationServerChannelOperation, "admin", "Create a server notification channel and reveal its signing secret once."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	admin.DefaultStatus = http.StatusCreated
	Register(reg, admin, func(ctx context.Context, in *NotificationServerChannelCreateInput) (*NotificationDestinationCreatedOutput, error) {
		svc := reg.deps.NotificationDestinationCreate
		if svc == nil {
			return nil, unavailable("notification destinations")
		}
		b := in.Body
		row, secret, err := svc.CreateNotificationServerChannel(ctx, claimsFrom(ctx).UserID, notifications.ServerChannelInput{Name: &b.Name, URL: &b.URL, Type: b.Type, Enabled: b.Enabled, NotifyNewMovies: b.NotifyNewMovies, NotifyNewEpisodes: b.NotifyNewEpisodes, NotifyNewAudiobooks: b.NotifyNewAudiobooks, NotifyNewEbooks: b.NotifyNewEbooks, NotifyRequestSubmitted: b.NotifyRequestSubmitted, NotifyRequestApproved: b.NotifyRequestApproved, NotifyRequestDeclined: b.NotifyRequestDeclined, NotifyRequestFulfilled: b.NotifyRequestFulfilled})
		if err != nil {
			return nil, notificationDestinationCreateProblem(err)
		}
		return &NotificationDestinationCreatedOutput{Body: NotificationDestinationCreated{ID: ID(row.ID), Name: row.Name, Type: row.Type, URLHost: row.URLHost, SigningSecret: secret}}, nil
	})
}
