package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type NotificationWebhookUpdateInput struct {
	ID          string `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		Name                   *string `json:"name,omitempty"`
		URL                    *string `json:"url,omitempty"`
		Enabled                *bool   `json:"enabled,omitempty"`
		NotifyFavorites        *bool   `json:"notify_favorites,omitempty"`
		NotifyWatchlist        *bool   `json:"notify_watchlist,omitempty"`
		NotifyContinueWatching *bool   `json:"notify_continue_watching,omitempty"`
		NotifyNextUp           *bool   `json:"notify_next_up,omitempty"`
		NotifyRequests         *bool   `json:"notify_requests,omitempty"`
	}
}

type NotificationWebhookUpdateOutput struct {
	ETag string `header:"ETag"`
	Body NotificationWebhookDestination
}

func registerNotificationWebhookUpdate(reg *Registry) {
	op := notificationOperation(http.MethodPut, "/webhooks/{id}", "updateNotificationWebhook")
	op.Guarded = true
	op.RetrySafety = RetrySafetyNaturalIdempotent
	op.MaxBodyBytes = 16384
	op.Summary = "Update webhook configuration using the original observed validator. Never rebase or replay an uncertain update."
	Register(reg, op, func(ctx context.Context, in *NotificationWebhookUpdateInput) (*NotificationWebhookUpdateOutput, error) {
		if reg.deps.NotificationDestinations == nil {
			return nil, unavailable("notification destinations")
		}
		row, err := reg.deps.NotificationDestinations.UpdateNotificationWebhook(ctx, profileFrom(ctx), in.ID, notifications.WebhookInput{
			Name: in.Body.Name, URL: in.Body.URL, Enabled: in.Body.Enabled,
			NotifyFavorites: in.Body.NotifyFavorites, NotifyWatchlist: in.Body.NotifyWatchlist,
			NotifyContinueWatching: in.Body.NotifyContinueWatching, NotifyNextUp: in.Body.NotifyNextUp, NotifyRequests: in.Body.NotifyRequests,
		}, func(revision int64) error {
			if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, notificationWebhookTag(profileFrom(ctx), in.ID, revision)); p != nil {
				return p
			}
			return nil
		})
		if err != nil {
			if p, ok := errors.AsType[*Problem](err); ok {
				return nil, p
			}
			if errors.Is(err, notifications.ErrWebhookNotFound) {
				return nil, NewProblem(TypeNotFound, "Webhook not found.")
			}
			if errors.Is(err, notifications.ErrWebhookInvalid) {
				return nil, NewProblem(TypeValidationFailed, "Invalid webhook configuration.")
			}
			return nil, serviceProblem(err)
		}
		return &NotificationWebhookUpdateOutput{ETag: notificationWebhookTag(row.ProfileID, row.ID, row.Revision).String(), Body: notificationWebhookDestinationOf(*row)}, nil
	})
}
