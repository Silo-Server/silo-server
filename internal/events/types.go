package events

import (
	"encoding/json"
	"time"
)

type EventChannel string

const (
	ChannelCatalog       EventChannel = "catalog"
	ChannelJobs          EventChannel = "jobs"
	ChannelSessions      EventChannel = "sessions"
	ChannelTasks         EventChannel = "tasks"
	ChannelScans         EventChannel = "scans"
	ChannelHistoryImport EventChannel = "history_import"
	ChannelUserState     EventChannel = "user_state"
	ChannelUserSettings  EventChannel = "user_settings"
	ChannelSettings      EventChannel = "settings"
	ChannelPlugins       EventChannel = "plugins"
	// ChannelNotifications carries profile-scoped user notifications
	// (inbox deliveries). Subscriptions require a websocket ticket binding
	// the connection to a (user, profile).
	ChannelNotifications EventChannel = "notifications"
	// ChannelDownloadPreparations carries admin-only changes to the prepared
	// download encode queue: download_preparation.changed and
	// download_preparation.progress.
	ChannelDownloadPreparations EventChannel = "download_preparations"
	// ChannelDownloads carries admin-only download.changed events for
	// managed device downloads: identifiers and state, never paths, so an
	// admin:monitor:read key may subscribe.
	ChannelDownloads EventChannel = "downloads"
)

var AllChannels = []EventChannel{
	ChannelCatalog,
	ChannelJobs,
	ChannelSessions,
	ChannelTasks,
	ChannelScans,
	ChannelHistoryImport,
	ChannelUserState,
	ChannelUserSettings,
	ChannelSettings,
	ChannelPlugins,
	ChannelNotifications,
	ChannelDownloadPreparations,
	ChannelDownloads,
}

// ClientChannels is every channel a websocket client may subscribe to: it is
// AllChannels minus ChannelPlugins, which carries host-to-plugin runtime
// dispatch and is granted to no role, not even admin. Naming it in a
// capability response or accepting it as a valid subscription target would
// point a client at a request that can never succeed.
var ClientChannels = []EventChannel{
	ChannelCatalog,
	ChannelJobs,
	ChannelSessions,
	ChannelTasks,
	ChannelScans,
	ChannelHistoryImport,
	ChannelUserState,
	ChannelUserSettings,
	ChannelSettings,
	ChannelNotifications,
	ChannelDownloadPreparations,
	ChannelDownloads,
}

// PlaybackFileItem is the catalog item linked to a playing file. For an
// episode, its title and type can differ from the episode's own.
type PlaybackFileItem struct {
	Title string
	Type  string
}

type Envelope struct {
	Channel   EventChannel    `json:"channel"`
	Event     string          `json:"event"`
	EventID   string          `json:"event_id"`
	Timestamp time.Time       `json:"timestamp"`
	SourceID  string          `json:"source_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	UserID    int             `json:"user_id,omitempty"`
	ProfileID string          `json:"profile_id,omitempty"`
	AdminOnly bool            `json:"admin_only,omitempty"`
	// AllViewers addresses a user_settings event to every connection rather
	// than to one account or profile. It is honored only on an envelope that
	// names no account and no profile, and is reserved for server-wide changes
	// such as an administrator default.
	AllViewers bool `json:"all_viewers,omitempty"`
	// V2Only keeps the event away from plugins: only events socket
	// connections receive it.
	V2Only bool `json:"v2_only,omitempty"`
	// TargetPluginID, when non-empty, restricts dispatch to a single installed
	// plugin (and only if it is already a subscriber). Used by
	// RuntimeHostServer.PublishEventTo.
	TargetPluginID string `json:"target_plugin_id,omitempty"`
	// EpisodeFileItem supplies the local plugin dispatcher with the file's
	// catalog item. The socket and the cross-node relay use Data.
	EpisodeFileItem *PlaybackFileItem `json:"-"`
}

type PublishOptions struct {
	EpisodeFileItem *PlaybackFileItem
	EventID         string
	UserID          int
	ProfileID       string
	AdminOnly       bool
	AllViewers      bool
	V2Only          bool
}

type EventsHelloMessage struct {
	Type              string         `json:"type"`
	SchemaVersion     int            `json:"schema_version"`
	ConnectionID      string         `json:"connection_id"`
	AvailableChannels []EventChannel `json:"available_channels"`
	RequiredAction    string         `json:"required_action"`
}

type EventsSubscribeMessage struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id,omitempty"`
	Channels  []EventChannel `json:"channels"`
}

type EventsRejectedChannel struct {
	Channel EventChannel `json:"channel"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
}

type EventsSubscribedMessage struct {
	Type      string                  `json:"type"`
	RequestID string                  `json:"request_id,omitempty"`
	Channels  []EventChannel          `json:"channels"`
	Rejected  []EventsRejectedChannel `json:"rejected,omitempty"`
}

type EventsSnapshotMessage struct {
	Type      string          `json:"type"`
	Channel   EventChannel    `json:"channel"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type EventsEventMessage struct {
	Type      string          `json:"type"`
	Channel   EventChannel    `json:"channel"`
	Event     string          `json:"event"`
	EventID   string          `json:"event_id"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// EventsAccessChangedMessage tells an events-socket client that the access its
// connection was opened under has changed. The server closes the connection
// right after it; see docs/realtime-api.md.
type EventsAccessChangedMessage struct {
	Type string `json:"type"`
}

type EventsErrorMessage struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
