package notifications

import (
	"context"
	"fmt"
	"time"

	publicevents "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/events"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/requests"
)

// RequestEventPayload renders a request in the plugin event catalog's
// RequestEvent shape: what plugins receive for a request lifecycle event.
func RequestEventPayload(req requests.Request) publicevents.RequestEvent {
	return publicevents.RequestEvent{
		ID:                   req.ID,
		MediaType:            string(req.MediaType),
		Title:                req.Title,
		Year:                 req.Year,
		TMDBID:               req.TMDBID,
		TVDBID:               req.TVDBID,
		IMDbID:               req.IMDbID,
		PosterPath:           req.PosterPath,
		Status:               string(req.Status),
		RequestedByUserID:    req.RequestedByUserID,
		RequestedByProfileID: req.RequestedByProfileID,
		RequesterName:        req.RequesterUsername,
		Reason:               req.OutcomeReason,
		LibraryContentID:     req.LibraryContentID,
		CreatedAt:            req.CreatedAt,
	}
}

// requestEventPublishTimeout bounds one detached request event publish.
const requestEventPublishTimeout = 30 * time.Second

// publishRequestEvent tells plugins about a request lifecycle event. It is
// published on the plugin channel, which no websocket client receives. The
// event ID is derived from the request and its last update, so a repeated
// call for the same transition carries the same ID and a plugin can skip it.
// Best-effort and detached, like the server-channel post beside it: the hub
// also publishes to Redis, and a request transition never waits on that.
func (s *System) publishRequestEvent(ctx context.Context, event string, req requests.Request) {
	if s == nil || s.events == nil {
		return
	}
	payload := RequestEventPayload(req)
	opts := evt.PublishOptions{
		EventID:   fmt.Sprintf("%s:%s:%d", event, req.ID, req.UpdatedAt.UnixNano()),
		AdminOnly: true,
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestEventPublishTimeout)
	go func() {
		defer cancel()
		if err := s.events.PublishJSON(publishCtx, evt.ChannelPlugins, event, payload, opts); err != nil {
			s.logger.WarnContext(publishCtx, "publish request event for plugins failed", "request_id", req.ID, "event", event, "error", err)
		}
	}()
}
