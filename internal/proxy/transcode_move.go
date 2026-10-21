package proxy

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// A transport whose token carries no recipe (a Jellyfin-compatible session)
// is rebuilt from the shared node recipe store, and the record there names the
// node that runs it. When this proxy cannot reach the node a token names, it
// asks the API server that owns the session to move the transport
// (noderecipe.MoveRequest), waits for the record to name another node, and
// relays there instead. The client keeps its URLs; the new node rebuilds the
// transcode from the record at the segment the client asks for next.
//
// Transports whose token carries the full recipe (native sessions) are not
// followed: a native client replans and gets a new token.

const (
	// transcodeMoveWait bounds how long one request waits for the API to move
	// a transport. A client retries a failed segment, so a request that gives up
	// costs one more retry, not the session.
	transcodeMoveWait = 8 * time.Second
	// transcodeMovePoll is how often the record is re-read while waiting.
	transcodeMovePoll = 200 * time.Millisecond
	// transcodeMoveRepublish re-sends the request while waiting, since pub/sub
	// delivery is at most once.
	transcodeMoveRepublish = 2 * time.Second
	// movedTranscodeRecheck is how often a store-backed transport's record is
	// re-read before a request, so a move, a later move, or a stop is noticed.
	movedTranscodeRecheck = 5 * time.Second
	// movedTranscodePruneEvery is how often expired entries are dropped.
	movedTranscodePruneEvery = time.Minute
	// maxTranscodeMoveHops bounds how many dead nodes one request moves past.
	maxTranscodeMoveHops = 3
)

// transcodeMovePublisher publishes cluster events. cache.EventBus implements it.
type transcodeMovePublisher interface {
	Publish(ctx context.Context, channel string, event cache.Event) error
}

// movedTranscode is where this proxy last found a store-backed transport
// running: the node its record names, or the token's node.
type movedTranscode struct {
	tokenNode string
	nodeURL   string
	checkedAt time.Time
}

type transcodeMoveFollower struct {
	recipes   recipeCardLookup
	publisher transcodeMovePublisher
	now       func() time.Time
	// wait, poll and republish are transcodeMoveWait, transcodeMovePoll and
	// transcodeMoveRepublish; tests shorten them.
	wait, poll, republish time.Duration

	mu         sync.Mutex
	moved      map[string]movedTranscode
	lastPruned time.Time
	group      singleflight.Group
}

// SetTranscodeMoveFollower wires the node recipe store and the event bus this
// proxy uses to follow a transcode that moved off a dead transcode node. Call
// it during construction. Either nil leaves relays failing as before.
func (s *Server) SetTranscodeMoveFollower(recipes recipeCardLookup, publisher transcodeMovePublisher) {
	if recipes == nil || publisher == nil {
		s.moves = nil
		return
	}
	s.moves = &transcodeMoveFollower{
		recipes:   recipes,
		publisher: publisher,
		now:       time.Now,
		wait:      transcodeMoveWait,
		poll:      transcodeMovePoll,
		republish: transcodeMoveRepublish,
		moved:     make(map[string]movedTranscode),
	}
}

// transcodeRecipeInStore reports whether a token's transport is rebuilt from
// the shared node recipe store rather than from the token itself, by the same
// rule a transcode node applies (see recipeIsComplete in internal/transcodenode).
func transcodeRecipeInStore(claims *streamtoken.Claims) bool {
	if claims == nil || claims.TranscodeNode == "" {
		return false
	}
	card := playback.RecipeCardFromClaims(claims)
	return card.SegmentDuration <= 0 || card.TargetCodecVideo == ""
}

// current returns the node to relay a transport to: the node its record
// names, or the token's node when the record names none. The record is read
// on the first request this proxy sees for the transport, so a transport
// moved before this proxy started is followed even when the token's node
// answers again, and re-read every movedTranscodeRecheck after that.
func (f *transcodeMoveFollower) current(ctx context.Context, transportID, tokenNode string) string {
	if f == nil {
		return tokenNode
	}
	now := f.now()
	f.mu.Lock()
	entry, ok := f.moved[transportID]
	f.mu.Unlock()
	if ok && sameNode(entry.tokenNode, tokenNode) && now.Sub(entry.checkedAt) < movedTranscodeRecheck {
		return entry.nodeURL
	}
	nodeURL := tokenNode
	if card, found := f.recipes.Get(ctx, transportID); found && card != nil && card.TranscodeNodeURL != "" {
		nodeURL = card.TranscodeNodeURL
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneLocked()
	f.moved[transportID] = movedTranscode{tokenNode: tokenNode, nodeURL: nodeURL, checkedAt: now}
	return nodeURL
}

// follow finds where a transport runs after deadNode stopped answering. It
// returns another node once the store names one, asking the API to move the
// transport while the record still names deadNode. ok is false when no record
// named another node within transcodeMoveWait.
func (f *transcodeMoveFollower) follow(ctx context.Context, transportID, tokenNode, deadNode string) (string, bool) {
	if f == nil {
		return "", false
	}
	key := transportID + "\x00" + nodepool.NormalizeNodeURL(deadNode)
	results := f.group.DoChan(key, func() (any, error) {
		waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), f.wait)
		defer cancel()
		return f.awaitMove(waitCtx, transportID, deadNode), nil
	})
	select {
	case result := <-results:
		nodeURL, _ := result.Val.(string)
		if nodeURL == "" {
			return "", false
		}
		f.mu.Lock()
		f.pruneLocked()
		f.moved[transportID] = movedTranscode{tokenNode: tokenNode, nodeURL: nodeURL, checkedAt: f.now()}
		f.mu.Unlock()
		return nodeURL, true
	case <-ctx.Done():
		return "", false
	}
}

func (f *transcodeMoveFollower) awaitMove(ctx context.Context, transportID, deadNode string) string {
	var requestedAt time.Time
	ticker := time.NewTicker(f.poll)
	defer ticker.Stop()
	for {
		// A missing record is not final: it can be briefly absent while a move
		// is committed (an old node binary deletes it on stop before central
		// writes it again), so keep polling until the deadline. A transport
		// that really stopped is denied before it reaches this relay, or costs
		// one request the wait.
		card, found := f.recipes.Get(ctx, transportID)
		if found && card != nil && card.TranscodeNodeURL != "" && !sameNode(card.TranscodeNodeURL, deadNode) {
			return card.TranscodeNodeURL
		}
		if now := f.now(); found && card != nil && (requestedAt.IsZero() || now.Sub(requestedAt) >= f.republish) {
			requestedAt = now
			f.requestMove(ctx, transportID, deadNode)
		}
		select {
		case <-ctx.Done():
			return ""
		case <-ticker.C:
		}
	}
}

func (f *transcodeMoveFollower) requestMove(ctx context.Context, transportID, deadNode string) {
	payload, err := noderecipe.EncodeMoveRequest(noderecipe.MoveRequest{TransportID: transportID, NodeURL: deadNode})
	if err != nil {
		return
	}
	if err := f.publisher.Publish(ctx, cache.ChannelTranscodeMove, cache.Event{Type: cache.EventTranscodeMoveRequested, Payload: payload}); err != nil {
		slog.WarnContext(ctx, "request a transcode move", "component", "proxy",
			"node", logredact.SanitizeURL(deadNode), "playback_session_id", transportID, "error", err)
	}
}

// pruneLocked drops transports not checked for longer than any token that
// could still name them. It walks the map at most every
// movedTranscodePruneEvery.
func (f *transcodeMoveFollower) pruneLocked() {
	now := f.now()
	if now.Sub(f.lastPruned) < movedTranscodePruneEvery {
		return
	}
	f.lastPruned = now
	cutoff := now.Add(-playback.MaxTokenTTL)
	for id, entry := range f.moved {
		if entry.checkedAt.Before(cutoff) {
			delete(f.moved, id)
		}
	}
}

func sameNode(a, b string) bool {
	return nodepool.NormalizeNodeURL(a) == nodepool.NormalizeNodeURL(b)
}
