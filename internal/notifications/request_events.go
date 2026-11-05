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

// requestEventPublishTimeout bounds the hub's Redis publish of one request
// event. Plugins on this node already have the event by then.
const requestEventPublishTimeout = 5 * time.Second

// publishRequestEvent tells plugins about a request lifecycle event. It is
// published on the plugin channel, which no websocket client receives. The
// event ID is derived from the request and its last update, so a repeated
// call for the same transition carries the same ID and a plugin can skip it.
//
// It publishes inline, so one request's events reach plugins in transition
// order and none is lost once the plugin dispatcher stops at shutdown. The
// hub hands the event to local subscribers, the plugin dispatcher among them,
// before its Redis publish, so only that bounded Redis call can delay the
// caller. The transition has already committed, so the publish ignores the
// caller's cancellation; a failure is logged.
func (s *System) publishRequestEvent(ctx context.Context, event string, req requests.Request) {
	if s == nil || s.events == nil {
		return
	}
	opts := evt.PublishOptions{
		EventID:   fmt.Sprintf("%s:%s:%d", event, req.ID, req.UpdatedAt.UnixNano()),
		AdminOnly: true,
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestEventPublishTimeout)
	defer cancel()
	if err := s.events.PublishJSON(publishCtx, evt.ChannelPlugins, event, RequestEventPayload(req), opts); err != nil {
		s.logger.WarnContext(ctx, "publish request event for plugins failed", "request_id", req.ID, "event", event, "error", err)
	}
}
