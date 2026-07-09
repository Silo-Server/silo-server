package notifications

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests"
)

// serverChannelRequestPostTimeout bounds one detached lifecycle post fan-out
// (up to serverChannelMaxChannels sequential sends at 10s each is the
// theoretical worst case; in practice a couple of channels are subscribed).
const serverChannelRequestPostTimeout = 60 * time.Second

// requestEventInfoFor converts a request into the payload-layer shape.
func requestEventInfoFor(req requests.Request) RequestEventInfo {
	info := RequestEventInfo{
		RequestID:       req.ID,
		TMDBID:          req.TMDBID,
		IMDBID:          req.IMDbID,
		MediaType:       string(req.MediaType),
		Title:           req.Title,
		Overview:        req.Overview,
		PosterPath:      req.PosterPath,
		RequesterName:   req.RequesterUsername,
		RequesterUserID: req.RequestedByUserID,
	}
	if req.Year != nil {
		info.Year = *req.Year
	}
	if req.TVDBID != nil {
		info.TVDBID = *req.TVDBID
	}
	return info
}

// PostServerChannelRequestEvent posts one request lifecycle event to opted-in
// server channels on a detached goroutine. No-op when server channels are not
// configured. Best-effort: the caller's flow never blocks on it.
func (s *System) PostServerChannelRequestEvent(ctx context.Context, event string, info RequestEventInfo) {
	if s == nil || s.serverChannelWorker == nil {
		return
	}
	// The caller's context ends with its HTTP request or reconcile pass;
	// posting continues on its own deadline (the detector's detach pattern).
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serverChannelRequestPostTimeout)
	go func() {
		defer cancel()
		s.serverChannelWorker.PostRequestEvent(postCtx, event, info)
	}()
}

// requesterDiscordID resolves a requester's OAuth-linked Discord user id for
// @mentions in server-channel request posts. Empty when the admin has not
// enabled requester mentions, the account never linked Discord, or the lookup
// fails — the post then falls back to the plain username. Wired into the
// sweep worker, which calls it only when a Discord channel is about to
// receive the event.
func (s *System) requesterDiscordID(ctx context.Context, userID int) string {
	if userID <= 0 || s.DiscordPrefs == nil || !s.Settings.ServerChannelMentionRequesters(ctx) {
		return ""
	}
	prefs, err := s.DiscordPrefs.Get(ctx, userID)
	if err != nil {
		s.logger.WarnContext(ctx, "server channel request post: discord identity lookup failed",
			"user_id", userID, "error", err)
		return ""
	}
	return prefs.DiscordUserID
}
