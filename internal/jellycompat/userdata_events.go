package jellycompat

import (
	"context"
	"encoding/json"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// UserStateEvents is the realtime hub that carries per-profile watched-state
// changes between API replicas. First-party handlers publish on it, and so do
// the compatibility mutations here. The compatibility socket turns the events
// into Jellyfin UserDataChanged notifications.
type UserStateEvents interface {
	PublishJSON(ctx context.Context, channel evt.EventChannel, event string, data any, opts evt.PublishOptions) error
	Subscribe() (<-chan evt.Envelope, func())
}

// userStateChangedEvent matches the first-party user_state event name and
// payload, so first-party clients and compatibility sockets read the same
// events whichever surface made the change.
const userStateChangedEvent = "user_state.changed"

// userStateChangeWatched is the first-party change kind for a watched-state
// update.
const userStateChangeWatched = "watched"

// wsUserDataChanged is Jellyfin's socket message type for user-data updates.
const wsUserDataChanged = "UserDataChanged"

// maxWatchedEventsPerBatch caps the events one batch mutation publishes. A
// Jellyfin client refreshes a whole list from any single UserDataChanged
// entry, and marking a long series must not flood the hub's subscriber
// buffers.
const maxWatchedEventsPerBatch = 25

type userStateChangedPayload struct {
	ProfileID  string `json:"profile_id"`
	ContentID  string `json:"content_id,omitempty"`
	SeriesID   string `json:"series_id,omitempty"`
	Change     string `json:"change"`
	Played     *bool  `json:"played,omitempty"`
	IsFavorite *bool  `json:"is_favorite,omitempty"`
}

// publishWatchedChange announces a successful compatibility watched-state
// mutation. It never fails the mutation: the write has already committed.
func publishWatchedChange(ctx context.Context, events UserStateEvents, session *Session, contentIDs []string, played bool) {
	if events == nil || session == nil || session.StreamAppUserID == 0 || session.ProfileID == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	published := 0
	for _, contentID := range contentIDs {
		if contentID == "" {
			continue
		}
		if published == maxWatchedEventsPerBatch {
			return
		}
		played := played
		_ = events.PublishJSON(ctx, evt.ChannelUserState, userStateChangedEvent, userStateChangedPayload{
			ProfileID: session.ProfileID,
			ContentID: contentID,
			Change:    userStateChangeWatched,
			Played:    &played,
		}, evt.PublishOptions{
			UserID:    session.StreamAppUserID,
			ProfileID: session.ProfileID,
		})
		published++
	}
}

// userDataChangedMessage is Jellyfin's UserDataChanged socket message.
type userDataChangedMessage struct {
	UserID       string                 `json:"UserId"`
	UserDataList []userDataChangedEntry `json:"UserDataList"`
}

type userDataChangedEntry struct {
	ItemID     string `json:"ItemId"`
	Key        string `json:"Key"`
	Played     *bool  `json:"Played,omitempty"`
	IsFavorite *bool  `json:"IsFavorite,omitempty"`
}

// userDataChangedFor translates a hub envelope into a UserDataChanged socket
// message for session, or reports false when the envelope is not a user-state
// change for that session's account and profile.
func userDataChangedFor(env evt.Envelope, session *Session, codec *ResourceIDCodec) (wsMessage, bool) {
	if session == nil || codec == nil || env.Channel != evt.ChannelUserState {
		return wsMessage{}, false
	}
	if env.UserID == 0 || env.UserID != session.StreamAppUserID {
		return wsMessage{}, false
	}
	var payload userStateChangedPayload
	if len(env.Data) == 0 || json.Unmarshal(env.Data, &payload) != nil {
		return wsMessage{}, false
	}
	profileID := env.ProfileID
	if profileID == "" {
		profileID = payload.ProfileID
	}
	if profileID == "" || profileID != session.ProfileID || payload.ContentID == "" {
		return wsMessage{}, false
	}
	itemID := codec.EncodeStringID(EncodedIDItem, payload.ContentID)
	data, err := json.Marshal(userDataChangedMessage{
		UserID: session.PseudoUserID.String(),
		UserDataList: []userDataChangedEntry{{
			ItemID:     itemID,
			Key:        itemID,
			Played:     payload.Played,
			IsFavorite: payload.IsFavorite,
		}},
	})
	if err != nil {
		return wsMessage{}, false
	}
	return wsMessage{MessageType: wsUserDataChanged, Data: data}, true
}
