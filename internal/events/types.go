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
	ChannelPlugins       EventChannel = "plugins"
	ChannelNotifications EventChannel = "notifications"
	ChannelRequests      EventChannel = "requests"
)

var AllChannels = []EventChannel{
	ChannelCatalog,
	ChannelJobs,
	ChannelSessions,
	ChannelTasks,
	ChannelScans,
	ChannelHistoryImport,
	ChannelUserState,
	ChannelPlugins,
	ChannelNotifications,
	ChannelRequests,
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
	// TargetPluginID, when non-empty, restricts dispatch to a single installed
	// plugin (and only if it is already a subscriber). Used by
	// RuntimeHostServer.PublishEventTo.
	TargetPluginID string `json:"target_plugin_id,omitempty"`
}

type PublishOptions struct {
	EventID   string
	UserID    int
	ProfileID string
	AdminOnly bool
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

type EventsErrorMessage struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
