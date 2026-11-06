package notifications

import (
	"context"
	"errors"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// PluginChannelRef names a notification_channel.v1 capability by its plugin
// and capability IDs. Both stay stable across plugin upgrades and
// reinstalls, so a destination keeps working when its plugin is replaced.
type PluginChannelRef struct {
	PluginID     string
	CapabilityID string
}

// PluginChannel is a channel an enabled plugin provides: a kind of
// destination profiles can add.
type PluginChannel struct {
	PluginChannelRef
	DisplayName string
	Description string
	// Schema is the destination settings form the plugin declares: a flat
	// object of scalar fields (manifest.ValidateDestinationSchema).
	Schema *pluginv1.ConfigSchema
	// PublicFields and SecretFields split the fields the schema declares.
	// The host classifies them by the plugin runtime's rules for config
	// schemas: a secret or password form control, or a writeOnly or
	// password-format JSON Schema property, makes a field secret. Only
	// public fields are ever returned by the API; secret fields and stored
	// fields the schema no longer declares, such as a secret a plugin
	// upgrade renamed, are write-only.
	PublicFields []string
	SecretFields []string
}

// declaredFields returns the only settings a destination may store: the
// public and secret fields the host classified. A key the schema lets
// through only with additionalProperties is refused: the settings API could
// neither return it nor keep it secret, and the form can't show it.
func (c PluginChannel) declaredFields() map[string]struct{} {
	out := make(map[string]struct{}, len(c.PublicFields)+len(c.SecretFields))
	for _, key := range c.PublicFields {
		out[key] = struct{}{}
	}
	for _, key := range c.SecretFields {
		out[key] = struct{}{}
	}
	return out
}

// PluginChannelHost reaches the plugins that provide notification channels.
// The plugin runtime implements it; the notification system never starts or
// addresses plugin processes itself.
type PluginChannelHost interface {
	// Channels lists the channels the enabled plugins provide.
	Channels(ctx context.Context) ([]PluginChannel, error)
	ValidateDestination(ctx context.Context, ref PluginChannelRef, req *pluginv1.ValidateNotificationDestinationRequest) (*pluginv1.ValidateNotificationDestinationResponse, error)
	SendNotification(ctx context.Context, ref PluginChannelRef, req *pluginv1.SendNotificationRequest) (*pluginv1.SendNotificationResponse, error)
}

// PluginDestination is a profile's destination on a plugin-provided channel.
// Its settings are stored as an enc:v1: envelope and secret fields never
// leave the server.
type PluginDestination struct {
	Revision               int64
	ID                     string
	UserID                 int
	ProfileID              string
	PluginID               string
	CapabilityID           string
	Name                   string
	ConfigCiphertext       string
	Enabled                bool
	NotifyFavorites        bool
	NotifyWatchlist        bool
	NotifyContinueWatching bool
	NotifyNextUp           bool
	NotifyRequests         bool
	ConsecutiveFailures    int
	DisabledReason         *string
	LastSuccessAt          *time.Time
	LastFailureAt          *time.Time
	LastFailureMessage     *string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Ref names the destination's channel.
func (d PluginDestination) Ref() PluginChannelRef {
	return PluginChannelRef{PluginID: d.PluginID, CapabilityID: d.CapabilityID}
}

// MatchesReasons reports whether at least one of a new-episode delivery's
// reasons passes the destination's filter. Like a webhook's, it can only
// narrow what the profile's preferences let through.
func (d PluginDestination) MatchesReasons(flags ReasonFlags) bool {
	return (flags.Favorite && d.NotifyFavorites) ||
		(flags.Watchlist && d.NotifyWatchlist) ||
		(flags.ContinueWatching && d.NotifyContinueWatching) ||
		(flags.NextUp && d.NotifyNextUp)
}

// DeliveryTypePluginDestinationAutoDisabled is the in-app notice posted when
// a plugin destination is disabled after it kept failing. Its reason_flags
// carry {"destination_id","destination_name"}, and it never reaches plugin
// destinations or webhooks (loop guard).
const DeliveryTypePluginDestinationAutoDisabled = "plugin_destination.auto_disabled"

// PluginDestinationMaxPerProfile caps each profile's plugin destinations.
const PluginDestinationMaxPerProfile = 10

// pluginDestinationDeliveriesPerMinute bounds sends per profile, as the
// webhook limit does: over-limit notifications stay in the inbox.
const pluginDestinationDeliveriesPerMinute = 30

// maxPluginDestinationConfigBytes bounds a destination's settings JSON.
const maxPluginDestinationConfigBytes = 16 << 10

// Plugin destination errors surfaced to the API layer.
var (
	ErrPluginDestinationInvalid  = errors.New("invalid plugin notification destination")
	ErrPluginDestinationNotFound = errors.New("plugin notification destination not found")
	ErrPluginDestinationLimit    = errors.New("plugin notification destination limit reached")
	// ErrPluginChannelUnavailable means no enabled plugin provides the channel
	// right now, or its plugin couldn't be reached.
	ErrPluginChannelUnavailable = errors.New("plugin notification channel unavailable")
)

// PluginDestinationRejectedError carries the plugin's reason for refusing a
// destination's settings, which the user can act on.
type PluginDestinationRejectedError struct {
	Message string
}

func (e *PluginDestinationRejectedError) Error() string {
	return "plugin rejected the destination settings: " + e.Message
}

func (e *PluginDestinationRejectedError) Is(target error) bool {
	return target == ErrPluginDestinationInvalid
}

// PluginDestinationConflictError means the destination changed while the
// plugin checked new settings, so they weren't saved. Revision is the
// destination's current revision.
type PluginDestinationConflictError struct {
	Revision int64
}

func (e *PluginDestinationConflictError) Error() string {
	return "plugin destination changed while its settings were checked"
}
