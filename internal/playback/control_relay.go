package playback

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Control relay: run a playback control request on the API replica that holds
// the session's realtime lane.
//
// A session's control lane (RealtimeHub), the tracker that enforces a
// command's deadline and the administrator command ledger live in the process
// that accepted the player's control socket. Behind a load balancer an
// administrator request can land on any replica, which finds no lane and,
// usually, no session either.
//
// The relay records lane ownership in Redis: one key per session naming the
// replica holding the lane and when its connection took the lane. A replica
// claims the key when a lane opens or a new connection takes it over, and
// re-asserts the claim on every refresh while the lane stays open. A claim
// never replaces one for a newer connection, so when a player reconnects to
// another replica before its old socket closes, the replica with the newest
// connection holds the key whatever order the replicas write in, and even
// when a lane notification was dropped. The replica deletes the key when the
// lane closes if it still names this replica. Every request, including one on
// a replica that also holds a lane, looks the owner up; when another replica
// owns the key the request is published on cache.ChannelPlaybackControl
// addressed to it. The owner checks that it still holds the lane, runs the
// request through its local handler, and publishes the reply addressed back.
// Every request for a session therefore executes in the one process that
// holds its newest lane, so the command ledger's applied-once ordering holds
// across the hop.
//
// Failure model. Forward reports ControlRelayServeLocally, and the caller
// serves the request exactly as a single replica would, when this replica
// owns the key, when no replica claims it, when the ownership read fails, or
// when the owner answers that it no longer holds the lane and one retry at a
// changed owner also declines: in each case the owner never ran the request.
// An unchanged or absent owner after a decline also uses the local path.
// Once Forward has tried to publish a request,
// Forward reports ControlRelayNoReply unless a reply arrives within
// controlRelayReplyTimeout: the owner may still have run it, so the caller
// must not assume it did not. That includes a publish that fails, because
// Redis may have delivered a PUBLISH whose answer the client never read. An
// owner that died leaves its claim until the TTL lapses or the player
// reconnects to a surviving replica, which claims the key afresh.
//
// Without Redis there is no relay (NewControlRelay returns nil) and every
// request is served locally, which is complete on a single replica.

const (
	controlRelayLaneKeyPrefix = "silo:playback:lane:"

	// controlRelayLaneTTL bounds how long a dead replica's claim survives.
	controlRelayLaneTTL = 30 * time.Second
	// controlRelayRefreshInterval re-asserts the claims of open lanes well
	// inside the TTL.
	controlRelayRefreshInterval = 10 * time.Second
	// controlRelayReplyTimeout bounds the wait for the owner's reply. The
	// owner's work is one bounded lane write (5 s socket write deadline) plus,
	// for a terminate, the session stop; the margin covers the bus.
	controlRelayReplyTimeout = 8 * time.Second
	// controlRelayHandlerTimeout bounds a relayed request on the owner. It is
	// longer than the reply wait because a request served there is not
	// abandoned when its caller stops waiting, as a local administrator
	// request runs until its HTTP response is written.
	controlRelayHandlerTimeout = 2 * time.Minute
	// controlRelayRedisTimeout bounds one ownership read or write.
	controlRelayRedisTimeout = time.Second
	// controlRelayRefreshBatch bounds the claims a refresh has in flight at
	// once, so a sweep over many lanes takes lanes/batch round trips rather
	// than one per lane, without taking much of the shared Redis pool.
	controlRelayRefreshBatch = 8
	// controlRelayQueueSize bounds pending lane open/close notifications; an
	// overflow is repaired by the next refresh, which claims with the same
	// connection time.
	controlRelayQueueSize = 256

	controlRelayKindRequest = "request"
	controlRelayKindReply   = "reply"
)

// ControlRelayHandler executes a relayed request on the owning replica and
// returns the reply body. It must not forward again.
type ControlRelayHandler func(ctx context.Context, sessionID, op string, body json.RawMessage) json.RawMessage

// ControlRelayOutcome says how Forward disposed of a request.
type ControlRelayOutcome int

const (
	// ControlRelayServeLocally: no other replica ran or will run the request;
	// the caller serves it.
	ControlRelayServeLocally ControlRelayOutcome = iota
	// ControlRelayHandled: the owner ran the request; the reply is its body.
	ControlRelayHandled
	// ControlRelayNoReply: the request was published but no reply arrived in
	// time. The owner may have run it, or may still run it.
	ControlRelayNoReply
)

// ControlRelayOwners records which replica holds a session's lane. The
// production store is Redis (NewControlRelay); tests supply their own.
type ControlRelayOwners interface {
	// Claim names owner, whose lane took its connection at connectedAt, and
	// extends the key's TTL, unless the key names another replica with a
	// newer connection (equal times go to the greater owner id). The newest
	// connection wins whatever order the claims arrive in.
	Claim(ctx context.Context, sessionID, owner string, connectedAt time.Time) error
	// Release deletes the key if it still names owner.
	Release(ctx context.Context, sessionID, owner string) error
	// Owner returns the replica holding the lane, or "" when none does.
	Owner(ctx context.Context, sessionID string) (string, error)
}

type controlRelayMessage struct {
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	SessionID string          `json:"session_id"`
	Op        string          `json:"op,omitempty"`
	Handled   bool            `json:"handled,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
}

// ControlRelay routes playback control requests to the lane owner. A nil
// *ControlRelay never forwards.
type ControlRelay struct {
	id     string
	hub    *RealtimeHub
	owners ControlRelayOwners

	replyTimeout    time.Duration
	refreshInterval time.Duration

	mu      sync.Mutex
	handler ControlRelayHandler
	publish func(context.Context, string) error
	pending map[string]chan controlRelayMessage
	events  chan string
	started bool
}

// NewControlRelay returns a relay for hub backed by client, or nil without
// Redis.
func NewControlRelay(hub *RealtimeHub, client *redis.Client) *ControlRelay {
	if hub == nil || client == nil {
		return nil
	}
	return NewControlRelayWithOwners(hub, &redisControlRelayOwners{client: client, ttl: controlRelayLaneTTL})
}

// NewControlRelayWithOwners returns a relay for hub that records lane
// ownership in owners.
func NewControlRelayWithOwners(hub *RealtimeHub, owners ControlRelayOwners) *ControlRelay {
	if hub == nil || owners == nil {
		return nil
	}
	return &ControlRelay{
		id:              uuid.NewString(),
		hub:             hub,
		owners:          owners,
		replyTimeout:    controlRelayReplyTimeout,
		refreshInterval: controlRelayRefreshInterval,
		pending:         make(map[string]chan controlRelayMessage),
		events:          make(chan string, controlRelayQueueSize),
	}
}

// Handle installs the handler that executes requests relayed to this replica.
func (r *ControlRelay) Handle(handler ControlRelayHandler) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.handler = handler
	r.mu.Unlock()
}

// Start subscribes to the control channel, starts tracking this replica's
// lanes and claims those already open. Call it once with the server lifetime
// context; publish and subscribe carry one payload on
// cache.ChannelPlaybackControl. An initial subscription error is returned, but
// startup keeps retrying until it succeeds or ctx ends. Until then commands
// use the local path. On ctx's end the replica releases its claims.
func (r *ControlRelay) Start(
	ctx context.Context,
	publish func(context.Context, string) error,
	subscribe func(context.Context, func(string)) error,
) error {
	if r == nil || publish == nil || subscribe == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}
	// Own startup, including retries, so another Start cannot subscribe twice.
	r.started = true
	r.mu.Unlock()
	if err := subscribe(ctx, r.receive); err != nil {
		go r.retrySubscription(ctx, publish, subscribe)
		return err
	}
	r.startSubscribed(ctx, publish)
	return nil
}

func (r *ControlRelay) retrySubscription(
	ctx context.Context,
	publish func(context.Context, string) error,
	subscribe func(context.Context, func(string)) error,
) {
	for delay := time.Second; ; delay = min(delay*2, 30*time.Second) {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		if err := subscribe(ctx, r.receive); err != nil {
			if ctx.Err() == nil {
				slog.WarnContext(ctx, "playback control relay subscription failed; retrying", "component", "playback", "error", err)
			}
			continue
		}
		r.startSubscribed(ctx, publish)
		return
	}
}

func (r *ControlRelay) startSubscribed(ctx context.Context, publish func(context.Context, string) error) {
	if ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	r.publish = publish
	r.mu.Unlock()
	r.hub.SetLaneObserver(r.observeLane)
	go r.run(ctx)
}

// Forward runs op for sessionID on the replica holding the session's newest
// lane and returns its reply body (see the failure model above for the
// outcomes). An explicit decline permits one retry at a changed owner within
// the original reply deadline; a missing reply never permits a retry.
func (r *ControlRelay) Forward(ctx context.Context, sessionID, op string, body any) (json.RawMessage, ControlRelayOutcome) {
	return r.forward(ctx, sessionID, op, body, "")
}

func (r *ControlRelay) forward(ctx context.Context, sessionID, op string, body any, declinedOwner string) (json.RawMessage, ControlRelayOutcome) {
	if r == nil || sessionID == "" {
		return nil, ControlRelayServeLocally
	}
	r.mu.Lock()
	publish := r.publish
	r.mu.Unlock()
	if publish == nil {
		return nil, ControlRelayServeLocally
	}

	lookupCtx, cancel := context.WithTimeout(ctx, controlRelayRedisTimeout)
	owner, err := r.owners.Owner(lookupCtx, sessionID)
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "playback control relay: owner lookup failed", "component", "playback", "playback_session_id", sessionID, "error", err)
		return nil, ControlRelayServeLocally
	}
	if owner == "" || owner == r.id || owner == declinedOwner {
		return nil, ControlRelayServeLocally
	}

	encodedBody, err := json.Marshal(body)
	if err != nil {
		return nil, ControlRelayServeLocally
	}
	request := controlRelayMessage{
		Kind:      controlRelayKindRequest,
		RequestID: uuid.NewString(),
		From:      r.id,
		To:        owner,
		SessionID: sessionID,
		Op:        op,
		Body:      encodedBody,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, ControlRelayServeLocally
	}

	replies := make(chan controlRelayMessage, 1)
	r.mu.Lock()
	r.pending[request.RequestID] = replies
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, request.RequestID)
		r.mu.Unlock()
	}()

	waitCtx, cancelWait := context.WithTimeout(ctx, r.replyTimeout)
	defer cancelWait()
	if err := publish(waitCtx, string(payload)); err != nil {
		slog.WarnContext(ctx, "playback control relay: publish failed", "component", "playback", "playback_session_id", sessionID, "op", op, "error", err)
		return nil, ControlRelayNoReply
	}
	select {
	case msg := <-replies:
		if !msg.Handled {
			if declinedOwner == "" {
				return r.forward(waitCtx, sessionID, op, body, owner)
			}
			return nil, ControlRelayServeLocally
		}
		return msg.Body, ControlRelayHandled
	case <-waitCtx.Done():
		slog.WarnContext(ctx, "playback control relay: owning replica did not answer", "component", "playback", "playback_session_id", sessionID, "op", op)
		return nil, ControlRelayNoReply
	}
}

// receive runs on the bus reader: it hands replies to their waiter and
// executes requests off the reader so a slow lane cannot stall the bus.
func (r *ControlRelay) receive(payload string) {
	var msg controlRelayMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil || msg.To != r.id || msg.RequestID == "" {
		return
	}
	switch msg.Kind {
	case controlRelayKindReply:
		r.mu.Lock()
		waiter := r.pending[msg.RequestID]
		r.mu.Unlock()
		if waiter != nil {
			select {
			case waiter <- msg:
			default:
			}
		}
	case controlRelayKindRequest:
		go r.serve(msg)
	}
}

func (r *ControlRelay) serve(request controlRelayMessage) {
	r.mu.Lock()
	handler := r.handler
	publish := r.publish
	r.mu.Unlock()
	if publish == nil {
		return
	}
	reply := controlRelayMessage{
		Kind:      controlRelayKindReply,
		RequestID: request.RequestID,
		From:      r.id,
		To:        request.From,
		SessionID: request.SessionID,
	}
	// A request may arrive after a newer lane took ownership while this
	// replica's old socket remains open. Recheck the claim before executing;
	// a declined request lets the caller look for the new owner.
	ownerCtx, cancelOwner := context.WithTimeout(context.Background(), controlRelayRedisTimeout)
	owner, ownerErr := r.owners.Owner(ownerCtx, request.SessionID)
	cancelOwner()
	if handler != nil && ownerErr == nil && owner == r.id && r.hub.HasLane(request.SessionID) {
		// The request runs to completion even when the caller stops waiting
		// for the reply: a terminate's history and stop receipt are written
		// after its transport cleanup, which can outlast the reply wait.
		handlerCtx, cancelHandler := context.WithTimeout(context.Background(), controlRelayHandlerTimeout)
		reply.Body = handler(handlerCtx, request.SessionID, request.Op, request.Body)
		cancelHandler()
		reply.Handled = true
	}
	payload, err := json.Marshal(reply)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.replyTimeout)
	defer cancel()
	if err := publish(ctx, string(payload)); err != nil {
		slog.WarnContext(ctx, "playback control relay: reply publish failed", "component", "playback", "playback_session_id", request.SessionID, "op", request.Op, "error", err)
	}
}

// observeLane runs under the hub's Register/Unregister; it only enqueues.
func (r *ControlRelay) observeLane(sessionID string, _ bool) {
	select {
	case r.events <- sessionID:
	default:
		// The refresh tick claims open lanes; a missed release expires.
	}
}

// run applies lane changes in order and refreshes claims until ctx ends.
func (r *ControlRelay) run(ctx context.Context) {
	ticker := time.NewTicker(r.refreshInterval)
	defer ticker.Stop()
	r.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			r.releaseAll()
			return
		case sessionID := <-r.events:
			r.applyLaneEvent(ctx, sessionID)
		case <-ticker.C:
			r.refresh(ctx)
		}
	}
}

// applyLaneEvent acts on the lane's current state rather than the
// notification, so notifications that raced each other cannot leave a claim
// for a closed lane or drop the claim of an open one, and a delayed claim
// carries the lane's real connection time.
func (r *ControlRelay) applyLaneEvent(ctx context.Context, sessionID string) {
	opCtx, cancel := context.WithTimeout(ctx, controlRelayRedisTimeout)
	defer cancel()
	var err error
	if connectedAt, open := r.hub.LaneConnectedAt(sessionID); open {
		err = r.owners.Claim(opCtx, sessionID, r.id, connectedAt)
	} else {
		err = r.owners.Release(opCtx, sessionID, r.id)
	}
	if err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "playback control relay: lane ownership update failed", "component", "playback", "playback_session_id", sessionID, "error", err)
	}
}

// refresh re-asserts the claim of every open lane. The claims go out in
// concurrent batches, and lane events that queued meanwhile are applied
// between batches to reduce renewal delays and avoid holding a new lane's first
// claim until the sweep ends. Renewal within the TTL still depends on load and
// Redis latency. A session's refresh claim never overlaps its own lane event.
func (r *ControlRelay) refresh(ctx context.Context) {
	sessionIDs := r.hub.LaneSessionIDs()
	for start := 0; start < len(sessionIDs); start += controlRelayRefreshBatch {
		if ctx.Err() != nil {
			return
		}
		r.applyQueuedLaneEvents(ctx)
		var wg sync.WaitGroup
		for _, sessionID := range sessionIDs[start:min(start+controlRelayRefreshBatch, len(sessionIDs))] {
			wg.Go(func() { r.reclaim(ctx, sessionID) })
		}
		wg.Wait()
	}
}

// applyQueuedLaneEvents applies up to one batch of the lane events already
// queued, concurrently and once per session. applyLaneEvent acts on the
// lane's current state, so collapsing a session's queued events loses
// nothing, and no two writes for one session overlap. The rest wait for the
// next gap, so a reconnect burst costs one round trip per batch rather than
// one per event, and cannot hold back the renewal of every other claim.
func (r *ControlRelay) applyQueuedLaneEvents(ctx context.Context) {
	sessionIDs := make(map[string]struct{}, controlRelayRefreshBatch)
	for range min(len(r.events), controlRelayRefreshBatch) {
		select {
		case sessionID := <-r.events:
			sessionIDs[sessionID] = struct{}{}
		default:
		}
	}
	var wg sync.WaitGroup
	for sessionID := range sessionIDs {
		wg.Go(func() { r.applyLaneEvent(ctx, sessionID) })
	}
	wg.Wait()
}

// reclaim re-asserts this replica's claim for a lane that is still open.
func (r *ControlRelay) reclaim(ctx context.Context, sessionID string) {
	connectedAt, open := r.hub.LaneConnectedAt(sessionID)
	if !open {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, controlRelayRedisTimeout)
	err := r.owners.Claim(opCtx, sessionID, r.id, connectedAt)
	cancel()
	if err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "playback control relay: lane claim refresh failed", "component", "playback", "playback_session_id", sessionID, "error", err)
	}
}

// releaseAll gives up this replica's claims on shutdown so requests stop
// waiting for it at once instead of after the TTL.
func (r *ControlRelay) releaseAll() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, sessionID := range r.hub.LaneSessionIDs() {
		if ctx.Err() != nil {
			return
		}
		_ = r.owners.Release(ctx, sessionID, r.id)
	}
}

// redisControlRelayOwners stores lane ownership as one expiring key per
// session whose value is "<connected-at unix microseconds> <owner>".
type redisControlRelayOwners struct {
	client *redis.Client
	ttl    time.Duration
}

var (
	// controlRelayClaimScript writes the caller's claim unless the key names
	// another replica whose connection is newer (equal times go to the greater
	// owner id). ARGV: owner, connected-at microseconds, TTL milliseconds.
	controlRelayClaimScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current then
  local sep = string.find(current, ' ', 1, true)
  if sep then
    local at = tonumber(string.sub(current, 1, sep - 1))
    local owner = string.sub(current, sep + 1)
    local mine = tonumber(ARGV[2])
    if owner ~= ARGV[1] and at and (at > mine or (at == mine and owner > ARGV[1])) then
      return 0
    end
  end
end
redis.call('SET', KEYS[1], ARGV[2] .. ' ' .. ARGV[1], 'PX', ARGV[3])
return 1`)
	// controlRelayReleaseScript deletes the key only if it names the caller.
	controlRelayReleaseScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current then
  local sep = string.find(current, ' ', 1, true)
  if sep and string.sub(current, sep + 1) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
  end
end
return 0`)
)

func controlRelayLaneKey(sessionID string) string {
	return controlRelayLaneKeyPrefix + sessionID
}

func (o *redisControlRelayOwners) Claim(ctx context.Context, sessionID, owner string, connectedAt time.Time) error {
	return controlRelayClaimScript.Run(ctx, o.client, []string{controlRelayLaneKey(sessionID)}, owner, connectedAt.UnixMicro(), o.ttl.Milliseconds()).Err()
}

func (o *redisControlRelayOwners) Release(ctx context.Context, sessionID, owner string) error {
	return controlRelayReleaseScript.Run(ctx, o.client, []string{controlRelayLaneKey(sessionID)}, owner).Err()
}

func (o *redisControlRelayOwners) Owner(ctx context.Context, sessionID string) (string, error) {
	value, err := o.client.Get(ctx, controlRelayLaneKey(sessionID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	at, owner, ok := strings.Cut(value, " ")
	if _, parseErr := strconv.ParseInt(at, 10, 64); !ok || parseErr != nil || owner == "" {
		return "", errors.New("playback control relay: malformed lane claim")
	}
	return owner, nil
}
