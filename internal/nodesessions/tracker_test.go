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
// registered the session again.
func TestRefreshDoesNotRestoreSessionReleasedMidRefresh(t *testing.T) {
	for _, tc := range []struct {
		name string
		// touchAfterRelease re-registers the session through Touch, as an
		// HLS replan on the same proxy does, before the refresh write lands.
		touchAfterRelease bool
		// holdAfterRelease starts a new request for the session, as a
		// remux replan on the same proxy does, before the refresh write lands.
		holdAfterRelease bool
		// wantType is the type of the record left in Redis, or "" for none.
		wantType string
	}{
		{name: "released"},
		{name: "released then touched", touchAfterRelease: true, wantType: "hls"},
		{name: "released then held again", holdAfterRelease: true, wantType: "remux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			redisStub := startRedisStub(t)
			tracker := NewTracker(redisStub.client(t), "http://proxy-a", "proxy-a", "proxy")
			ctx := context.Background()
			info := SessionInfo{SessionID: "play-1", Type: "direct_play"}
			key := tracker.redisKey(info.SessionID)

			release := tracker.Hold(ctx, info)
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
	stub := &redisStub{addr: ln.Addr().String(), keys: make(map[string]string)}
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
			s.mu.Unlock()
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
