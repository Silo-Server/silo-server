package playback

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Realtime message types exchanged over /playback/ws/{session_id}.
type RealtimeMessageType string

const (
	RealtimeMessageTypeCommand RealtimeMessageType = "command"
	RealtimeMessageTypeEvent   RealtimeMessageType = "event"
	RealtimeMessageTypeHello   RealtimeMessageType = "hello"
	RealtimeMessageTypeAck     RealtimeMessageType = "ack"
	RealtimeMessageTypeResult  RealtimeMessageType = "result"
)

// RealtimeEventName identifies a supported server-pushed event.
type RealtimeEventName string

const (
	RealtimeEventChapterThumbnailReady RealtimeEventName = "chapter_thumbnail_ready"
	RealtimeEventMarkersUpdated        RealtimeEventName = "markers_updated"
)

var supportedRealtimeEventNameSet = map[RealtimeEventName]struct{}{
	RealtimeEventChapterThumbnailReady: {},
	RealtimeEventMarkersUpdated:        {},
}

// CommandName identifies a supported realtime command.
type CommandName string

const (
	CommandPause              CommandName = "pause"
	CommandUnpause            CommandName = "unpause"
	CommandPlayPause          CommandName = "play_pause"
	CommandSeek               CommandName = "seek"
	CommandSetVolume          CommandName = "set_volume"
	CommandStop               CommandName = "stop"
	CommandTerminate          CommandName = "terminate"
	CommandDisplayMessage     CommandName = "display_message"
	CommandServerRestarting   CommandName = "server_restarting"
	CommandServerShuttingDown CommandName = "server_shutting_down"
	CommandPlayMedia          CommandName = "play_media"
	CommandSetAudioTrack      CommandName = "set_audio_track"
	CommandSetSubtitleTrack   CommandName = "set_subtitle_track"
)

var supportedCommandNames = []CommandName{
	CommandPause,
	CommandUnpause,
	CommandPlayPause,
	CommandSeek,
	CommandSetVolume,
	CommandStop,
	CommandTerminate,
	CommandDisplayMessage,
	CommandServerRestarting,
	CommandServerShuttingDown,
	CommandPlayMedia,
	CommandSetAudioTrack,
	CommandSetSubtitleTrack,
}

var supportedCommandNameSet = func() map[CommandName]struct{} {
	names := map[CommandName]struct{}{}
	for _, name := range supportedCommandNames {
		names[name] = struct{}{}
	}
	return names
}()

// RealtimeAckStatus represents the client acknowledgement state.
type RealtimeAckStatus string

const (
	RealtimeAckStatusAccepted RealtimeAckStatus = "accepted"
)

// RealtimeResultStatus represents the client completion state.
type RealtimeResultStatus string

const (
	RealtimeResultStatusCompleted RealtimeResultStatus = "completed"
	RealtimeResultStatusRejected  RealtimeResultStatus = "rejected"
)

var (
	ErrUnsupportedCommandName = errors.New("unsupported command name")
	ErrUnsupportedEventName   = errors.New("unsupported realtime event name")
	ErrInvalidRealtimePayload = errors.New("invalid realtime payload")
)

// EventEnvelope is the server-to-client realtime event message.
type EventEnvelope struct {
	Type      RealtimeMessageType `json:"type"`
	SessionID string              `json:"session_id"`
	Name      RealtimeEventName   `json:"name"`
	Payload   json.RawMessage     `json:"payload,omitempty"`
}

// ChapterThumbnailReadyPayload describes one chapter thumbnail that became available.
type ChapterThumbnailReadyPayload struct {
	SessionID          string `json:"session_id"`
	FileID             int    `json:"file_id"`
	ChapterIndex       int    `json:"chapter_index"`
	ThumbnailURL       string `json:"thumbnail_url"`
	ThumbnailThumbhash string `json:"thumbnail_thumbhash,omitempty"`
}

type TimeRangePayload struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type MarkersUpdatedPayload struct {
	SessionID string            `json:"session_id"`
	FileID    int               `json:"file_id"`
	Intro     *TimeRangePayload `json:"intro"`
	Credits   *TimeRangePayload `json:"credits"`
	Recap     *TimeRangePayload `json:"recap"`
	Preview   *TimeRangePayload `json:"preview"`
}

// NewEventEnvelope creates a validated realtime event envelope.
func NewEventEnvelope(sessionID string, name RealtimeEventName, payload json.RawMessage) (EventEnvelope, error) {
	normalizedPayload, err := normalizeJSONPayload(payload)
	if err != nil {
		return EventEnvelope{}, err
	}
	env := EventEnvelope{
		Type:      RealtimeMessageTypeEvent,
		SessionID: sessionID,
		Name:      name,
		Payload:   normalizedPayload,
	}
	if err := env.Validate(); err != nil {
		return EventEnvelope{}, err
	}
	return env, nil
}

// NewChapterThumbnailReadyEvent creates a validated chapter thumbnail event.
func NewChapterThumbnailReadyEvent(
	sessionID string,
	fileID int,
	chapterIndex int,
	thumbnailURL string,
	thumbnailThumbhash string,
) (EventEnvelope, error) {
	payload, err := json.Marshal(ChapterThumbnailReadyPayload{
		SessionID:          sessionID,
		FileID:             fileID,
		ChapterIndex:       chapterIndex,
		ThumbnailURL:       thumbnailURL,
		ThumbnailThumbhash: thumbnailThumbhash,
	})
	if err != nil {
		return EventEnvelope{}, err
	}
	return NewEventEnvelope(sessionID, RealtimeEventChapterThumbnailReady, payload)
}

func NewMarkersUpdatedEvent(
	sessionID string,
	fileID int,
	intro *TimeRangePayload,
	credits *TimeRangePayload,
	recap *TimeRangePayload,
	preview *TimeRangePayload,
) (EventEnvelope, error) {
	payload, err := json.Marshal(MarkersUpdatedPayload{
		SessionID: sessionID,
		FileID:    fileID,
		Intro:     intro,
		Credits:   credits,
		Recap:     recap,
		Preview:   preview,
	})
	if err != nil {
		return EventEnvelope{}, err
	}
	return NewEventEnvelope(sessionID, RealtimeEventMarkersUpdated, payload)
}

// ParseEventEnvelope decodes and validates a realtime event envelope.
func ParseEventEnvelope(data []byte) (EventEnvelope, error) {
	var env EventEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return EventEnvelope{}, err
	}
	normalizedPayload, err := normalizeJSONPayload(env.Payload)
	if err != nil {
		return EventEnvelope{}, err
	}
	env.Payload = normalizedPayload
	if err := env.Validate(); err != nil {
		return EventEnvelope{}, err
	}
	return env, nil
}

// Validate checks the event envelope shape.
func (e *EventEnvelope) Validate() error {
	if e == nil {
		return ErrInvalidRealtimePayload
	}
	if e.Type != RealtimeMessageTypeEvent {
		return fmt.Errorf("event envelope type must be %q", RealtimeMessageTypeEvent)
	}
	if e.SessionID == "" {
		return ErrInvalidRealtimePayload
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	} else if !json.Valid(e.Payload) {
		return ErrInvalidRealtimePayload
	}
	if _, ok := supportedRealtimeEventNameSet[e.Name]; !ok {
		return ErrUnsupportedEventName
	}
	return nil
}

// CommandEnvelope is the server-to-client command message.
type CommandEnvelope struct {
	Type       RealtimeMessageType `json:"type"`
	CommandID  string              `json:"command_id"`
	SessionID  string              `json:"session_id"`
	Name       CommandName         `json:"name"`
	Reason     string              `json:"reason,omitempty"`
	IssuedBy   *CommandIssuedBy    `json:"issued_by,omitempty"`
	DeadlineMS int                 `json:"deadline_ms,omitempty"`
	Payload    json.RawMessage     `json:"payload,omitempty"`
}

// CommandIssuedBy identifies the source of a command.
type CommandIssuedBy struct {
	Kind string `json:"kind"`
}

// NewCommandEnvelope creates a validated command envelope.
func NewCommandEnvelope(sessionID, commandID string, name CommandName, payload json.RawMessage) (CommandEnvelope, error) {
	normalizedPayload, err := normalizeJSONPayload(payload)
	if err != nil {
		return CommandEnvelope{}, err
	}
	env := CommandEnvelope{
		Type:      RealtimeMessageTypeCommand,
		CommandID: commandID,
		SessionID: sessionID,
		Name:      name,
		Payload:   normalizedPayload,
	}
	if err := env.Validate(); err != nil {
		return CommandEnvelope{}, err
	}
	return env, nil
}

// ParseCommandEnvelope decodes and validates a command envelope.
func ParseCommandEnvelope(data []byte) (CommandEnvelope, error) {
	var env CommandEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return CommandEnvelope{}, err
	}
	normalizedPayload, err := normalizeJSONPayload(env.Payload)
	if err != nil {
		return CommandEnvelope{}, err
	}
	env.Payload = normalizedPayload
	if err := env.Validate(); err != nil {
		return CommandEnvelope{}, err
	}
	return env, nil
}

// Validate checks the envelope for required fields and supported command names.
func (e *CommandEnvelope) Validate() error {
	if e == nil {
		return ErrInvalidRealtimePayload
	}
	if e.Type != RealtimeMessageTypeCommand {
		return fmt.Errorf("command envelope type must be %q", RealtimeMessageTypeCommand)
	}
	if e.CommandID == "" || e.SessionID == "" {
		return ErrInvalidRealtimePayload
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	} else if !json.Valid(e.Payload) {
		return ErrInvalidRealtimePayload
	}
	if err := ValidateCommandName(e.Name); err != nil {
		return err
	}
	return nil
}

// ValidateCommandName reports whether a command is supported.
func ValidateCommandName(name CommandName) error {
	if _, ok := supportedCommandNameSet[name]; !ok {
		return ErrUnsupportedCommandName
	}
	return nil
}

// SupportedCommandNames returns a copy of the supported command names.
func SupportedCommandNames() []CommandName {
	out := make([]CommandName, len(supportedCommandNames))
	copy(out, supportedCommandNames)
	return out
}

// HelloEnvelope is the client hello message.
type HelloEnvelope struct {
	Type         RealtimeMessageType `json:"type"`
	SessionID    string              `json:"session_id"`
	Client       HelloClientInfo     `json:"client"`
	Capabilities HelloCapabilities   `json:"capabilities"`
}

// HelloClientInfo identifies the client implementation.
type HelloClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// HelloCapabilities lists supported commands.
type HelloCapabilities struct {
	Commands []CommandName `json:"commands"`
}

// Validate checks the hello envelope shape and capability names.
func (e HelloEnvelope) Validate() error {
	if e.Type != RealtimeMessageTypeHello {
		return fmt.Errorf("hello envelope type must be %q", RealtimeMessageTypeHello)
	}
	if e.SessionID == "" || e.Client.Name == "" || e.Client.Version == "" {
		return ErrInvalidRealtimePayload
	}
	for _, name := range e.Capabilities.Commands {
		if err := ValidateCommandName(name); err != nil {
			return err
		}
	}
	return nil
}

// AckEnvelope is the client acknowledgement message.
type AckEnvelope struct {
	Type      RealtimeMessageType `json:"type"`
	CommandID string              `json:"command_id"`
	SessionID string              `json:"session_id"`
	Status    RealtimeAckStatus   `json:"status"`
}

// Validate checks the ack envelope shape.
func (e AckEnvelope) Validate() error {
	if e.Type != RealtimeMessageTypeAck {
		return fmt.Errorf("ack envelope type must be %q", RealtimeMessageTypeAck)
	}
	if e.CommandID == "" || e.SessionID == "" || e.Status == "" {
		return ErrInvalidRealtimePayload
	}
	if e.Status != RealtimeAckStatusAccepted {
		return ErrInvalidRealtimePayload
	}
	return nil
}

// ResultEnvelope is the client completion message.
type ResultEnvelope struct {
	Type      RealtimeMessageType  `json:"type"`
	CommandID string               `json:"command_id"`
	SessionID string               `json:"session_id"`
	Status    RealtimeResultStatus `json:"status"`
	Error     string               `json:"error,omitempty"`
}

// Validate checks the result envelope shape.
func (e ResultEnvelope) Validate() error {
	if e.Type != RealtimeMessageTypeResult {
		return fmt.Errorf("result envelope type must be %q", RealtimeMessageTypeResult)
	}
	if e.CommandID == "" || e.SessionID == "" || e.Status == "" {
		return ErrInvalidRealtimePayload
	}
	switch e.Status {
	case RealtimeResultStatusCompleted, RealtimeResultStatusRejected:
		return nil
	default:
		return ErrInvalidRealtimePayload
	}
}

func normalizeJSONPayload(payload json.RawMessage) (json.RawMessage, error) {
	if len(payload) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(payload) {
		return nil, ErrInvalidRealtimePayload
	}
	return payload, nil
}
