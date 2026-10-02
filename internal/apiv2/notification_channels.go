package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type NotificationChannelService interface {
	ClearEmailAddress(context.Context, int, string) error
	UnlinkDiscord(context.Context, int) error
	EmailPreferences(context.Context, int, string) (notifications.EmailPreferencesState, error)
	SetEmailMode(context.Context, int, string, string) error
	DiscordPrefsFor(context.Context, int) (notifications.DiscordPrefs, error)
	SetDiscordMode(context.Context, int, string) error
}

type NotificationChannelModeInput struct {
	Body struct {
		Mode string `json:"mode" enum:"off,per_episode,daily_digest,per_episode_and_digest"`
	}
}
type NotificationEmailPreferences struct {
	Mode           string `json:"mode" enum:"off,per_episode,daily_digest,per_episode_and_digest"`
	CustomEmail    string `json:"custom_email"`
	PendingEmail   string `json:"pending_email"`
	CanEditAddress bool   `json:"can_edit_address"`
}
type NotificationEmailPreferencesOutput struct{ Body NotificationEmailPreferences }
type NotificationDiscordPreferences struct {
	Linked          bool   `json:"linked"`
	DiscordUsername string `json:"discord_username,omitempty"`
	Mode            string `json:"mode" enum:"off,per_episode,daily_digest,per_episode_and_digest"`
	LinkFailure     string `json:"link_failure,omitempty"`
}
type NotificationDiscordPreferencesOutput struct {
	Body NotificationDiscordPreferences
}

func (reg *Registry) notificationEmailState(ctx context.Context) (*NotificationEmailPreferencesOutput, error) {
	svc := reg.deps.NotificationChannels
	if svc == nil {
		return nil, unavailable("notification channels")
	}
	view, err := svc.EmailPreferences(ctx, claimsFrom(ctx).UserID, profileFrom(ctx))
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &NotificationEmailPreferencesOutput{Body: NotificationEmailPreferences{Mode: view.Mode, CustomEmail: view.CustomEmail, PendingEmail: view.PendingEmail, CanEditAddress: !view.IsChild}}, nil
}
func (reg *Registry) notificationDiscordState(ctx context.Context) (*NotificationDiscordPreferencesOutput, error) {
	svc := reg.deps.NotificationChannels
	if svc == nil {
		return nil, unavailable("notification channels")
	}
	view, err := svc.DiscordPrefsFor(ctx, claimsFrom(ctx).UserID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &NotificationDiscordPreferencesOutput{Body: NotificationDiscordPreferences{Linked: view.Linked(), DiscordUsername: view.DiscordUsername, Mode: view.Mode, LinkFailure: view.LinkFailure}}, nil
}

func registerNotificationChannels(reg *Registry) {
	clearAddress := notificationOperation(http.MethodDelete, "/email-preferences/address", "clearNotificationEmailAddress")
	clearAddress.RetrySafety = RetrySafetyNonRetryable
	clearAddress.Summary = "Clear the profile's verified and pending notification address and turn email delivery off. Send once; replay may clear a newer address."
	Register(reg, clearAddress, func(ctx context.Context, _ *struct{}) (*NotificationEmailPreferencesOutput, error) {
		svc := reg.deps.NotificationChannels
		if svc == nil {
			return nil, unavailable("notification channels")
		}
		if err := svc.ClearEmailAddress(ctx, claimsFrom(ctx).UserID, profileFrom(ctx)); err != nil {
			if errors.Is(err, notifications.ErrEmailChildProfile) {
				return nil, NewProblem(TypePermissionDenied, "Child profiles cannot change the notification address.")
			}
			return nil, serviceProblem(err)
		}
		return reg.notificationEmailState(ctx)
	})

	unlink := notificationOperation(http.MethodDelete, "/discord-link", "unlinkNotificationDiscord")
	unlink.ProfileOptional = true
	unlink.DefaultStatus = http.StatusNoContent
	unlink.RetrySafety = RetrySafetyNonRetryable
	unlink.Summary = "Unlink the account's current Discord identity and turn delivery off. Send once; replay can unlink a later connection."
	Register(reg, unlink, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		svc := reg.deps.NotificationChannels
		if svc == nil {
			return nil, unavailable("notification channels")
		}
		if err := svc.UnlinkDiscord(ctx, claimsFrom(ctx).UserID); err != nil {
			return nil, serviceProblem(err)
		}
		return &struct{}{}, nil
	})

	emailRead := notificationOperation(http.MethodGet, "/email-preferences", "getNotificationEmailPreferences")
	emailRead.Summary = "Read the acting profile's email notification preferences."
	Register(reg, emailRead, func(ctx context.Context, _ *struct{}) (*NotificationEmailPreferencesOutput, error) {
		return reg.notificationEmailState(ctx)
	})
	emailWrite := notificationOperation(http.MethodPut, "/email-preferences", "updateNotificationEmailPreferences")
	emailWrite.RetrySafety = RetrySafetyNonRetryable
	emailWrite.Summary = "Set the acting profile's email delivery mode."
	Register(reg, emailWrite, func(ctx context.Context, in *NotificationChannelModeInput) (*NotificationEmailPreferencesOutput, error) {
		svc := reg.deps.NotificationChannels
		if svc == nil {
			return nil, unavailable("notification channels")
		}
		if err := svc.SetEmailMode(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), in.Body.Mode); err != nil {
			return nil, notificationChannelProblem(err)
		}
		return reg.notificationEmailState(ctx)
	})
	discordRead := notificationOperation(http.MethodGet, "/discord-preferences", "getNotificationDiscordPreferences")
	discordRead.ProfileOptional = true
	discordRead.Summary = "Read the login account's Discord notification preferences."
	Register(reg, discordRead, func(ctx context.Context, _ *struct{}) (*NotificationDiscordPreferencesOutput, error) {
		return reg.notificationDiscordState(ctx)
	})
	discordWrite := notificationOperation(http.MethodPut, "/discord-preferences", "updateNotificationDiscordPreferences")
	discordWrite.ProfileOptional = true
	discordWrite.RetrySafety = RetrySafetyNonRetryable
	discordWrite.Summary = "Set the login account's Discord delivery mode."
	Register(reg, discordWrite, func(ctx context.Context, in *NotificationChannelModeInput) (*NotificationDiscordPreferencesOutput, error) {
		svc := reg.deps.NotificationChannels
		if svc == nil {
			return nil, unavailable("notification channels")
		}
		if err := svc.SetDiscordMode(ctx, claimsFrom(ctx).UserID, in.Body.Mode); err != nil {
			return nil, notificationChannelProblem(err)
		}
		return reg.notificationDiscordState(ctx)
	})
}

func notificationChannelProblem(err error) *Problem {
	switch {
	case errors.Is(err, notifications.ErrEmailModeInvalid), errors.Is(err, notifications.ErrDiscordModeInvalid):
		return NewProblem(TypeValidationFailed, "Unknown notification mode.")
	case errors.Is(err, notifications.ErrEmailModeNotAllowed), errors.Is(err, notifications.ErrDiscordModeNotAllowed):
		return NewProblem(TypeValidationFailed, "Per-episode delivery is disabled by the administrator.")
	case errors.Is(err, notifications.ErrEmailNoAddress):
		return NewProblem(TypeValidationFailed, "Verify an email address for this profile first.")
	case errors.Is(err, notifications.ErrDiscordNotLinked):
		return NewProblem(TypeValidationFailed, "Link a Discord account first.")
	default:
		return serviceProblem(err)
	}
}
