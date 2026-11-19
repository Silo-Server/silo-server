package handlers

import (
	"context"
	"errors"
	"maps"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// acceptRedisSaves makes every Redis connection a save checks succeed, for
// tests about what a save stores rather than whether Redis answers.
func acceptRedisSaves(t *testing.T) {
	t.Helper()
	previous := probeRedis
	probeRedis = func(context.Context, config.RedisConfig) error { return nil }
	t.Cleanup(func() { probeRedis = previous })
}

// closedRedisURL returns a redis:// URL for a local port nothing listens on.
func closedRedisURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "redis://" + address
}

// racingSettingsStore stores concurrent once a save has read the settings to
// check its Redis connection, the way a second admin's save would land
// between that check and the save's transaction.
type racingSettingsStore struct {
	*fakeServerSettingsStore
	concurrent map[string]string
}

func (s *racingSettingsStore) GetAll(ctx context.Context) (map[string]string, error) {
	values, err := s.fakeServerSettingsStore.GetAll(ctx)
	maps.Copy(s.values, s.concurrent)
	s.concurrent = nil
	return values, err
}

// A URL can turn the socket timeout off, as a number or as a duration. The
// check still gives up when its time is up, so a Redis that accepts the
// connection and never answers cannot hold a save, or its connection, open.
func TestRedisCheckGivesUpOnARedisThatNeverAnswers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()

	for _, timeouts := range []string{"read_timeout=-1&write_timeout=-1", "read_timeout=-1s&write_timeout=-1s"} {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		done := make(chan error, 1)
		go func() {
			done <- probeRedis(ctx, config.RedisConfig{URL: "redis://" + listener.Addr().String() + "?" + timeouts})
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: the check succeeded against a Redis that never answers", timeouts)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the check was still waiting 5 seconds after its context ended", timeouts)
		}
		cancel()
	}
}

func TestAdminSettingsRefuseRedisSaveWhenTheConnectionMovedDuringTheCheck(t *testing.T) {
	redis16, _ := fakeRedisServer(t, fakeRedis{databases: 16})
	for _, single := range []bool{false, true} {
		moved := closedRedisURL(t)
		settings := &racingSettingsStore{
			fakeServerSettingsStore: &fakeServerSettingsStore{values: map[string]string{"redis.url": redis16 + "/3"}},
			concurrent:              map[string]string{"redis.url": moved},
		}
		err := saveAdminSetting(t, &AdminHandler{SettingsRepo: settings}, config.RedisDBSettingKey, "5", single)
		if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != 400 || !strings.Contains(apiErr.Message, "changed while Silo was checking") {
			t.Errorf("single=%v: %v; want the save refused", single, err)
		}
		if got, stored := settings.values[config.RedisDBSettingKey]; stored {
			t.Errorf("single=%v: stored redis.db = %q next to a URL that was never checked with it", single, got)
		}
		if got := settings.values["redis.url"]; got != moved {
			t.Errorf("single=%v: stored redis.url = %q, want the other save's URL left alone", single, got)
		}
	}
}
