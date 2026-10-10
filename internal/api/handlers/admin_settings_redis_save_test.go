package handlers

import (
	"context"
	"encoding/json"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// acceptRedisSaves makes every Redis connection a save checks succeed, for
// tests about what a save stores rather than whether Redis answers.
func acceptRedisSaves(t *testing.T) {
	t.Helper()
	previous := pingRedis
	pingRedis = func(context.Context, config.RedisConfig) error { return nil }
	t.Cleanup(func() { pingRedis = previous })
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

// saveRedisSetting saves one key through the single-key endpoint or the batch
// one.
func saveRedisSetting(t *testing.T, handler *AdminHandler, key, value string, single bool) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if single {
		body, err := json.Marshal(map[string]string{"value": value})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/"+key, strings.NewReader(string(body)))
		handler.HandleUpdateSetting(rec, withChiParam(req, "key", key))
		return rec
	}
	body, err := json.Marshal(map[string]map[string]string{"values": {key: value}})
	if err != nil {
		t.Fatal(err)
	}
	handler.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(string(body))))
	return rec
}

// A start that cannot connect to the Redis its settings name stops the
// server, and the value can then no longer be corrected in the admin UI.
func TestAdminSettingsRefuseRedisSettingsTheServerCannotStartWith(t *testing.T) {
	redis16, _ := fakeRedisServer(t, 16)
	unreachable := closedRedisURL(t)
	for _, tc := range []struct {
		name       string
		stored     map[string]string
		key, value string
		want       string
	}{
		{
			name:  "a host nothing answers on",
			key:   "redis.url",
			value: unreachable,
			want:  "Silo could not connect to Redis",
		},
		{
			name:  "a database number in redis.url the server does not have",
			key:   "redis.url",
			value: redis16 + "/99",
			want:  "Redis refused the connection",
		},
		{
			name:   "a redis.db the server does not have",
			stored: map[string]string{"redis.url": redis16 + "/3"},
			key:    config.RedisDBSettingKey,
			value:  "16",
			want:   "Redis refused the connection",
		},
	} {
		for _, single := range []bool{false, true} {
			stored := tc.stored
			if stored == nil {
				stored = map[string]string{}
			}
			settings := &fakeServerSettingsStore{values: maps.Clone(stored)}
			rec := saveRedisSetting(t, &AdminHandler{SettingsRepo: settings}, tc.key, tc.value, single)

			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_settings") {
				t.Errorf("%s (single=%v): status = %d, body = %s; want a 400 invalid_settings", tc.name, single, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("%s (single=%v): body = %s; want it to say %q", tc.name, single, rec.Body.String(), tc.want)
			}
			// The message is written by Silo. The client error names the
			// address, which the native API does not return.
			if strings.Contains(rec.Body.String(), "127.0.0.1") {
				t.Errorf("%s (single=%v): body = %s; want no address in it", tc.name, single, rec.Body.String())
			}
			if !maps.Equal(settings.values, stored) {
				t.Errorf("%s (single=%v): stored %v, want %v left alone", tc.name, single, settings.values, stored)
			}
		}
	}
}

func TestAdminSettingsSaveRedisSettingsTheServerCanStartWith(t *testing.T) {
	t.Run("a database the server has is checked and stored", func(t *testing.T) {
		for _, single := range []bool{false, true} {
			redis16, checked := fakeRedisServer(t, 16)
			settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": redis16 + "/3"}}
			handler := &AdminHandler{SettingsRepo: settings}

			if rec := saveRedisSetting(t, handler, config.RedisDBSettingKey, "15", single); rec.Code != http.StatusOK {
				t.Fatalf("single=%v: status = %d, want 200; body=%s", single, rec.Code, rec.Body.String())
			}
			if got := settings.values[config.RedisDBSettingKey]; got != "15" {
				t.Errorf("single=%v: stored redis.db = %q, want 15", single, got)
			}
			select {
			case db := <-checked:
				if db != 15 {
					t.Errorf("single=%v: checked database %d, want the 15 being saved", single, db)
				}
			default:
				t.Errorf("single=%v: the save did not check the connection", single)
			}
		}
	})

	// The web form sends redis.url with other edits. A Redis that is down for
	// a moment must not block them.
	t.Run("a redis.url that does not change is not checked", func(t *testing.T) {
		down := closedRedisURL(t) + "/3"
		for _, single := range []bool{false, true} {
			settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": down}}
			if rec := saveRedisSetting(t, &AdminHandler{SettingsRepo: settings}, "redis.url", down, single); rec.Code != http.StatusOK {
				t.Errorf("single=%v: status = %d, want 200; body=%s", single, rec.Code, rec.Body.String())
			}
			if rec := saveRedisSetting(t, &AdminHandler{SettingsRepo: settings}, config.RedisDBSettingKey, "3", single); rec.Code != http.StatusOK {
				t.Errorf("single=%v: redis.db the URL already names: status = %d, want 200; body=%s", single, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("clearing redis.url switches Redis off without a check", func(t *testing.T) {
		for _, single := range []bool{false, true} {
			settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": closedRedisURL(t), "redis.db": "5"}}
			if rec := saveRedisSetting(t, &AdminHandler{SettingsRepo: settings}, "redis.url", "", single); rec.Code != http.StatusOK {
				t.Fatalf("single=%v: status = %d, want 200; body=%s", single, rec.Code, rec.Body.String())
			}
			if got := settings.values["redis.url"]; got != "" {
				t.Errorf("single=%v: stored redis.url = %q, want it cleared", single, got)
			}
		}
	})
}

// A batch checks the database number it stores against the URL it stores.
func TestAdminSettingsCheckRedisURLAndDBSavedTogether(t *testing.T) {
	redis16, checked := fakeRedisServer(t, 16)
	settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": closedRedisURL(t) + "/3"}}
	body := `{"values":{"redis.url":"` + redis16 + `","redis.db":"7"}}`
	rec := httptest.NewRecorder()
	(&AdminHandler{SettingsRepo: settings}).HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	select {
	case db := <-checked:
		if db != 7 {
			t.Errorf("checked database %d, want 7", db)
		}
	default:
		t.Fatal("the save did not check the connection")
	}
	if settings.values["redis.url"] != redis16 || settings.values[config.RedisDBSettingKey] != "7" {
		t.Errorf("stored %v, want the new URL and 7", settings.values)
	}
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

func TestAdminSettingsRefuseRedisSaveWhenTheConnectionMovedDuringTheCheck(t *testing.T) {
	redis16, _ := fakeRedisServer(t, 16)
	for _, single := range []bool{false, true} {
		moved := closedRedisURL(t)
		settings := &racingSettingsStore{
			fakeServerSettingsStore: &fakeServerSettingsStore{values: map[string]string{"redis.url": redis16 + "/3"}},
			concurrent:              map[string]string{"redis.url": moved},
		}
		rec := saveRedisSetting(t, &AdminHandler{SettingsRepo: settings}, config.RedisDBSettingKey, "5", single)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "changed while Silo was checking") {
			t.Errorf("single=%v: status = %d, body = %s; want the save refused", single, rec.Code, rec.Body.String())
		}
		if got, stored := settings.values[config.RedisDBSettingKey]; stored {
			t.Errorf("single=%v: stored redis.db = %q next to a URL that was never checked with it", single, got)
		}
		if got := settings.values["redis.url"]; got != moved {
			t.Errorf("single=%v: stored redis.url = %q, want the other save's URL left alone", single, got)
		}
	}
}
