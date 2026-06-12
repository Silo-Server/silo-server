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

// ServerChannelLifecycleNotifier adapts request lifecycle transitions to
// server-channel posts. Sends are detached and best-effort: the request flow
// never waits on or fails because of a broadcast destination.
type ServerChannelLifecycleNotifier struct {
	system *System
}

// NewServerChannelLifecycleNotifier creates the adapter; returns nil when the
// system has no server-channel support (no at-rest cipher).
func NewServerChannelLifecycleNotifier(system *System) *ServerChannelLifecycleNotifier {
	if system == nil || system.serverChannelWorker == nil {
		return nil
	}
	return &ServerChannelLifecycleNotifier{system: system}
}

// RequestSubmitted implements requests.LifecycleNotifier.
func (n *ServerChannelLifecycleNotifier) RequestSubmitted(ctx context.Context, req requests.Request) {
	n.post(ctx, ServerChannelEventRequestSubmitted, req)
}

// RequestApproved implements requests.LifecycleNotifier.
func (n *ServerChannelLifecycleNotifier) RequestApproved(ctx context.Context, req requests.Request) {
	n.post(ctx, ServerChannelEventRequestApproved, req)
}

// RequestDeclined implements requests.LifecycleNotifier.
func (n *ServerChannelLifecycleNotifier) RequestDeclined(ctx context.Context, req requests.Request) {
	n.post(ctx, ServerChannelEventRequestDeclined, req)
}

func (n *ServerChannelLifecycleNotifier) post(ctx context.Context, event string, req requests.Request) {
	if n == nil || n.system == nil {
		return
	}
	n.system.PostServerChannelRequestEvent(ctx, event, requestEventInfoFor(req))
}

// requestEventInfoFor converts a request into the payload-layer shape.
func requestEventInfoFor(req requests.Request) RequestEventInfo {
	info := RequestEventInfo{
		RequestID:     req.ID,
		TMDBID:        req.TMDBID,
		MediaType:     string(req.MediaType),
		Title:         req.Title,
		RequesterName: req.RequesterUsername,
	}
	if req.Year != nil {
		info.Year = *req.Year
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
