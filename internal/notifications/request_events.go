package notifications

import (
	"context"
	"fmt"

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

// publishRequestEvent tells plugins about a request lifecycle event. It is
// published on the plugin channel, which no websocket client receives. The
// event ID is derived from the request and its last update, so a repeated
// call for the same transition carries the same ID and a plugin can skip it.
// Best-effort, like the server-channel post beside it.
func (s *System) publishRequestEvent(ctx context.Context, event string, req requests.Request) {
	if s == nil || s.events == nil {
		return
	}
	if err := s.events.PublishJSON(ctx, evt.ChannelPlugins, event, RequestEventPayload(req), evt.PublishOptions{
		EventID:   fmt.Sprintf("%s:%s:%d", event, req.ID, req.UpdatedAt.UnixNano()),
		AdminOnly: true,
	}); err != nil {
		s.logger.WarnContext(ctx, "publish request event for plugins failed", "request_id", req.ID, "event", event, "error", err)
	}
}
