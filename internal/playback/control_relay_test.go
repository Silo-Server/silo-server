package playback

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// relayTestBus is an in-process stand-in for the event bus: every published
// payload reaches every subscriber on its own goroutine, like a Redis reader.
type relayTestBus struct {
	mu        sync.Mutex
	handlers  []func(string)
	published atomic.Int64
	down      atomic.Bool
}

func (b *relayTestBus) publish(_ context.Context, payload string) error {
	if b.down.Load() {
		return errors.New("bus down")
	}
	b.published.Add(1)
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

// relayTestOwners is the ownership store in memory, with the Redis scripts'
// semantics.
type relayTestOwners struct {
	mu     sync.Mutex
	owners map[string]relayTestClaim
}

type relayTestClaim struct {
	owner       string
	connectedAt time.Time
}

func newRelayTestOwners() *relayTestOwners {
	return &relayTestOwners{owners: map[string]relayTestClaim{}}
}

func (o *relayTestOwners) Claim(_ context.Context, sessionID, owner string, connectedAt time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if current, ok := o.owners[sessionID]; ok && current.owner != owner &&
		(current.connectedAt.After(connectedAt) || (current.connectedAt.Equal(connectedAt) && current.owner > owner)) {
		return nil
	}
	o.owners[sessionID] = relayTestClaim{owner: owner, connectedAt: connectedAt}
	return nil
}

func (o *relayTestOwners) Release(_ context.Context, sessionID, owner string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owners[sessionID].owner == owner {
		delete(o.owners, sessionID)
	}
	return nil
}

func (o *relayTestOwners) Owner(_ context.Context, sessionID string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.owners[sessionID].owner, nil
}

func (o *relayTestOwners) get(sessionID string) string {
	owner, _ := o.Owner(context.Background(), sessionID)
	return owner
}

type relayTestReplica struct {
	hub   *RealtimeHub
	relay *ControlRelay
	calls atomic.Int64
}

func startRelayTestReplica(t *testing.T, bus *relayTestBus, owners ControlRelayOwners) *relayTestReplica {
	t.Helper()
	replica := &relayTestReplica{hub: NewRealtimeHub()}
	replica.relay = NewControlRelayWithOwners(replica.hub, owners)
	replica.relay.replyTimeout = 2 * time.Second
	replica.relay.Handle(func(_ context.Context, sessionID, op string, body json.RawMessage) json.RawMessage {
		replica.calls.Add(1)
		reply, _ := json.Marshal(map[string]string{"session": sessionID, "op": op, "echo": string(body), "by": replica.relay.id})
		return reply
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := replica.relay.Start(ctx, bus.publish, bus.subscribe); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return replica
}

func waitForRelayOwner(t *testing.T, owners *relayTestOwners, sessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for owners.get(sessionID) != want {
		if time.Now().After(deadline) {
			t.Fatalf("owner of %s = %q, want %q", sessionID, owners.get(sessionID), want)
		}
		//nolint:forbidigo // Polls observable state: the relay claims lanes on its own goroutine.
		time.Sleep(5 * time.Millisecond)
	}
}

func TestControlRelayForwardsToTheLaneOwner(t *testing.T) {
	bus, owners := &relayTestBus{}, newRelayTestOwners()
	a := startRelayTestReplica(t, bus, owners)
	b := startRelayTestReplica(t, bus, owners)

	reg := a.hub.Register("s1", registrationTestConn{})
	t.Cleanup(func() { a.hub.Unregister(reg) })
	waitForRelayOwner(t, owners, "s1", a.relay.id)

	reply, outcome := b.relay.Forward(context.Background(), "s1", "pause", map[string]int{"sequence": 7})
	if outcome != ControlRelayHandled {
		t.Fatalf("Forward from the replica without the lane = %v, want handled", outcome)
	}
	var decoded map[string]string
	if err := json.Unmarshal(reply, &decoded); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if decoded["by"] != a.relay.id || decoded["op"] != "pause" || decoded["session"] != "s1" || decoded["echo"] != `{"sequence":7}` {
		t.Fatalf("reply = %v", decoded)
	}
	if a.calls.Load() != 1 || b.calls.Load() != 0 {
		t.Fatalf("handler calls: owner %d, caller %d", a.calls.Load(), b.calls.Load())
	}

	// The owner itself serves locally without touching the bus.
	before := bus.published.Load()
	if _, outcome := a.relay.Forward(context.Background(), "s1", "pause", nil); outcome != ControlRelayServeLocally {
		t.Fatalf("the lane owner's own request = %v, want served locally", outcome)
	}
	if bus.published.Load() != before {
		t.Fatal("the lane owner published a request")
	}
}

func TestControlRelayServesLocallyWithoutAReachableOwner(t *testing.T) {
	bus, owners := &relayTestBus{}, newRelayTestOwners()
	a := startRelayTestReplica(t, bus, owners)
	b := startRelayTestReplica(t, bus, owners)

	// No replica claims the session.
	if _, outcome := b.relay.Forward(context.Background(), "unknown", "pause", nil); outcome != ControlRelayServeLocally {
		t.Fatalf("a session nobody claims = %v, want served locally", outcome)
	}

	// A claim that outlived its lane is refused by its holder at once.
	_ = owners.Claim(context.Background(), "stale", a.relay.id, time.Now())
	started := time.Now()
	if _, outcome := b.relay.Forward(context.Background(), "stale", "pause", nil); outcome != ControlRelayServeLocally {
		t.Fatalf("a claim without a lane = %v, want served locally", outcome)
	}
	if elapsed := time.Since(started); elapsed >= b.relay.replyTimeout {
		t.Fatalf("stale claim waited %s, the full reply timeout", elapsed)
	}
	if a.calls.Load() != 0 {
		t.Fatal("a replica without the lane executed the request")
	}

	// A failed publish may still have reached the owner, so it is not served
	// here as if the request never left this replica.
	reg := a.hub.Register("live", registrationTestConn{})
	t.Cleanup(func() { a.hub.Unregister(reg) })
	waitForRelayOwner(t, owners, "live", a.relay.id)
	bus.down.Store(true)
	if _, outcome := b.relay.Forward(context.Background(), "live", "pause", nil); outcome != ControlRelayNoReply {
		t.Fatalf("a failed publish = %v, want no reply", outcome)
	}
}

// TestControlRelayReportsNoReplyOnceRequestWasPublished: an owner that does
// not answer may still run the request, so the caller is not told to serve
// it locally.
func TestControlRelayReportsNoReplyOnceRequestWasPublished(t *testing.T) {
	bus, owners := &relayTestBus{}, newRelayTestOwners()
	b := startRelayTestReplica(t, bus, owners)
	b.relay.replyTimeout = 50 * time.Millisecond
	_ = owners.Claim(context.Background(), "orphan", "dead-replica", time.Now())
	if _, outcome := b.relay.Forward(context.Background(), "orphan", "pause", nil); outcome != ControlRelayNoReply {
		t.Fatalf("an owner that never answered = %v, want no reply", outcome)
	}
}

func TestControlRelayLaneOwnershipFollowsTheNewestConnection(t *testing.T) {
	bus, owners := &relayTestBus{}, newRelayTestOwners()
	a := startRelayTestReplica(t, bus, owners)
	b := startRelayTestReplica(t, bus, owners)

	regA := a.hub.Register("s1", registrationTestConn{})
	waitForRelayOwner(t, owners, "s1", a.relay.id)

	// The player reconnects to B before A notices its old socket died.
	regB := b.hub.Register("s1", registrationTestConn{})
	waitForRelayOwner(t, owners, "s1", b.relay.id)

	// A's refresh, or its open notification arriving late, must not take
	// the session back, and A closing its stale lane must not delete B's
	// claim.
	a.relay.refresh(context.Background())
	if got := owners.get("s1"); got != b.relay.id {
		t.Fatalf("refresh stole the claim: owner %q", got)
	}
	a.relay.applyLaneEvent(context.Background(), "s1")
	if got := owners.get("s1"); got != b.relay.id {
		t.Fatalf("a delayed open notification stole the claim: owner %q", got)
	}

	// While A still holds its old lane, a request on A goes to B, which holds
	// the player's current connection.
	if _, outcome := a.relay.Forward(context.Background(), "s1", "pause", nil); outcome != ControlRelayHandled || b.calls.Load() != 1 || a.calls.Load() != 0 {
		t.Fatalf("request on the stale lane's replica = %v (owner calls %d, stale calls %d), want relayed to the newest lane", outcome, b.calls.Load(), a.calls.Load())
	}
	a.hub.Unregister(regA)
	a.relay.applyLaneEvent(context.Background(), "s1")
	if got := owners.get("s1"); got != b.relay.id {
		t.Fatalf("stale close released the new claim: owner %q", got)
	}

	// B's lane closing releases the claim.
	b.hub.Unregister(regB)
	waitForRelayOwner(t, owners, "s1", "")
}

// TestControlRelayRefreshRepairsADroppedTakeover: when the newer replica's
// open notification was dropped, its refresh still takes the key from the
// replica holding the older connection, whichever refreshes first.
func TestControlRelayRefreshRepairsADroppedTakeover(t *testing.T) {
	owners := newRelayTestOwners()
	hubA, hubB := NewRealtimeHub(), NewRealtimeHub()
	a, b := NewControlRelayWithOwners(hubA, owners), NewControlRelayWithOwners(hubB, owners)
	hubA.Register("s1", registrationTestConn{})
	a.refresh(context.Background())
	olderAt, _ := hubA.LaneConnectedAt("s1")
	for {
		hubB.Register("s1", registrationTestConn{})
		if newerAt, _ := hubB.LaneConnectedAt("s1"); newerAt.After(olderAt) {
			break
		}
	}
	b.refresh(context.Background())
	a.refresh(context.Background())
	if got := owners.get("s1"); got != b.id {
		t.Fatalf("owner after refreshes = %q, want the newest connection's replica", got)
	}
}

func TestControlRelayRefreshReclaimsAnExpiredClaim(t *testing.T) {
	owners := newRelayTestOwners()
	hub := NewRealtimeHub()
	relay := NewControlRelayWithOwners(hub, owners)
	hub.Register("s1", registrationTestConn{})
	relay.refresh(context.Background())
	if got := owners.get("s1"); got != relay.id {
		t.Fatalf("refresh did not claim an open lane: owner %q", got)
	}
	_ = owners.Release(context.Background(), "s1", relay.id) // the key expired
	relay.refresh(context.Background())
	if got := owners.get("s1"); got != relay.id {
		t.Fatalf("refresh did not reclaim: owner %q", got)
	}
}

func TestControlRelayNilNeverForwards(t *testing.T) {
	var relay *ControlRelay
	if _, outcome := relay.Forward(context.Background(), "s1", "pause", nil); outcome != ControlRelayServeLocally {
		t.Fatalf("nil relay = %v, want served locally", outcome)
	}
	if NewControlRelay(NewRealtimeHub(), nil) != nil {
		t.Fatal("relay without Redis")
	}
}

// TestRedisControlRelayOwners checks the ownership scripts against Redis.
func TestRedisControlRelayOwners(t *testing.T) {
	endpoint := os.Getenv("SILO_TEST_REDIS_URL")
	if endpoint == "" {
		t.Skip("SILO_TEST_REDIS_URL required for the Redis lane ownership test")
	}
	opts, err := redis.ParseURL(endpoint)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	owners := &redisControlRelayOwners{client: client, ttl: time.Minute}
	sessionID := "control-relay-test-" + time.Now().Format("150405.000000000")
	t.Cleanup(func() { client.Del(ctx, controlRelayLaneKey(sessionID)) })

	owner := func() string {
		value, err := owners.Owner(ctx, sessionID)
		if err != nil {
			t.Fatalf("Owner: %v", err)
		}
		return value
	}
	if owner() != "" {
		t.Fatal("fresh session has an owner")
	}
	older := time.Now()
	newer := older.Add(time.Second)
	if err := owners.Claim(ctx, sessionID, "a", older); err != nil || owner() != "a" {
		t.Fatalf("claim of a free key: %v, owner %q", err, owner())
	}
	if err := owners.Claim(ctx, sessionID, "a", older); err != nil || owner() != "a" {
		t.Fatalf("refresh of the caller's claim: %v, owner %q", err, owner())
	}
	if err := owners.Claim(ctx, sessionID, "b", newer); err != nil || owner() != "b" {
		t.Fatalf("claim for a newer connection: %v, owner %q", err, owner())
	}
	if err := owners.Claim(ctx, sessionID, "a", older); err != nil || owner() != "b" {
		t.Fatalf("an older connection took the claim: %v, owner %q", err, owner())
	}
	if err := owners.Claim(ctx, sessionID, "a", newer); err != nil || owner() != "b" {
		t.Fatalf("a tie went to the lesser owner id: %v, owner %q", err, owner())
	}
	if err := owners.Release(ctx, sessionID, "a"); err != nil || owner() != "b" {
		t.Fatalf("release of another replica's claim: %v, owner %q", err, owner())
	}
	if ttl := client.PTTL(ctx, controlRelayLaneKey(sessionID)).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("claim TTL = %s", ttl)
	}
	if err := owners.Release(ctx, sessionID, "b"); err != nil || owner() != "" {
		t.Fatalf("release: %v, owner %q", err, owner())
	}
}
