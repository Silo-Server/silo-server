package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const compatSessionTranscode = "Transcode"

type sessionPlayStateDTO struct {
	PositionTicks       int64  `json:"PositionTicks"`
	IsPaused            bool   `json:"IsPaused"`
	PlayMethod          string `json:"PlayMethod,omitempty"`
	AudioStreamIndex    *int   `json:"AudioStreamIndex,omitempty"`
	SubtitleStreamIndex *int   `json:"SubtitleStreamIndex,omitempty"`
}

type sessionInfoDTO struct {
	ID                    string               `json:"Id"`
	UserID                string               `json:"UserId"`
	UserName              string               `json:"UserName,omitempty"`
	Client                string               `json:"Client,omitempty"`
	DeviceID              string               `json:"DeviceId,omitempty"`
	DeviceName            string               `json:"DeviceName,omitempty"`
	LastActivityDate      string               `json:"LastActivityDate"`
	IsActive              bool                 `json:"IsActive"`
	SupportsMediaControl  bool                 `json:"SupportsMediaControl"`
	SupportsRemoteControl bool                 `json:"SupportsRemoteControl"`
	PlayState             *sessionPlayStateDTO `json:"PlayState,omitempty"`
	NowPlayingItem        *baseItemDTO         `json:"NowPlayingItem,omitempty"`
}

// HandleSessions lists playback sessions owned by this exact login/API token.
// Remote control is unavailable, so controllable-session queries return empty.
func (h *PlaybackHandler) HandleSessions(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, 401, "Unauthorized", "Missing authentication token")
		return
	}
	q := newCaseInsensitiveQuery(r.URL.Query())
	result := make([]sessionInfoDTO, 0)
	if q.Get("controllableByUserId") != "" {
		writeJSON(w, 200, result)
		return
	}
	activeWithin := int64(0)
	filterActivity := false
	if raw := q.Get("activeWithinSeconds"); raw != "" {
		var err error
		filterActivity = true
		activeWithin, err = strconv.ParseInt(raw, 10, 32)
		if err != nil || activeWithin < 0 {
			writeError(w, 400, "BadRequest", "Invalid activeWithinSeconds")
			return
		}
	}
	lister, ok := h.playbackStore.(interface {
		ListActiveForToken(context.Context, string) ([]PlaybackSession, error)
	})
	if !ok {
		writeCompatUpstreamError(w, fmt.Errorf("session listing unavailable"))
		return
	}
	sessions, err := lister.ListActiveForToken(r.Context(), session.Token)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	for _, play := range sessions {
		if play.UpstreamSessionID == "" || q.Get("deviceId") != "" && q.Get("deviceId") != play.ClientDeviceID {
			continue
		}
		dto := sessionInfoDTO{ID: play.ID, UserID: session.PseudoUserID.String(), UserName: session.Username, DeviceID: play.ClientDeviceID, LastActivityDate: play.UpdatedAt.UTC().Format(time.RFC3339Nano), IsActive: true}
		activity := play.UpdatedAt
		if h.sessionMgr != nil {
			if native, err := h.sessionMgr.GetSession(play.UpstreamSessionID); err == nil && native != nil && native.UserID == session.StreamAppUserID && native.ProfileID == session.ProfileID {
				dto.Client = native.ClientName
				activity = native.LastActivityAt
				if activity.IsZero() {
					activity = native.UpdatedAt
				}
				dto.LastActivityDate = activity.UTC().Format(time.RFC3339Nano)
				method := ""
				switch native.PlayMethod {
				case playback.PlayDirect:
					method = "DirectPlay"
				case playback.PlayRemux:
					method = "DirectStream"
				case playback.PlayTranscode:
					method = compatSessionTranscode
				}
				dto.PlayState = &sessionPlayStateDTO{PositionTicks: secondsToTicks(native.Position), IsPaused: native.IsPaused, PlayMethod: method}
				if len(play.MediaSources) > 0 {
					dto.PlayState.AudioStreamIndex = play.MediaSources[0].SelectedAudioStreamIndex
					dto.PlayState.SubtitleStreamIndex = play.MediaSources[0].SelectedSubtitleStreamIndex
				}
			}
		}
		if filterActivity && time.Since(activity) > time.Duration(activeWithin)*time.Second {
			continue
		}
		if h.content != nil {
			detail, err := h.content.GetItemDetail(r.Context(), session, play.ItemID, nil)
			if err != nil {
				continue
			}
			item := newMapper(h.codec, h.cfg).itemFromDetail(*detail, false, nil)
			dto.NowPlayingItem = &item
		}
		result = append(result, dto)
	}
	writeJSON(w, 200, result)
}

// HandleSessionPlayingPing refreshes activity without reporting a zero position.
func (h *PlaybackHandler) HandleSessionPlayingPing(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, 401, "Unauthorized", "Missing authentication token")
		return
	}
	id := newCaseInsensitiveQuery(r.URL.Query()).Get("playSessionId")
	if id == "" {
		writeError(w, 400, "BadRequest", "playSessionId is required")
		return
	}
	play, ok := h.playbackStore.Get(id)
	if !ok {
		play, ok = h.playbackStore.FindByClientPlaySessionID(session.Token, id)
	}
	if !ok || play.CompatToken != session.Token {
		writeError(w, 404, "NotFound", "Playback session not found")
		return
	}

	toucher, ok := h.playbackStore.(interface {
		TouchActiveForToken(context.Context, string, string) error
	})
	if !ok {
		writeCompatUpstreamError(w, fmt.Errorf("durable session keepalive unavailable"))
		return
	}
	if err := toucher.TouchActiveForToken(r.Context(), play.ID, session.Token); err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	if play.UpstreamSessionID != "" && h.sessionMgr != nil {
		if native, err := h.sessionMgr.GetSession(play.UpstreamSessionID); err == nil && native != nil && native.UserID == session.StreamAppUserID && native.ProfileID == session.ProfileID {
			if toucher, ok := h.sessionMgr.(interface{ TouchActivity(string) error }); ok {
				if err := toucher.TouchActivity(play.UpstreamSessionID); err != nil {
					writeCompatUpstreamError(w, err)
					return
				}
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
