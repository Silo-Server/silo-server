package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type NotificationServerChannelUpdateInput struct {
	ID   string `path:"id"`
	Body struct {
		Name                   *string `json:"name,omitempty"`
		URL                    *string `json:"url,omitempty"`
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
type NotificationServerChannelUpdateOutput struct{ Body NotificationServerChannel }

func registerNotificationServerChannelUpdate(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPut, Prefix+"/admin/notifications/server-channels/{id}", "updateAdminNotificationServerChannel", "admin", "Update channel configuration and atomically reset dispatch state on URL replacement or re-enabling. Never replay an uncertain update."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNonRetryable}
	op.MaxBodyBytes = 16384
	Register(reg, op, func(ctx context.Context, in *NotificationServerChannelUpdateInput) (*NotificationServerChannelUpdateOutput, error) {
		if reg.deps.NotificationDestinations == nil {
			return nil, unavailable("notification destinations")
		}
		row, err := reg.deps.NotificationDestinations.UpdateNotificationServerChannel(ctx, in.ID, notifications.ServerChannelInput{
			Name:                   in.Body.Name,
			URL:                    in.Body.URL,
			Enabled:                in.Body.Enabled,
			NotifyNewMovies:        in.Body.NotifyNewMovies,
			NotifyNewEpisodes:      in.Body.NotifyNewEpisodes,
			NotifyNewAudiobooks:    in.Body.NotifyNewAudiobooks,
			NotifyNewEbooks:        in.Body.NotifyNewEbooks,
			NotifyRequestSubmitted: in.Body.NotifyRequestSubmitted,
			NotifyRequestApproved:  in.Body.NotifyRequestApproved,
			NotifyRequestDeclined:  in.Body.NotifyRequestDeclined,
			NotifyRequestFulfilled: in.Body.NotifyRequestFulfilled,
		})
		if errors.Is(err, notifications.ErrServerChannelNotFound) {
			return nil, NewProblem(TypeNotFound, "Server channel not found.")
		}
		if errors.Is(err, notifications.ErrServerChannelInvalid) {
			return nil, NewProblem(TypeValidationFailed, "Invalid server channel configuration.")
		}
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &NotificationServerChannelUpdateOutput{Body: notificationServerChannelOf(*row)}, nil
	})
}
