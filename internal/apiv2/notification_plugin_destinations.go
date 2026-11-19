package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/notifications"
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
	Available     bool `json:"available" doc:"At least one enabled plugin provides a channel; GET /notifications/plugin-channels lists them"`
	MaxPerProfile int  `json:"max_per_profile"`
}

// NotificationPluginChannel is a channel an enabled plugin provides, with
// the form for one destination's settings.
type NotificationPluginChannel struct {
	PluginID     string                         `json:"plugin_id" doc:"Names the channel together with capability_id; both stay stable across plugin upgrades"`
	CapabilityID string                         `json:"capability_id"`
	DisplayName  string                         `json:"display_name"`
	Description  string                         `json:"description"`
	SettingsForm NotificationPluginSettingsForm `json:"settings_form" doc:"The destination settings form the plugin declares"`
}

// NotificationPluginSettingsForm is a destination settings form: one flat
// list of fields, rendered in order. Labels, descriptions and placeholders
// are the plugin's text.
type NotificationPluginSettingsForm struct {
	Fields []NotificationPluginSettingsField `json:"fields"`
}

// NotificationPluginSettingsField is one setting of a destination form.
// Optional members are absent when the plugin declares none.
type NotificationPluginSettingsField struct {
	Key          string                             `json:"key" doc:"The setting's key in settings"`
	Label        string                             `json:"label"`
	Description  string                             `json:"description,omitempty" doc:"Help text; absent when the plugin gives none"`
	Placeholder  string                             `json:"placeholder,omitempty" doc:"Absent when the plugin gives none"`
	Control      string                             `json:"control" enum:"text,password,number,switch,select" doc:"How to render the input. password hides what is typed; select picks one of options. A client that doesn't know a control renders the field by its type"`
	Type         string                             `json:"type" enum:"string,integer,number,boolean" doc:"The JSON type the value is sent as"`
	Required     bool                               `json:"required" doc:"The server refuses a save without a value. A stored secret left blank on an edit counts"`
	Secret       bool                               `json:"secret" doc:"Write-only: never returned in settings; listed in secret_settings_set when stored. Leave it blank on an edit to keep the stored value. A destination's secret_settings_set can also name a setting saved as secret before a plugin upgrade unmarked it; treat that one as secret too"`
	DefaultValue NotificationPluginSettingValue     `json:"default_value,omitempty" doc:"The value a new destination starts with, of the field's type; absent when there is none"`
	Options      []NotificationPluginSettingsOption `json:"options,omitempty" doc:"The choices of a select field, in order; absent for other controls"`
	Minimum      *float64                           `json:"minimum,omitempty" doc:"Smallest allowed number, inclusive; absent when unbounded"`
	Maximum      *float64                           `json:"maximum,omitempty" doc:"Largest allowed number, inclusive; absent when unbounded"`
	MinLength    *int                               `json:"min_length,omitempty" doc:"Fewest characters in a string; absent when unbounded"`
	MaxLength    *int                               `json:"max_length,omitempty" doc:"Most characters in a string; absent when unbounded"`
	Pattern      string                             `json:"pattern,omitempty" doc:"A Go RE2 regular expression a string must match somewhere in it; absent when there is none. A client may check it before saving; the server's check is the one that counts"`
}

// NotificationPluginSettingsOption is one choice of a select field.
type NotificationPluginSettingsOption struct {
	Value NotificationPluginSettingValue `json:"value" doc:"The choice, of the field's type"`
	Label string                         `json:"label"`
}

// NotificationPluginSettingValue is a setting's scalar JSON value.
type NotificationPluginSettingValue json.RawMessage

func (v NotificationPluginSettingValue) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return json.RawMessage(v).MarshalJSON()
}

func (v *NotificationPluginSettingValue) UnmarshalJSON(data []byte) error {
	*v = append((*v)[:0], data...)
	return nil
}

func (NotificationPluginSettingValue) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Description: "A string, number or boolean, as the field's type says", OneOf: []*huma.Schema{
		{Type: huma.TypeString}, {Type: huma.TypeNumber}, {Type: huma.TypeBoolean},
	}}
}

type NotificationPluginChannelListOutput struct {
	Body Collection[NotificationPluginChannel]
}

// NotificationPluginDestination is one of the acting profile's plugin
// destinations.
type NotificationPluginDestination struct {
	ETag                string          `json:"etag" doc:"Original validator for conditional update and deletion; preserve unchanged with the observed row."`
	ID                  ID              `json:"id"`
	Name                string          `json:"name"`
	PluginID            string          `json:"plugin_id"`
	CapabilityID        string          `json:"capability_id"`
	ChannelName         string          `json:"channel_name" doc:"The channel's display name; empty while it is unavailable"`
	Available           bool            `json:"available" doc:"An enabled plugin provides the destination's channel. False while that plugin is disabled or uninstalled; nothing is sent then"`
	Enabled             bool            `json:"enabled"`
	NotifyNextUp        bool            `json:"notify_next_up"`
	NotifyRequests      bool            `json:"notify_requests"`
	Settings            PluginJSONValue `json:"settings" doc:"Stored settings the channel's current schema declares and does not mark secret; empty while the channel is unavailable"`
	SecretSettingsSet   []string        `json:"secret_settings_set" doc:"Secret settings that have a stored value"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	DisabledReason      *string         `json:"disabled_reason"`
	LastSuccessAt       NullableInstant `json:"last_success_at"`
	LastFailureAt       NullableInstant `json:"last_failure_at"`
	LastFailureMessage  *string         `json:"last_failure_message"`
	CreatedAt           Instant         `json:"created_at"`
}

func notificationPluginDestinationTag(profile, id string, revision int64) EntityTag {
	return RenderETag("notification-plugin-destination:"+profile, id, revision)
}

func notificationPluginDestinationOf(view notifications.PluginDestinationView) NotificationPluginDestination {
	d := view.PluginDestination
	out := NotificationPluginDestination{
		ETag:                notificationPluginDestinationTag(d.ProfileID, d.ID, d.Revision).String(),
		ID:                  ID(d.ID),
		Name:                d.Name,
		PluginID:            d.PluginID,
		CapabilityID:        d.CapabilityID,
		Available:           view.Channel != nil,
		Enabled:             d.Enabled,
		NotifyNextUp:        d.NotifyNextUp,
		NotifyRequests:      d.NotifyRequests,
		Settings:            NonNilMap(PluginJSONValue(view.Settings)),
		SecretSettingsSet:   NonNil(view.SecretsSet),
		ConsecutiveFailures: d.ConsecutiveFailures,
		DisabledReason:      d.DisabledReason,
		LastSuccessAt:       destinationInstant(d.LastSuccessAt),
		LastFailureAt:       destinationInstant(d.LastFailureAt),
		LastFailureMessage:  d.LastFailureMessage,
		CreatedAt:           NewInstant(d.CreatedAt),
	}
	if view.Channel != nil {
		out.ChannelName = view.Channel.DisplayName
	}
	return out
}

func notificationPluginChannelOf(channel notifications.PluginChannel) (NotificationPluginChannel, error) {
	fields := channel.FormFields()
	form := NotificationPluginSettingsForm{Fields: make([]NotificationPluginSettingsField, 0, len(fields))}
	for _, field := range fields {
		item := NotificationPluginSettingsField{
			Key: field.Key, Label: field.Label, Description: field.Description, Placeholder: field.Placeholder,
			Control: field.Control, Type: field.Type, Required: field.Required, Secret: field.Secret,
			Minimum: field.Minimum, Maximum: field.Maximum, MinLength: field.MinLength, MaxLength: field.MaxLength,
			Pattern: field.Pattern,
		}
		if field.Default != nil {
			raw, err := json.Marshal(field.Default)
			if err != nil {
				return NotificationPluginChannel{}, err
			}
			item.DefaultValue = raw
		}
		for _, option := range field.Options {
			raw, err := json.Marshal(option.Value)
			if err != nil {
				return NotificationPluginChannel{}, err
			}
			item.Options = append(item.Options, NotificationPluginSettingsOption{Value: raw, Label: option.Label})
		}
		form.Fields = append(form.Fields, item)
	}
	return NotificationPluginChannel{
		PluginID:     channel.PluginID,
		CapabilityID: channel.CapabilityID,
		DisplayName:  channel.DisplayName,
		Description:  channel.Description,
		SettingsForm: form,
	}, nil
}

// NotificationPluginDestinationWrite is an update. A field left out keeps
// its value.
type NotificationPluginDestinationWrite struct {
	Name           *string         `json:"name,omitempty" minLength:"1" maxLength:"64"`
	Settings       PluginJSONValue `json:"settings,omitempty" doc:"Settings fields, merged over the stored ones. A non-secret field sent as null is removed; a secret field sent blank or left out keeps its stored value. Each value is checked against the channel's settings form"`
	ClearSecrets   []string        `json:"clear_secrets,omitempty" maxItems:"64" doc:"Secret settings to remove"`
	Enabled        *bool           `json:"enabled,omitempty"`
	NotifyNextUp   *bool           `json:"notify_next_up,omitempty"`
	NotifyRequests *bool           `json:"notify_requests,omitempty"`
}

func (w NotificationPluginDestinationWrite) input() notifications.PluginDestinationInput {
	return notifications.PluginDestinationInput{
		Name: w.Name, Settings: map[string]any(w.Settings), ClearSecrets: w.ClearSecrets, Enabled: w.Enabled,
		NotifyNextUp: w.NotifyNextUp, NotifyRequests: w.NotifyRequests,
	}
}

// NotificationPluginDestinationCreateInputBody is a new destination. Its
// name keeps the schema name the anonymous body had.
type NotificationPluginDestinationCreateInputBody struct {
	PluginID       string          `json:"plugin_id" minLength:"1" maxLength:"256"`
	CapabilityID   string          `json:"capability_id" minLength:"1" maxLength:"256"`
	Name           string          `json:"name" minLength:"1" maxLength:"64"`
	Settings       PluginJSONValue `json:"settings,omitempty" doc:"Settings fields, checked against the channel's settings form: declared fields only, each of its declared type, and every required field present"`
	Enabled        *bool           `json:"enabled,omitempty" doc:"Defaults to true"`
	NotifyNextUp   *bool           `json:"notify_next_up,omitempty" doc:"Defaults to true"`
	NotifyRequests *bool           `json:"notify_requests,omitempty" doc:"Defaults to true"`
}

type NotificationPluginDestinationCreateInput struct {
	Body NotificationPluginDestinationCreateInputBody
}

func (c NotificationPluginDestinationCreateInputBody) input() notifications.PluginDestinationInput {
	return notifications.PluginDestinationInput{
		Channel: notifications.PluginChannelRef{PluginID: c.PluginID, CapabilityID: c.CapabilityID},
		Name:    &c.Name, Settings: map[string]any(c.Settings), Enabled: c.Enabled,
		NotifyNextUp: c.NotifyNextUp, NotifyRequests: c.NotifyRequests,
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
	if invalid, ok := errors.AsType[*notifications.PluginDestinationSettingsError](err); ok {
		fieldErrors := make([]ProblemError, 0, len(invalid.Fields))
		for _, field := range invalid.Fields {
			location := locationBody + ".settings"
			if field.Field != "" {
				location += "." + field.Field
			}
			fieldErrors = append(fieldErrors, ProblemError{Location: location, Code: field.Code, Detail: field.Message})
		}
		return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").WithErrors(fieldErrors...)
	}
	switch {
	case errors.Is(err, notifications.ErrPluginDestinationNotFound):
		return NewProblem(TypeNotFound, "Notification destination not found.")
	case errors.Is(err, notifications.ErrPluginDestinationProfileGone):
		return NewProblem(TypeNotFound, "Profile not found.")
	case errors.Is(err, notifications.ErrPluginDestinationSettingsUnreadable):
		return NewProblem(TypeConflict, "This server can't read the destination's stored settings, so they weren't changed.")
	case errors.Is(err, notifications.ErrPluginDestinationLimit):
		return NewProblem(TypeValidationFailed, "Notification destination limit reached.")
	case errors.Is(err, notifications.ErrPluginDestinationNameTaken):
		return validationProblem(locationBody+".name", codeDuplicate, "A destination with this name already exists.")
	case errors.Is(err, notifications.ErrPluginDestinationInvalid):
		detail := strings.TrimPrefix(err.Error(), notifications.ErrPluginDestinationInvalid.Error()+": ")
		return validationProblem(locationBody, codeInvalid, detail)
	case errors.Is(err, notifications.ErrPluginChannelNotProvided):
		// Waiting won't change it: the destination's plugin is uninstalled
		// or turned off. A create maps it to a field error instead.
		return NewProblem(TypeConflict, "The plugin that provides this destination's channel isn't installed or is turned off.")
	case errors.Is(err, notifications.ErrPluginChannelUnavailable):
		return NewProblem(TypeDependencyUnavailable, "The plugin that provides this channel isn't available.").WithRetryAfter(30)
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
	channels.Summary = "List the notification channels the enabled plugins provide, with each channel's destination settings form."
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
	create.Summary = "Add a destination on a plugin-provided channel to the acting profile, after the plugin accepts its settings. A channel no enabled plugin provides is a 422 at body.capability_id. Send once; a replay answers 422 for the name already taken."
	Register(reg, create, func(ctx context.Context, in *NotificationPluginDestinationCreateInput) (*NotificationPluginDestinationOutput, error) {
		svc, p := reg.pluginDestinations()
		if p != nil {
			return nil, p
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		row, err := svc.Create(ctx, user, profile, in.Body.input())
		if errors.Is(err, notifications.ErrPluginChannelNotProvided) {
			return nil, validationProblem(locationBody+".capability_id", codeInvalid, "No enabled plugin provides this channel.")
		}
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
	// 409: this server can't decrypt the stored settings, or no enabled
	// plugin provides the destination's channel, so a settings change is
	// refused. 503 (declared on every operation) is a plugin that failed to
	// answer its check.
	update.Errors = []int{http.StatusConflict}
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
