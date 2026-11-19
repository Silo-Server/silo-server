package nodesessions

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Overlapping requests for one playback session (a HEAD probe or a second
// range request while a GET plays) each hold the session. Only the last
// release may end it, in memory and in Redis.
func TestHoldKeepsSessionUntilLastOverlappingRequestEnds(t *testing.T) {
	redisStub := startRedisStub(t)
	tracker := NewTracker(redisStub.client(t), "http://proxy-a", "proxy-a", "proxy")
	ctx := context.Background()
	info := SessionInfo{SessionID: "play-1", Type: "remux"}

	releasePlaying := tracker.Hold(ctx, info)
	const overlapping = 8
	releases := make(chan func(context.Context), overlapping)
	var wg sync.WaitGroup
	for range overlapping {
		wg.Go(func() { releases <- tracker.Hold(ctx, info) })
	}
	wg.Wait()
	close(releases)
	for release := range releases {
		wg.Go(func() {
			release(ctx)
			release(ctx) // a repeated release must not end another request's hold
		})
	}
	wg.Wait()

	if got := tracker.ActiveCount(); got != 1 {
		t.Fatalf("ActiveCount after overlapping requests ended = %d, want 1", got)
	}
	if !redisStub.exists(tracker.redisKey(info.SessionID)) {
		t.Fatal("session record deleted from Redis while a request still holds it")
	}

	releasePlaying(ctx)
	if got := tracker.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount after last request ended = %d, want 0", got)
	}
	if redisStub.exists(tracker.redisKey(info.SessionID)) {
		t.Fatal("session record kept in Redis after its last request ended")
	}
}

// A direct-play stream arrives as a series of range requests with gaps
// between them. The last release of a lingering hold must keep the session
// counted and its record in Redis until sessionTTL passes with no new request.
func TestHoldLingeringKeepsSessionThroughGapBetweenRequests(t *testing.T) {
	redisStub := startRedisStub(t)
	tracker := NewTracker(redisStub.client(t), "http://proxy-a", "proxy-a", "proxy")
	ctx := context.Background()
	info := SessionInfo{SessionID: "play-1", Type: "direct_play"}
	key := tracker.redisKey(info.SessionID)

	for range 2 {
		release := tracker.HoldLingering(ctx, info)
		release(ctx, true)
		if got := tracker.ActiveCount(); got != 1 {
			t.Fatalf("ActiveCount in the gap after a request = %d, want 1", got)
		}
		tracker.refreshAll(ctx)
		if !redisStub.exists(key) {
			t.Fatal("session record deleted from Redis in the gap after a request")
		}
		// The record ends with the count: refresh must not give it a new TTL.
		if got := redisStub.expires(key); got != 0 {
			t.Fatalf("refresh extended a lingering record %d time(s), want none", got)
		}
	}

	// A replan to HLS on the same proxy keeps the session ID. Its first Touch
	// must write its own record over the lingering one, as it does after a
	// plain Hold release.
	tracker.Touch(ctx, SessionInfo{SessionID: info.SessionID, Type: "transcode"})
	if got := redisStub.recordType(t, key); got != "transcode" {
		t.Fatalf("record type after Touch following a lingering release = %q, want %q", got, "transcode")
	}

	tracker.mu.Lock()
	tracker.touched[info.SessionID] = time.Now().Add(-sessionTTL - time.Second)
	tracker.mu.Unlock()
	tracker.refreshAll(ctx)
	if got := tracker.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount after sessionTTL with no request = %d, want 0", got)
	}
}

// Only a request that delivered media lingers. A HEAD probe, a refusal or an
// unsatisfiable range must not keep a node's capacity for sessionTTL, and one
// that arrives while an earlier request's linger runs must leave it as it was.
func TestHoldLingeringCountsOnlyDeliveredRequests(t *testing.T) {
	redisStub := startRedisStub(t)
	tracker := NewTracker(redisStub.client(t), "http://proxy-a", "proxy-a", "proxy")
	ctx := context.Background()
	info := SessionInfo{SessionID: "play-1", Type: "direct_play"}
	key := tracker.redisKey(info.SessionID)

	tracker.HoldLingering(ctx, info)(ctx, false)
	if got := tracker.ActiveCount(); got != 0 || redisStub.exists(key) {
		t.Fatalf("after a request that delivered nothing: ActiveCount = %d, record exists = %t, want 0 and none", got, redisStub.exists(key))
	}

	tracker.HoldLingering(ctx, info)(ctx, true)
	tracker.mu.Lock()
	tracker.touched[info.SessionID] = time.Now().Add(-sessionTTL / 2)
	tracker.mu.Unlock()
	tracker.HoldLingering(ctx, info)(ctx, false)
	if got := tracker.ActiveCount(); got != 1 || !redisStub.exists(key) {
		t.Fatalf("after a request that delivered nothing during a linger: ActiveCount = %d, record exists = %t, want 1 and a record", got, redisStub.exists(key))
	}
	// Its hold gave the record a full TTL; the record goes back to the time the linger has left.
	if got := redisStub.ttl(key); got <= 0 || got > sessionTTL/2 {
		t.Fatalf("record TTL after it = %s, want the linger's remaining time, at most %s", got, sessionTTL/2)
	}

	// A range that delivered media can end while a probe for the same session
	// is still open. The probe ends last and must not take the linger with it.
	tracker.Remove(ctx, info.SessionID)
	get, probe := tracker.HoldLingering(ctx, info), tracker.HoldLingering(ctx, info)
	get(ctx, true)
	probe(ctx, false)
	if got := tracker.ActiveCount(); got != 1 || !redisStub.exists(key) {
		t.Fatalf("after a probe outlived a range that delivered media: ActiveCount = %d, record exists = %t, want 1 and a record", got, redisStub.exists(key))
	}

	// Remove ends a session outright. The lingering release writes its record
	// outside the lock, so a Remove that lands first must not be undone by it.
	tracker.Remove(ctx, info.SessionID)
	last := tracker.HoldLingering(ctx, info)
	redisStub.beforeNextSet(func() { tracker.Remove(ctx, info.SessionID) })
	last(ctx, true)
	if got := tracker.ActiveCount(); got != 0 || redisStub.exists(key) {
		t.Fatalf("after Remove raced a lingering release: ActiveCount = %d, record exists = %t, want 0 and none", got, redisStub.exists(key))
	}
	tracker.HoldLingering(ctx, info)(ctx, true)

	// A replan to remux on the same node keeps the session ID. When the remux
	// ends, the direct play's linger must not come back.
	tracker.Hold(ctx, SessionInfo{SessionID: info.SessionID, Type: "remux"})(ctx)
	if got := tracker.ActiveCount(); got != 0 || redisStub.exists(key) {
		t.Fatalf("after a remux that followed a lingering direct play ended: ActiveCount = %d, record exists = %t, want 0 and none", got, redisStub.exists(key))
	}

	// The remux can also start while a direct-play range is still open, and
	// end before it. That range's release is then the last, and must not make
	// the finished remux linger.
	straggler := tracker.HoldLingering(ctx, info)
	tracker.Hold(ctx, SessionInfo{SessionID: info.SessionID, Type: "remux"})(ctx)
	straggler(ctx, true)
	if got := tracker.ActiveCount(); got != 0 || redisStub.exists(key) {
		t.Fatalf("after a direct-play range outlived a remux on the same session: ActiveCount = %d, record exists = %t, want 0 and none", got, redisStub.exists(key))
	}

	// The other way round: a replan from remux to direct play, whose first
	// range starts while the remux request is still open. The session is a
	// direct play from then on and lingers after that range.
	remux := tracker.Hold(ctx, SessionInfo{SessionID: info.SessionID, Type: "remux"})
	first := tracker.HoldLingering(ctx, info)
	remux(ctx)
	first(ctx, true)
	if got := tracker.ActiveCount(); got != 1 || !redisStub.exists(key) {
		t.Fatalf("after a direct play's first range that started during a remux: ActiveCount = %d, record exists = %t, want 1 and a record", got, redisStub.exists(key))
	}
}

// An authoritative Remove ends the session; a hold taken before it must not
// end a hold taken after the session was registered again.
func TestHoldReleasedAfterRemoveLeavesNewHoldAlone(t *testing.T) {
	redisStub := startRedisStub(t)
	tracker := NewTracker(redisStub.client(t), "http://proxy-a", "proxy-a", "proxy")
	ctx := context.Background()
	info := SessionInfo{SessionID: "play-1", Type: "remux"}

	releaseStale := tracker.Hold(ctx, info)
	tracker.Remove(ctx, info.SessionID)
	releaseCurrent := tracker.Hold(ctx, info)
	releaseStale(ctx)

	if got := tracker.ActiveCount(); got != 1 {
		t.Fatalf("ActiveCount after stale release = %d, want 1", got)
	}
	releaseCurrent(ctx)
	if got := tracker.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount after current release = %d, want 0", got)
	}
}

// A held session's Redis record can go missing while it plays: the DEL from
// an earlier request's last release can land after the next request's SET, or
// that SET can fail. The refresh loop must write the record back while a hold
// remains, and must not bring back a session whose last hold has ended.
func TestRefreshRestoresHeldSessionRecord(t *testing.T) {
	redisStub := startRedisStub(t)
	tracker := NewTracker(redisStub.client(t), "http://proxy-a", "proxy-a", "proxy")
	ctx := context.Background()
	info := SessionInfo{SessionID: "play-1", Type: "direct_play"}
	key := tracker.redisKey(info.SessionID)

	release := tracker.Hold(ctx, info)
	redisStub.drop(key)
	tracker.refreshAll(ctx)
	if !redisStub.exists(key) {
		t.Fatal("refresh left a held session without its Redis record")
	}

	release(ctx)
	tracker.refreshAll(ctx)
	if redisStub.exists(key) {
		t.Fatal("refresh brought back a session whose last hold had ended")
	}
}

// The last release can end a session after refresh has read its record but
// before refresh's write reaches Redis. That write must not leave the ended
// session in Redis for another TTL, or replace the record of a request that
// registered the session again. A lingering last release is the exception: it
// keeps the session counted and its record.
func TestRefreshDoesNotRestoreSessionReleasedMidRefresh(t *testing.T) {
	for _, tc := range []struct {
		name string
		// lingerRelease takes the hold with HoldLingering, as a direct-play
		// request does.
		lingerRelease bool
		// touchAfterRelease re-registers the session through Touch, as an
		// HLS replan on the same proxy does, before the refresh write lands.
		touchAfterRelease bool
		// holdAfterRelease starts a new request for the session, as a
		// remux replan on the same proxy does, before the refresh write lands.
		holdAfterRelease bool
		// wantType is the type of the record left in Redis, or "" for none.
		wantType string
		// wantCount is ActiveCount once the refresh finishes.
		wantCount int
	}{
		{name: "released"},
		{name: "released then touched", touchAfterRelease: true, wantType: "hls", wantCount: 1},
		{name: "released then held again", holdAfterRelease: true, wantType: "remux", wantCount: 1},
		{name: "released lingering", lingerRelease: true, wantType: "direct_play", wantCount: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			redisStub := startRedisStub(t)
			tracker := NewTracker(redisStub.client(t), "http://proxy-a", "proxy-a", "proxy")
			ctx := context.Background()
			info := SessionInfo{SessionID: "play-1", Type: "direct_play"}
			key := tracker.redisKey(info.SessionID)

			var release func(context.Context)
			if tc.lingerRelease {
				lingering := tracker.HoldLingering(ctx, info)
				release = func(ctx context.Context) { lingering(ctx, true) }
			} else {
				release = tracker.Hold(ctx, info)
			}
			redisStub.beforeNextSet(func() {
				release(ctx)
				if tc.touchAfterRelease {
					tracker.Touch(ctx, SessionInfo{SessionID: info.SessionID, Type: "hls"})
				}
				if tc.holdAfterRelease {
					tracker.Hold(ctx, SessionInfo{SessionID: info.SessionID, Type: "remux"})
				}
			})
			tracker.refreshAll(ctx)
			if tc.touchAfterRelease {
				// The next HLS segment fetch.
				tracker.Touch(ctx, SessionInfo{SessionID: info.SessionID, Type: "hls"})
			}

			if got := redisStub.recordType(t, key); got != tc.wantType {
				t.Fatalf("record type after refresh = %q, want %q", got, tc.wantType)
			}
			if got := tracker.ActiveCount(); got != tc.wantCount {
				t.Fatalf("ActiveCount after refresh = %d, want %d", got, tc.wantCount)
			}
		})
	}
}

// redisStub answers every command with a success reply and tracks each key's
// value, applying SET and DEL when it replies. That is enough Redis for the
// tracker, which ignores replies beyond logging errors.
type redisStub struct {
	addr string
	mu   sync.Mutex
	keys map[string]string
	// ttls holds the expiry each SET gave its key; expired counts EXPIRE commands by key.
	ttls    map[string]time.Duration
	expired map[string]int
	// onSet, when set, runs once before the next SET is applied.
	onSet func()
}

// beforeNextSet runs fn once, just before the stub applies the next SET.
func (s *redisStub) beforeNextSet(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSet = fn
}

func startRedisStub(t *testing.T) *redisStub {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stub := &redisStub{addr: ln.Addr().String(), keys: make(map[string]string), ttls: make(map[string]time.Duration), expired: make(map[string]int)}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() { stub.serve(conn) })
		}
	})
	return stub
}

func (s *redisStub) client(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: s.addr, DisableIdentity: true})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func (s *redisStub) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	for {
		args, err := readRESPCommand(r)
		if err != nil {
			return
		}
		reply := "+OK\r\n"
		switch strings.ToUpper(args[0]) {
		case "HELLO":
			// Refusing HELLO makes the client fall back to RESP2.
			reply = "-ERR unknown command 'HELLO'\r\n"
		case "SET":
			s.mu.Lock()
			hook := s.onSet
			s.onSet = nil
			s.mu.Unlock()
			if hook != nil {
				hook()
			}
			s.mu.Lock()
			s.keys[args[1]] = args[2]
			if len(args) >= 5 {
				if n, err := strconv.Atoi(args[4]); err == nil {
					unit := time.Second
					if strings.EqualFold(args[3], "px") {
						unit = time.Millisecond
					}
					s.ttls[args[1]] = time.Duration(n) * unit
				}
			}
			s.mu.Unlock()
		case "EXPIRE":
			s.mu.Lock()
			s.expired[args[1]]++
			s.mu.Unlock()
			reply = ":1\r\n"
		case "DEL":
			s.mu.Lock()
			for _, key := range args[1:] {
				delete(s.keys, key)
			}
			s.mu.Unlock()
			reply = ":" + strconv.Itoa(len(args)-1) + "\r\n"
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

// drop deletes a key behind the tracker's back.
func (s *redisStub) drop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
}

// ttl returns the expiry the latest SET gave key.
func (s *redisStub) ttl(key string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ttls[key]
}

// expires returns how many EXPIRE commands named key.
func (s *redisStub) expires(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expired[key]
}

func (s *redisStub) exists(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.keys[key]
	return ok
}

// recordType returns the Type of the session record stored at key, or "" when
// there is none.
func (s *redisStub) recordType(t *testing.T, key string) string {
	t.Helper()
	s.mu.Lock()
	data, ok := s.keys[key]
	s.mu.Unlock()
	if !ok {
		return ""
	}
	var info SessionInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		t.Fatalf("decode record %s: %v", key, err)
	}
	return info.Type
}

func readRESPCommand(r *bufio.Reader) ([]string, error) {
	n, err := readRESPLength(r, '*')
	if err != nil {
		return nil, err
	}
	args := make([]string, n)
	for i := range args {
		size, err := readRESPLength(r, '$')
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args[i] = string(buf[:size])
	}
	return args, nil
}

func readRESPLength(r *bufio.Reader, prefix byte) (int, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return 0, err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) == 0 || line[0] != prefix {
		return 0, io.ErrUnexpectedEOF
	}
	return strconv.Atoi(line[1:])
}
