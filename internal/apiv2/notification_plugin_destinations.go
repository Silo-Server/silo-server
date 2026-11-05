package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

const listNotificationPluginDestinationsOperation = "listNotificationPluginDestinations"

// NotificationPluginDestinationService is the plugin destination seam,
// implemented by *notifications.PluginDestinationService. Every method is
// scoped to the acting account profile.
type NotificationPluginDestinationService interface {
	Available() bool
	Channels(context.Context) ([]notifications.PluginChannel, error)
	ListPage(context.Context, int, string, int, *notifications.Cursor) ([]notifications.PluginDestinationView, error)
	View(context.Context, notifications.PluginDestination) notifications.PluginDestinationView
	Create(context.Context, int, string, notifications.PluginDestinationInput) (*notifications.PluginDestination, error)
	UpdateGuarded(context.Context, int, string, string, notifications.PluginDestinationInput, func(int64) error) (*notifications.PluginDestination, error)
	DeleteGuarded(context.Context, int, string, string, func(int64) error) error
	Test(context.Context, int, string, string) (*notifications.PluginDestinationTestResult, error)
}

// NotificationPluginDestinationCapability reports plugin destinations.
type NotificationPluginDestinationCapability struct {
	Available     bool `json:"available" doc:"At least one enabled plugin provides a destination kind; GET /notifications/plugin-channels lists them"`
	MaxPerProfile int  `json:"max_per_profile"`
}

// NotificationPluginChannel is a kind of destination an enabled plugin
// provides, with the form for one destination's settings.
type NotificationPluginChannel struct {
	PluginID     string                  `json:"plugin_id" doc:"Names the kind together with capability_id; both stay stable across plugin upgrades"`
	CapabilityID string                  `json:"capability_id"`
	DisplayName  string                  `json:"display_name"`
	Description  string                  `json:"description"`
	SettingsForm AdminPluginConfigSchema `json:"settings_form" doc:"The destination settings form the plugin declares. Fields marked secret are write-only"`
}

type NotificationPluginChannelListOutput struct {
	Body Collection[NotificationPluginChannel]
}

// NotificationPluginDestination is one of the acting profile's plugin
// destinations.
type NotificationPluginDestination struct {
	ETag                   string          `json:"etag" doc:"Original validator for conditional update and deletion; preserve unchanged with the observed row."`
	ID                     ID              `json:"id"`
	Name                   string          `json:"name"`
	PluginID               string          `json:"plugin_id"`
	CapabilityID           string          `json:"capability_id"`
	ChannelName            string          `json:"channel_name" doc:"The kind's display name; empty while it is unavailable"`
	Available              bool            `json:"available" doc:"An enabled plugin provides the destination's kind. False while that plugin is disabled or uninstalled; nothing is sent then"`
	Enabled                bool            `json:"enabled"`
	NotifyFavorites        bool            `json:"notify_favorites"`
	NotifyWatchlist        bool            `json:"notify_watchlist"`
	NotifyContinueWatching bool            `json:"notify_continue_watching"`
	NotifyNextUp           bool            `json:"notify_next_up"`
	NotifyRequests         bool            `json:"notify_requests"`
	Settings               PluginJSONValue `json:"settings" doc:"Stored settings the kind's current schema declares and does not mark secret; empty while the kind is unavailable"`
	SecretSettingsSet      []string        `json:"secret_settings_set" doc:"Secret settings that have a stored value"`
	ConsecutiveFailures    int             `json:"consecutive_failures"`
	DisabledReason         *string         `json:"disabled_reason"`
	LastSuccessAt          NullableInstant `json:"last_success_at"`
	LastFailureAt          NullableInstant `json:"last_failure_at"`
	LastFailureMessage     *string         `json:"last_failure_message"`
	CreatedAt              Instant         `json:"created_at"`
}

func notificationPluginDestinationTag(profile, id string, revision int64) EntityTag {
	return RenderETag("notification-plugin-destination:"+profile, id, revision)
}

func notificationPluginDestinationOf(view notifications.PluginDestinationView) NotificationPluginDestination {
	d := view.PluginDestination
	out := NotificationPluginDestination{
		ETag:                   notificationPluginDestinationTag(d.ProfileID, d.ID, d.Revision).String(),
		ID:                     ID(d.ID),
		Name:                   d.Name,
		PluginID:               d.PluginID,
		CapabilityID:           d.CapabilityID,
		Available:              view.Channel != nil,
		Enabled:                d.Enabled,
		NotifyFavorites:        d.NotifyFavorites,
		NotifyWatchlist:        d.NotifyWatchlist,
		NotifyContinueWatching: d.NotifyContinueWatching,
		NotifyNextUp:           d.NotifyNextUp,
		NotifyRequests:         d.NotifyRequests,
		Settings:               NonNilMap(PluginJSONValue(view.Settings)),
		SecretSettingsSet:      NonNil(view.SecretsSet),
		ConsecutiveFailures:    d.ConsecutiveFailures,
		DisabledReason:         d.DisabledReason,
		LastSuccessAt:          destinationInstant(d.LastSuccessAt),
		LastFailureAt:          destinationInstant(d.LastFailureAt),
		LastFailureMessage:     d.LastFailureMessage,
		CreatedAt:              NewInstant(d.CreatedAt),
	}
	if view.Channel != nil {
		out.ChannelName = view.Channel.DisplayName
	}
	return out
}

func notificationPluginChannelOf(channel notifications.PluginChannel) (NotificationPluginChannel, error) {
	forms, err := adminPluginConfigSchemasOf(plugins.ConfigSchemaViews([]*pluginv1.ConfigSchema{channel.Schema}))
	if err != nil {
		return NotificationPluginChannel{}, err
	}
	out := NotificationPluginChannel{
		PluginID:     channel.PluginID,
		CapabilityID: channel.CapabilityID,
		DisplayName:  channel.DisplayName,
		Description:  channel.Description,
	}
	if len(forms) > 0 {
		out.SettingsForm = forms[0]
	}
	return out, nil
}

type NotificationPluginDestinationWrite struct {
	Name                   *string         `json:"name,omitempty" minLength:"1" maxLength:"64"`
	Settings               PluginJSONValue `json:"settings,omitempty" doc:"Settings fields, merged over the stored ones on update. A non-secret field sent as null is removed; a secret field sent blank or left out keeps its stored value"`
	ClearSecrets           []string        `json:"clear_secrets,omitempty" maxItems:"64" doc:"Secret settings to remove"`
	Enabled                *bool           `json:"enabled,omitempty"`
	NotifyFavorites        *bool           `json:"notify_favorites,omitempty"`
	NotifyWatchlist        *bool           `json:"notify_watchlist,omitempty"`
	NotifyContinueWatching *bool           `json:"notify_continue_watching,omitempty"`
	NotifyNextUp           *bool           `json:"notify_next_up,omitempty"`
	NotifyRequests         *bool           `json:"notify_requests,omitempty"`
}

func (w NotificationPluginDestinationWrite) input() notifications.PluginDestinationInput {
	return notifications.PluginDestinationInput{
		Name: w.Name, Settings: map[string]any(w.Settings), ClearSecrets: w.ClearSecrets, Enabled: w.Enabled,
		NotifyFavorites: w.NotifyFavorites, NotifyWatchlist: w.NotifyWatchlist,
		NotifyContinueWatching: w.NotifyContinueWatching, NotifyNextUp: w.NotifyNextUp, NotifyRequests: w.NotifyRequests,
	}
}

type NotificationPluginDestinationCreateInput struct {
	Body struct {
		PluginID     string `json:"plugin_id" minLength:"1" maxLength:"256"`
		CapabilityID string `json:"capability_id" minLength:"1" maxLength:"256"`
		NotificationPluginDestinationWrite
	}
}

type NotificationPluginDestinationUpdateInput struct {
	ID          string `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        NotificationPluginDestinationWrite
}

type NotificationPluginDestinationDeleteInput struct {
	ID          string `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

type NotificationPluginDestinationOutput struct {
	ETag string `header:"ETag"`
	Body NotificationPluginDestination
}

type NotificationPluginDestinationListOutput struct {
	Body Collection[NotificationPluginDestination]
}

// notificationPluginDestinationProblem maps the service's errors. A
// plugin's reason for refusing settings is the user's to act on, so it is
// passed on.
func notificationPluginDestinationProblem(err error) error {
	if p, ok := errors.AsType[*Problem](err); ok {
		return p
	}
	if rejected, ok := errors.AsType[*notifications.PluginDestinationRejectedError](err); ok {
		return validationProblem(locationBody+".settings", codeInvalid, rejected.Message)
	}
	switch {
	case errors.Is(err, notifications.ErrPluginDestinationNotFound):
		return NewProblem(TypeNotFound, "Notification destination not found.")
	case errors.Is(err, notifications.ErrPluginDestinationLimit):
		return NewProblem(TypeValidationFailed, "Notification destination limit reached.")
	case errors.Is(err, notifications.ErrPluginDestinationInvalid):
		detail := strings.TrimPrefix(err.Error(), notifications.ErrPluginDestinationInvalid.Error()+": ")
		return validationProblem(locationBody, codeInvalid, detail)
	case errors.Is(err, notifications.ErrPluginChannelUnavailable):
		return NewProblem(TypeDependencyUnavailable, "The plugin that provides this kind of destination isn't available.").WithRetryAfter(30)
	}
	return serviceProblem(err)
}

func (reg *Registry) pluginDestinations() (NotificationPluginDestinationService, *Problem) {
	if reg.deps.NotificationPluginDestinations == nil {
		return nil, unavailable("plugin notification destinations")
	}
	return reg.deps.NotificationPluginDestinations, nil
}

func registerNotificationPluginDestinations(reg *Registry) {
	registerNotificationPluginDestinationReads(reg)
	registerNotificationPluginDestinationWrites(reg)
}

func registerNotificationPluginDestinationReads(reg *Registry) {
	channels := notificationOperation(http.MethodGet, "/plugin-channels", "listNotificationPluginChannels")
	channels.Summary = "List the kinds of notification destination the enabled plugins provide, with each kind's settings form."
	Register(reg, channels, func(ctx context.Context, _ *struct{}) (*NotificationPluginChannelListOutput, error) {
		svc, p := reg.pluginDestinations()
		if p != nil {
			return nil, p
		}
		rows, err := svc.Channels(ctx)
		if err != nil {
			return nil, notificationPluginDestinationProblem(err)
		}
		items := make([]NotificationPluginChannel, 0, len(rows))
		for _, row := range rows {
			item, err := notificationPluginChannelOf(row)
			if err != nil {
				return nil, serviceProblem(err)
			}
			items = append(items, item)
		}
		return &NotificationPluginChannelListOutput{Body: NewCollection(items)}, nil
	})

	cursors := NewCursors(reg.deps.CursorSecret)
	list := notificationOperation(http.MethodGet, "/plugin-destinations", listNotificationPluginDestinationsOperation)
	list.Summary = "List the acting profile's plugin destinations. A destination whose plugin is disabled or removed reads as unavailable, without its settings."
	Register(reg, list, func(ctx context.Context, in *NotificationDestinationListInput) (*NotificationPluginDestinationListOutput, error) {
		svc, p := reg.pluginDestinations()
		if p != nil {
			return nil, p
		}
		scope := notificationCursorScope(ctx, listNotificationPluginDestinationsOperation, strconv.Itoa(in.Limit))
		var after *notifications.Cursor
		if in.Cursor != "" {
			after = new(notifications.Cursor)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		rows, err := svc.ListPage(ctx, user, profile, in.Limit+1, after)
		if err != nil {
			return nil, serviceProblem(err)
		}
		more := len(rows) > in.Limit
		if more {
			rows = rows[:in.Limit]
		}
		items := make([]NotificationPluginDestination, 0, len(rows))
		for _, row := range rows {
			items = append(items, notificationPluginDestinationOf(row))
		}
		next := ""
		if more {
			last := rows[len(rows)-1]
			next, err = cursors.Encode(scope, notifications.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &NotificationPluginDestinationListOutput{Body: Paginated(items, next)}, nil
	})
}

func registerNotificationPluginDestinationWrites(reg *Registry) {
	create := notificationOperation(http.MethodPost, "/plugin-destinations", "createNotificationPluginDestination")
	create.DefaultStatus = http.StatusCreated
	create.RetrySafety = RetrySafetyNonRetryable
	create.MaxBodyBytes = 32768
	create.Summary = "Add a destination of a plugin-provided kind to the acting profile, after the plugin accepts its settings. Send once; a replay can create a second destination."
	Register(reg, create, func(ctx context.Context, in *NotificationPluginDestinationCreateInput) (*NotificationPluginDestinationOutput, error) {
		svc, p := reg.pluginDestinations()
		if p != nil {
			return nil, p
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		input := in.Body.input()
		input.Channel = notifications.PluginChannelRef{PluginID: in.Body.PluginID, CapabilityID: in.Body.CapabilityID}
		row, err := svc.Create(ctx, user, profile, input)
		if err != nil {
			return nil, notificationPluginDestinationProblem(err)
		}
		body := notificationPluginDestinationOf(svc.View(ctx, *row))
		return &NotificationPluginDestinationOutput{ETag: body.ETag, Body: body}, nil
	})

	update := notificationOperation(http.MethodPut, "/plugin-destinations/{id}", "updateNotificationPluginDestination")
	update.Guarded = true
	update.RetrySafety = RetrySafetyNaturalIdempotent
	update.MaxBodyBytes = 32768
	update.Summary = "Update a plugin destination using the original observed validator. Changed settings are checked by the plugin again. Never rebase or replay an uncertain update."
	Register(reg, update, func(ctx context.Context, in *NotificationPluginDestinationUpdateInput) (*NotificationPluginDestinationOutput, error) {
		svc, p := reg.pluginDestinations()
		if p != nil {
			return nil, p
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		row, err := svc.UpdateGuarded(ctx, user, profile, in.ID, in.Body.input(), func(revision int64) error {
			if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, notificationPluginDestinationTag(profile, in.ID, revision)); p != nil {
				return p
			}
			return nil
		})
		if conflict, ok := errors.AsType[*notifications.PluginDestinationConflictError](err); ok {
			return nil, StaleVersionProblem(notificationPluginDestinationTag(profile, in.ID, conflict.Revision))
		}
		if err != nil {
			return nil, notificationPluginDestinationProblem(err)
		}
		body := notificationPluginDestinationOf(svc.View(ctx, *row))
		return &NotificationPluginDestinationOutput{ETag: body.ETag, Body: body}, nil
	})

	remove := notificationOperation(http.MethodDelete, "/plugin-destinations/{id}", "deleteNotificationPluginDestination")
	remove.DefaultStatus = http.StatusNoContent
	remove.Guarded = true
	remove.RetrySafety = RetrySafetyNaturalIdempotent
	remove.Summary = "Delete the acting profile's plugin destination and its queued messages. Does not recall a message already sent."
	Register(reg, remove, func(ctx context.Context, in *NotificationPluginDestinationDeleteInput) (*struct{}, error) {
		svc, p := reg.pluginDestinations()
		if p != nil {
			return nil, p
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		err := svc.DeleteGuarded(ctx, user, profile, in.ID, func(revision int64) error {
			if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, notificationPluginDestinationTag(profile, in.ID, revision)); p != nil {
				return p
			}
			return nil
		})
		if err != nil {
			return nil, notificationPluginDestinationProblem(err)
		}
		return &struct{}{}, nil
	})

	test := notificationOperation(http.MethodPost, "/plugin-destinations/{id}/test", "testNotificationPluginDestination")
	test.RetrySafety = RetrySafetyNonRetryable
	test.Summary = "Send one test message to the acting profile's plugin destination now. A failed send is a 200 result with ok false; it isn't retried or counted."
	Register(reg, test, func(ctx context.Context, in *NotificationDestinationTestInput) (*NotificationDestinationTestOutput, error) {
		svc, p := reg.pluginDestinations()
		if p != nil {
			return nil, p
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		result, err := svc.Test(ctx, user, profile, string(in.ID))
		if err != nil {
			return nil, notificationPluginDestinationProblem(err)
		}
		return &NotificationDestinationTestOutput{Body: NotificationDestinationTestResult{OK: result.OK, DurationMS: result.DurationMS, Message: result.Message}}, nil
	})
}
