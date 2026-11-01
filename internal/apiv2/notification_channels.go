package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// discordManagementDescription states the household-manager rule on every
// operation that changes the account-wide Discord destination or mode.
const discordManagementDescription = "Discord delivery is account-wide, so only the account's household manager may change it. X-Profile-Id must name the account's primary profile, with X-Profile-Token when that profile is PIN-protected (without it the request is 403 profile_verification_required); naming any other profile is 403 permission_denied, including on an admin account. A request without X-Profile-Id is accepted only from an admin account's API key, or from an admin account's login session while no profile on the account is PIN-protected or access-restricted; otherwise it is 403 permission_denied."

type NotificationDiscordManagementService interface {
	MayManageDiscordNotifications(context.Context, int, string, func(*userstore.Profile) error) (bool, error)
}

func (reg *Registry) requireDiscordNotificationManagement(ctx context.Context) *Problem {
	svc := reg.deps.NotificationDiscordManagement
	if svc == nil {
		return unavailable("Discord notification management")
	}
	allowed, err := svc.MayManageDiscordNotifications(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), verifyHouseholdProfile(ctx))
	if errors.Is(err, access.ErrProfileUnverified) {
		return NewProblem(TypeProfileVerificationRequired, "Discord notification changes require verifying the primary profile PIN.")
	}
	if err != nil {
		return serviceProblem(err)
	}
	if !allowed {
		return NewProblem(TypePermissionDenied, "Discord notification changes require the account's primary profile.")
	}
	return nil
}

type NotificationChannelService interface {
	ClearEmailAddress(context.Context, int, string) error
	UnlinkDiscord(context.Context, int) error
	EmailPreferences(context.Context, int, string) (notifications.EmailPreferencesState, error)
	SetEmailMode(context.Context, int, string, string) error
	SetEmailChoices(context.Context, int, string, notifications.EmailChoices) error
	SetEmailSends(context.Context, int, string, bool, bool, bool) error
	DiscordPrefsFor(context.Context, int) (notifications.DiscordPrefs, error)
	SetDiscordMode(context.Context, int, string) error
}

type NotificationChannelModeInput struct {
	Body struct {
		Mode string `json:"mode" enum:"off,per_episode,daily_digest,per_episode_and_digest"`
	}
}
type NotificationEmailPreferencesInput struct {
	Body struct {
		Mode        string    `json:"mode,omitempty" enum:"off,per_episode,daily_digest,per_episode_and_digest,weekly_digest,per_episode_and_weekly_digest" doc:"One mode for older clients; instant, daily and weekly take precedence when any is sent."`
		Instant     *bool     `json:"instant,omitempty" doc:"An email shortly after something new arrives."`
		Daily       *bool     `json:"daily,omitempty" doc:"A daily digest."`
		Weekly      *bool     `json:"weekly,omitempty" doc:"A weekly digest."`
		Content     *string   `json:"content,omitempty" enum:"followed,all" doc:"What to email: shows the profile follows, or everything new it can open."`
		Types       *[]string `json:"types,omitempty" doc:"Item types to include (movie, episode); empty includes both."`
		LibraryIDs  *[]int    `json:"library_ids,omitempty" doc:"Libraries to include; empty includes every library the profile can open."`
		MaxAgeHours *int      `json:"max_age_hours,omitempty" doc:"How recently an item must have been added to count as new, in hours; 0 uses the server default."`
	}
}
type NotificationEmailPreferences struct {
	Mode           string   `json:"mode" enum:"off,per_episode,daily_digest,per_episode_and_digest,weekly_digest,per_episode_and_weekly_digest"`
	CustomEmail    string   `json:"custom_email"`
	PendingEmail   string   `json:"pending_email"`
	CanEditAddress bool     `json:"can_edit_address"`
	Instant        bool     `json:"instant" doc:"An email shortly after something new arrives."`
	Daily          bool     `json:"daily" doc:"A daily digest."`
	Weekly         bool     `json:"weekly" doc:"A weekly digest."`
	Content        string   `json:"content" enum:"followed,all" doc:"What to email: shows the profile follows, or everything new it can open."`
	Types          []string `json:"types" doc:"Item types to include; empty includes both."`
	LibraryIDs     []int    `json:"library_ids" doc:"Libraries to include; empty includes every library the profile can open."`
	MaxAgeHours    int      `json:"max_age_hours" doc:"How recently an item must have been added to count as new, in hours; 0 uses the server default."`
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
	content := view.Content
	if content == "" {
		content = notifications.EmailContentFollowed
	}
	types, libraries := view.Types, view.LibraryIDs
	if types == nil {
		types = []string{}
	}
	if libraries == nil {
		libraries = []int{}
	}
	return &NotificationEmailPreferencesOutput{Body: NotificationEmailPreferences{Mode: view.Mode, CustomEmail: view.CustomEmail, PendingEmail: view.PendingEmail, CanEditAddress: !view.IsChild, Instant: view.Instant, Daily: view.Daily, Weekly: view.Weekly, Content: content, Types: types, LibraryIDs: libraries, MaxAgeHours: view.MaxAgeHours}}, nil
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
	unlink.Description = discordManagementDescription
	Register(reg, unlink, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		if p := reg.requireDiscordNotificationManagement(ctx); p != nil {
			return nil, p
		}
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
	emailWrite.Summary = "Set the acting profile's email delivery mode and what its emails include."
	Register(reg, emailWrite, func(ctx context.Context, in *NotificationEmailPreferencesInput) (*NotificationEmailPreferencesOutput, error) {
		svc := reg.deps.NotificationChannels
		if svc == nil {
			return nil, unavailable("notification channels")
		}
		b := in.Body
		sends := b.Instant != nil || b.Daily != nil || b.Weekly != nil
		choices := b.Content != nil || b.Types != nil || b.LibraryIDs != nil || b.MaxAgeHours != nil
		if b.Mode == "" && !sends && !choices {
			return nil, NewProblem(TypeValidationFailed, "Nothing to change: send a mode, the sends or the choices.")
		}
		if b.Content != nil || b.Types != nil || b.LibraryIDs != nil || b.MaxAgeHours != nil {
			choices := notifications.EmailChoices{Content: b.Content, Types: b.Types, LibraryIDs: b.LibraryIDs, MaxAgeHours: b.MaxAgeHours}
			if err := svc.SetEmailChoices(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), choices); err != nil {
				return nil, notificationChannelProblem(err)
			}
		}
		if b.Instant != nil || b.Daily != nil || b.Weekly != nil {
			current, err := svc.EmailPreferences(ctx, claimsFrom(ctx).UserID, profileFrom(ctx))
			if err != nil {
				return nil, serviceProblem(err)
			}
			instant, daily, weekly := current.Instant, current.Daily, current.Weekly
			if b.Instant != nil {
				instant = *b.Instant
			}
			if b.Daily != nil {
				daily = *b.Daily
			}
			if b.Weekly != nil {
				weekly = *b.Weekly
			}
			if err := svc.SetEmailSends(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), instant, daily, weekly); err != nil {
				return nil, notificationChannelProblem(err)
			}
		} else if b.Mode != "" {
			if err := svc.SetEmailMode(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), b.Mode); err != nil {
				return nil, notificationChannelProblem(err)
			}
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
	discordWrite.Description = discordManagementDescription
	Register(reg, discordWrite, func(ctx context.Context, in *NotificationChannelModeInput) (*NotificationDiscordPreferencesOutput, error) {
		if p := reg.requireDiscordNotificationManagement(ctx); p != nil {
			return nil, p
		}
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
