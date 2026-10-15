package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// relayTestBus delivers every published payload to every subscriber on its
// own goroutine, like the Redis bus reader.
type relayTestBus struct {
	mu       sync.Mutex
	handlers []func(string)
}

func (b *relayTestBus) publish(_ context.Context, payload string) error {
	b.mu.Lock()
	handlers := append([]func(string){}, b.handlers...)
	b.mu.Unlock()
	for _, handler := range handlers {
		go handler(payload)
	}
	return nil
}

func (b *relayTestBus) subscribe(_ context.Context, handler func(string)) error {
	b.mu.Lock()
	b.handlers = append(b.handlers, handler)
	b.mu.Unlock()
	return nil
}

// relayTestOwners is lane ownership in memory: the newest connection wins.
type relayTestOwners struct {
	mu     sync.Mutex
	owners map[string]string
	at     map[string]time.Time
}

func (o *relayTestOwners) Claim(_ context.Context, sessionID, owner string, connectedAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if current, ok := o.owners[sessionID]; ok && current != owner && o.at[sessionID].After(connectedAt) {
		return nil
	}
	o.owners[sessionID] = owner
	o.at[sessionID] = connectedAt
	return nil
}

func (o *relayTestOwners) Release(_ context.Context, sessionID, owner string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owners[sessionID] == owner {
		delete(o.owners, sessionID)
	}
	return nil
}

func (o *relayTestOwners) Owner(_ context.Context, sessionID string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.owners[sessionID], nil
}

// relayTestConn is a lane connection safe to read while the owner writes.
type relayTestConn struct {
	mu       sync.Mutex
	messages []playback.CommandEnvelope
}

func (c *relayTestConn) WriteJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if env, ok := v.(playback.CommandEnvelope); ok {
		c.messages = append(c.messages, env)
	}
	return nil
}

func (c *relayTestConn) sent() []playback.CommandEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]playback.CommandEnvelope{}, c.messages...)
}

type relayTestCluster struct {
	owner, other           *AdminPlaybackControlHandler
	ownerMgr, otherMgr     *playback.SessionManager
	session                *playback.Session
	conn                   *relayTestConn
	ownerRelay, otherRelay *playback.ControlRelay
	owners                 *relayTestOwners
}

// newRelayTestCluster builds two API replicas sharing a bus and an ownership
// store. The session and its control lane live on owner; other has neither.
func newRelayTestCluster(t *testing.T) *relayTestCluster {
	t.Helper()
	bus := &relayTestBus{}
	owners := &relayTestOwners{owners: map[string]string{}, at: map[string]time.Time{}}
	c := &relayTestCluster{owners: owners}
	var ownerHub, otherHub *playback.RealtimeHub
	c.owner, c.ownerMgr, ownerHub, c.session = newAdminPlaybackControlTestHandler(t)
	c.other, c.otherMgr, otherHub, _ = newAdminPlaybackControlTestHandler(t)
	if _, err := c.otherMgr.GetSession(c.session.ID); err == nil {
		t.Fatal("the session must exist only on the owning replica")
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.ownerRelay = playback.NewControlRelayWithOwners(ownerHub, owners)
	c.otherRelay = playback.NewControlRelayWithOwners(otherHub, owners)
	c.owner.UseControlRelay(c.ownerRelay)
	c.other.UseControlRelay(c.otherRelay)
	for _, relay := range []*playback.ControlRelay{c.ownerRelay, c.otherRelay} {
		if err := relay.Start(ctx, bus.publish, bus.subscribe); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}

	c.conn = &relayTestConn{}
	registration := ownerHub.Register(c.session.ID, c.conn)
	if registration == nil {
		t.Fatal("register lane")
	}
	t.Cleanup(func() { ownerHub.Unregister(registration) })
	if !c.owner.playback.setRealtimeConnectionState(c.session.ID, true) {
		t.Fatal("mark the session connected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if owner, _ := owners.Owner(ctx, c.session.ID); owner != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the owning replica never claimed the lane")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return c
}

// TestAdminCommandRelaysToTheLaneOwner is the multi-replica case from #2283:
// the request lands on a replica that holds neither the session nor its
// lane, and the command still reaches the player through the owner.
func TestAdminCommandRelaysToTheLaneOwner(t *testing.T) {
	c := newRelayTestCluster(t)
	ctx := context.Background()

	view, err := c.other.Command(ctx, sequencedInput(c.session.ID, sequencedCommandA, 1, playback.CommandPause))
	if err != nil || view.Outcome != AdminPlaybackCommandApplied || view.Delivery != AdminPlaybackDeliveryDispatched || view.Sequence != 1 {
		t.Fatalf("relayed pause = %+v, %v", view, err)
	}
	sent := c.conn.sent()
	if len(sent) != 1 || sent[0].CommandID != sequencedCommandA || sent[0].Name != playback.CommandPause {
		t.Fatalf("lane received %+v", sent)
	}

	// The ledger on the owner orders commands from every replica: a replay
	// from the other replica is the recorded receipt, a stale command is
	// refused with the latest sequence, and the owner's own view agrees.
	replay, err := c.other.Command(ctx, sequencedInput(c.session.ID, sequencedCommandA, 1, playback.CommandPause))
	if err != nil || replay.Outcome != AdminPlaybackCommandReplayed {
		t.Fatalf("relayed replay = %+v, %v", replay, err)
	}
	if _, err := c.owner.Command(ctx, sequencedInput(c.session.ID, sequencedCommandB, 5, playback.CommandUnpause)); err != nil {
		t.Fatalf("local resume: %v", err)
	}
	var stale *AdminPlaybackCommandStaleError
	if _, err := c.other.Command(ctx, sequencedInput(c.session.ID, sequencedCommandC, 3, playback.CommandPause)); !errors.As(err, &stale) || stale.Latest != 5 {
		t.Fatalf("relayed stale = %v, want stale at 5", err)
	}
	conflicting := sequencedInput(c.session.ID, sequencedCommandA, 1, playback.CommandUnpause)
	if _, err := c.other.Command(ctx, conflicting); !errors.Is(err, ErrAdminPlaybackCommandConflict) {
		t.Fatalf("relayed conflict = %v", err)
	}
	if got := len(c.conn.sent()); got != 2 {
		t.Fatalf("lane received %d commands, want 2", got)
	}
	if _, _, ok := c.other.commandLedgerState(c.session.ID); ok {
		t.Fatal("the relaying replica kept a ledger for a session it does not hold")
	}
	if latest, _, ok := c.owner.commandLedgerState(c.session.ID); !ok || latest != 5 {
		t.Fatalf("owner ledger latest = %d (%v), want 5", latest, ok)
	}

	// A message travels the same way.
	message := sequencedInput(c.session.ID, "44444444-4444-4444-8444-444444444444", 6, playback.CommandDisplayMessage)
	message.Message = "hello"
	if view, err := c.other.Command(ctx, message); err != nil || view.Delivery != AdminPlaybackDeliveryDispatched {
		t.Fatalf("relayed message = %+v, %v", view, err)
	}
	if sent := c.conn.sent(); len(sent) != 3 || sent[2].Name != playback.CommandDisplayMessage {
		t.Fatalf("lane received %+v", sent)
	}
}

// TestAdminTerminateRelaysToTheLaneOwner: the owner ends its live session and
// tells the player, instead of the receiving replica revoking blind.
func TestAdminTerminateRelaysToTheLaneOwner(t *testing.T) {
	c := newRelayTestCluster(t)

	view, err := c.other.Terminate(t.Context(), AdminTerminateInput{SessionID: c.session.ID, ActorID: 2, Reason: "policy"})
	if err != nil {
		t.Fatal(err)
	}
	if !view.AuthorityRevoked || !view.ClientNotified || view.Delivery != AdminTerminateDeliveryDispatched {
		t.Fatalf("relayed terminate = %+v", view)
	}
	sent := c.conn.sent()
	if len(sent) != 1 || sent[0].Name != playback.CommandTerminate || sent[0].CommandID != view.CommandID {
		t.Fatalf("lane received %+v", sent)
	}
	if _, err := c.ownerMgr.GetSession(c.session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("the owner kept the terminated session: %v", err)
	}
}

// TestAdminCommandWithoutRelayOwnerServesLocally keeps the single-replica
// answer when no replica holds a lane for the session.
func TestAdminCommandWithoutRelayOwnerServesLocally(t *testing.T) {
	c := newRelayTestCluster(t)
	ctx := context.Background()

	if _, err := c.other.Command(ctx, sequencedInput("no-such-session", sequencedCommandA, 1, playback.CommandPause)); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("unknown session = %v, want not found", err)
	}
	if len(c.conn.sent()) != 0 {
		t.Fatal("an unknown session's command reached another lane")
	}
}

// TestAdminRelayWithoutAReplyIsNotRepeatedBlindly: an owner that does not
// answer may still have run the request. A command answers unavailable so a
// retry with the same identity replays the owner's receipt, rather than 404
// from a replica that never held the session; a terminate still revokes from
// here, and a session already gone answers unavailable rather than 404.
func TestAdminRelayWithoutAReplyIsNotRepeatedBlindly(t *testing.T) {
	c := newRelayTestCluster(t)
	if err := c.owners.Claim(context.Background(), "orphan", "dead-replica", time.Now()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.other.Command(ctx, sequencedInput("orphan", sequencedCommandA, 1, playback.CommandPause)); !errors.Is(err, ErrAdminPlaybackCommandUnavailable) {
		t.Fatalf("command without a reply = %v, want unavailable", err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.other.Terminate(ctx, AdminTerminateInput{SessionID: "orphan", ActorID: 2}); !errors.Is(err, ErrAdminTerminateUnavailable) {
		t.Fatalf("terminate without a reply on a session already gone = %v, want unavailable", err)
	}
}
