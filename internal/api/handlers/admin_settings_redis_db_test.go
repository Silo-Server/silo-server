package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

// redisFromEnvironment is the handler state main builds for a process started
// with REDIS_URL=redis://env.example.invalid:6379/1.
func redisFromEnvironment(settings *fakeServerSettingsStore) *AdminHandler {
	return &AdminHandler{
		SettingsRepo: settings,
		BootstrapSensitiveConfigured: map[string]bool{
			"redis.url":              true,
			config.RedisDBSettingKey: true,
		},
		BootstrapSensitiveValues: map[string]string{
			"redis.url":              "redis://env.example.invalid:6379/1",
			config.RedisDBSettingKey: "1",
		},
		RedisBootstrapAvailable: true,
	}
}

func effectiveSettingsOf(t *testing.T, handler *AdminHandler) map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.HandleGetEffectiveSettings(rec, httptest.NewRequest(http.MethodGet, "/admin/settings/effective", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var values map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&values); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, leaked := values["redis.url"]; leaked {
		t.Fatal("effective settings response leaked redis.url")
	}
	return values
}

func TestAdminEffectiveSettingsReportRedisDatabaseInUse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored map[string]string
		want   string
	}{
		{"the number in the saved URL", map[string]string{"redis.url": "redis://cache.example.invalid:6379/3"}, "3"},
		{"0 for a saved URL without a number", map[string]string{"redis.url": "redis://cache.example.invalid:6379"}, "0"},
		{"the number in a Sentinel URL", map[string]string{"redis.url": "redis://sentinel.example.invalid:26379/2?master_name=mymaster"}, "2"},
		{"a saved redis.db", map[string]string{"redis.url": "redis://cache.example.invalid:6379/3", "redis.db": "5"}, "5"},
		{"a saved redis.db of 0", map[string]string{"redis.url": "redis://cache.example.invalid:6379/3", "redis.db": "0"}, "0"},
		{"the number in the URL after redis.db was cleared", map[string]string{"redis.url": "redis://cache.example.invalid:6379/3", "redis.db": ""}, "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: tc.stored}}
			if got := effectiveSettingsOf(t, handler)[config.RedisDBSettingKey]; got != tc.want {
				t.Errorf("effective redis.db = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("nothing without Redis", func(t *testing.T) {
		handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{}}}
		if got, reported := effectiveSettingsOf(t, handler)[config.RedisDBSettingKey]; reported {
			t.Errorf("effective redis.db = %q on an install without Redis, want it left out", got)
		}
	})

	t.Run("the number in REDIS_URL, not a saved redis.db", func(t *testing.T) {
		handler := redisFromEnvironment(&fakeServerSettingsStore{values: map[string]string{
			"redis.url": "redis://cache.example.invalid:6379/3",
			"redis.db":  "5",
		}})
		if got := effectiveSettingsOf(t, handler)[config.RedisDBSettingKey]; got != "1" {
			t.Errorf("effective redis.db = %q, want the 1 in REDIS_URL", got)
		}
	})
}

type redisDBSaveResponse struct {
	Values              map[string]string `json:"values"`
	RestartRequired     bool              `json:"restart_required"`
	RestartRequiredKeys []string          `json:"restart_required_keys"`
}

func saveSettings(t *testing.T, handler *AdminHandler, body string) (*httptest.ResponseRecorder, redisDBSaveResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(body)))
	var response redisDBSaveResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec, response
}

func TestAdminSettingsSaveRedisDB(t *testing.T) {
	const savedURL = "redis://cache.example.invalid:6379/3"

	t.Run("a new number is stored and needs a restart", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL}}
		rec, response := saveSettings(t, &AdminHandler{SettingsRepo: settings}, `{"values":{"redis.db":" 05 "}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got := settings.values["redis.db"]; got != "5" {
			t.Errorf("stored redis.db = %q, want 5", got)
		}
		if response.Values["redis.db"] != "5" || !slices.Contains(response.RestartRequiredKeys, "redis.db") {
			t.Errorf("response = %+v, want redis.db 5 and a restart for it", response)
		}
	})

	t.Run("the number already in use stores nothing", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL}}
		rec, response := saveSettings(t, &AdminHandler{SettingsRepo: settings}, `{"values":{"redis.db":"3"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got, stored := settings.values["redis.db"]; stored {
			t.Errorf("stored redis.db = %q, want no row for the number the URL already names", got)
		}
		if response.RestartRequired {
			t.Errorf("response = %+v, want no restart for an unchanged database number", response)
		}
	})

	t.Run("clearing it returns to the number in the URL", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL, "redis.db": "5"}}
		rec, response := saveSettings(t, &AdminHandler{SettingsRepo: settings}, `{"values":{"redis.db":""}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got := settings.values["redis.db"]; got != "" {
			t.Errorf("stored redis.db = %q, want it cleared", got)
		}
		if response.Values["redis.db"] != "3" || !slices.Contains(response.RestartRequiredKeys, "redis.db") {
			t.Errorf("response = %+v, want the URL's 3 and a restart for redis.db", response)
		}
	})

	// The number in effect before the batch is the 3 in the old URL. The new
	// URL has no number, so without a row the install would move to database 0.
	t.Run("a number saved together with a new URL is stored", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL}}
		rec, _ := saveSettings(t, &AdminHandler{SettingsRepo: settings},
			`{"values":{"redis.url":"redis://other.example.invalid:6379","redis.db":"3"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got := settings.values["redis.db"]; got != "3" {
			t.Errorf("stored redis.db = %q, want 3", got)
		}
		cfg, err := config.LoadFromDB(settings.values)
		if err != nil {
			t.Fatal(err)
		}
		if db, ok := cfg.Redis.Database(); !ok || db != 3 {
			t.Errorf("database number after the save = (%d, %v), want 3", db, ok)
		}
	})

	t.Run("a number equal to the one in the new URL stores no row", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL}}
		rec, _ := saveSettings(t, &AdminHandler{SettingsRepo: settings},
			`{"values":{"redis.url":"redis://other.example.invalid:6379/7","redis.db":"7"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got, stored := settings.values["redis.db"]; stored {
			t.Errorf("stored redis.db = %q, want no row for the number the new URL names", got)
		}
	})
}

// saveRedisDB saves redis.db alone, through the single-key endpoint or the
// batch one.
func saveRedisDB(handler *AdminHandler, value string, single bool) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	if single {
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/redis.db", strings.NewReader(`{"value":"`+value+`"}`))
		handler.HandleUpdateSetting(rec, withChiParam(req, "key", "redis.db"))
	} else {
		req := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"values":{"redis.db":"`+value+`"}}`))
		handler.HandleUpdateSettings(rec, req)
	}
	return rec
}

// redisDatabaseAfterRestart is the database number a server started on the
// stored settings would use.
func redisDatabaseAfterRestart(t *testing.T, settings *fakeServerSettingsStore) int {
	t.Helper()
	cfg, err := config.LoadFromDB(settings.values)
	if err != nil {
		t.Fatal(err)
	}
	db, ok := cfg.Redis.Database()
	if !ok {
		t.Fatalf("the stored settings %v name no database number", settings.values)
	}
	return db
}

// The row holds a number only while it differs from the one the URL names, so
// what a save stores cannot depend on the saves before it. Each of these
// installs shows 3 or 5 and is then given the 3 its URL already names.
func TestAdminSettingsRedisDBRowIgnoresEarlierSaves(t *testing.T) {
	const savedURL = "redis://cache.example.invalid:6379/3"
	for name, stored := range map[string]map[string]string{
		"never set":              {"redis.url": savedURL},
		"set once, then cleared": {"redis.url": savedURL, "redis.db": ""},
		"set to 5":               {"redis.url": savedURL, "redis.db": "5"},
		// A URL-only save can bring the URL to the number a row already holds.
		"set to 3 before the URL named it": {"redis.url": savedURL, "redis.db": "3"},
	} {
		for _, single := range []bool{false, true} {
			settings := &fakeServerSettingsStore{values: maps.Clone(stored)}
			handler := &AdminHandler{SettingsRepo: settings}
			if rec := saveRedisDB(handler, "3", single); rec.Code != http.StatusOK {
				t.Fatalf("%s (single=%v): status = %d, want 200; body=%s", name, single, rec.Code, rec.Body.String())
			}
			if got := settings.values["redis.db"]; got != "" {
				t.Errorf("%s (single=%v): stored redis.db = %q, want no number for the 3 the URL names", name, single, got)
			}
			if db := redisDatabaseAfterRestart(t, settings); db != 3 {
				t.Errorf("%s (single=%v): database number = %d, want 3", name, single, db)
			}

			// A save of the URL alone then moves every one of them alike.
			if rec, _ := saveSettings(t, handler, `{"values":{"redis.url":"redis://cache.example.invalid:6379/7"}}`); rec.Code != http.StatusOK {
				t.Fatalf("%s (single=%v): URL save status = %d, want 200; body=%s", name, single, rec.Code, rec.Body.String())
			}
			if db := redisDatabaseAfterRestart(t, settings); db != 7 {
				t.Errorf("%s (single=%v): database number after a URL-only save = %d, want the URL's 7", name, single, db)
			}
		}
	}
}

func TestAdminSettingsStoreNoRedisDBWithoutRedis(t *testing.T) {
	const savedURL = "redis://cache.example.invalid:6379/3"

	for _, single := range []bool{false, true} {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		handler := &AdminHandler{SettingsRepo: settings}
		rec := saveRedisDB(handler, "4", single)
		if rec.Code != http.StatusOK {
			t.Fatalf("single=%v: status = %d, want 200; body=%s", single, rec.Code, rec.Body.String())
		}
		// The answer matches what was stored: no number, and no restart for it.
		var answer struct {
			Value           string            `json:"value"`
			Values          map[string]string `json:"values"`
			RestartRequired bool              `json:"restart_required"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
			t.Fatalf("single=%v: decode response: %v; body=%s", single, err, rec.Body.String())
		}
		if answer.Value != "" || answer.Values["redis.db"] != "" || answer.RestartRequired {
			t.Errorf("single=%v: the save answered %s, want no number and no restart", single, rec.Body.String())
		}
		if got := settings.values["redis.db"]; got != "" {
			t.Errorf("single=%v: stored redis.db = %q on an install without Redis, want none", single, got)
		}
		if got, reported := effectiveSettingsOf(t, handler)[config.RedisDBSettingKey]; reported {
			t.Errorf("single=%v: effective redis.db = %q on an install without Redis, want it left out", single, got)
		}
	}

	// A batch can carry a number next to the URL it clears.
	for name, stored := range map[string]map[string]string{
		"no number saved": {"redis.url": savedURL},
		"a number saved":  {"redis.url": savedURL, "redis.db": "5"},
	} {
		settings := &fakeServerSettingsStore{values: stored}
		rec, _ := saveSettings(t, &AdminHandler{SettingsRepo: settings}, `{"values":{"redis.url":"","redis.db":"7"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body=%s", name, rec.Code, rec.Body.String())
		}
		if settings.values["redis.url"] != "" || settings.values["redis.db"] != "" {
			t.Errorf("%s: stored redis.url = %q and redis.db = %q after Redis was switched off, want neither",
				name, settings.values["redis.url"], settings.values["redis.db"])
		}
	}
}

// A redis.url saved by an older build can be a bare address, which names no
// number to compare with.
func TestAdminSettingsStoreRedisDBNextToAnUnreadableURL(t *testing.T) {
	for _, single := range []bool{false, true} {
		settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": "cache.example.invalid:6379"}}
		if rec := saveRedisDB(&AdminHandler{SettingsRepo: settings}, "0", single); rec.Code != http.StatusOK {
			t.Fatalf("single=%v: status = %d, want 200; body=%s", single, rec.Code, rec.Body.String())
		}
		if got := settings.values["redis.db"]; got != "0" {
			t.Errorf("single=%v: stored redis.db = %q, want 0", single, got)
		}
	}
}

func TestAdminSettingsRejectInvalidRedisDB(t *testing.T) {
	for _, value := range []string{"-1", "three", "1.5"} {
		for _, single := range []bool{false, true} {
			settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": "redis://cache.example.invalid:6379/3"}}
			rec := saveRedisDB(&AdminHandler{SettingsRepo: settings}, value, single)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "redis.db") {
				t.Errorf("redis.db=%q (single=%v): status = %d, body = %s; want a 400 that names redis.db", value, single, rec.Code, rec.Body.String())
			}
			if _, stored := settings.values["redis.db"]; stored {
				t.Errorf("redis.db=%q (single=%v) was stored", value, single)
			}
		}
	}
}

func TestAdminSettingsRefuseRedisDBManagedByEnvironment(t *testing.T) {
	for _, single := range []bool{false, true} {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		rec := saveRedisDB(redisFromEnvironment(settings), "5", single)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "managed_by_environment") {
			t.Errorf("single=%v: status = %d, body = %s; want managed_by_environment", single, rec.Code, rec.Body.String())
		}
		if _, stored := settings.values["redis.db"]; stored {
			t.Errorf("single=%v: redis.db was stored on an install that takes Redis from REDIS_URL", single)
		}
	}

	status := redisFromEnvironment(&fakeServerSettingsStore{}).adminSensitiveSettingsStatus(nil)
	if !slices.Contains(status.ManagedByEnv, "redis.db") {
		t.Errorf("managed by environment = %v, want redis.db listed for the admin UI", status.ManagedByEnv)
	}
	if slices.Contains(status.Configured, "redis.db") {
		t.Errorf("configured secrets = %v, want redis.db left out: it is not a secret", status.Configured)
	}
}

// REDIS_URL keeps a server up whose saved number it cannot start with. The
// number has to be removable from there, or taking REDIS_URL away again
// brings the failed start back.
func TestAdminSettingsClearRedisDBManagedByEnvironment(t *testing.T) {
	for _, single := range []bool{false, true} {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"redis.url": "redis://cache.example.invalid:6379/3",
			"redis.db":  "99",
		}}
		handler := redisFromEnvironment(settings)

		if rec := saveRedisDB(handler, "5", single); rec.Code != http.StatusBadRequest {
			t.Errorf("single=%v: saving a number: status = %d, want 400; body=%s", single, rec.Code, rec.Body.String())
		}
		if got := settings.values["redis.db"]; got != "99" {
			t.Fatalf("single=%v: stored redis.db = %q after a refused save, want the 99 left alone", single, got)
		}

		if rec := saveRedisDB(handler, " ", single); rec.Code != http.StatusOK {
			t.Fatalf("single=%v: clearing: status = %d, want 200; body=%s", single, rec.Code, rec.Body.String())
		}
		if got := settings.values["redis.db"]; got != "" {
			t.Errorf("single=%v: stored redis.db = %q after clearing, want it empty", single, got)
		}
		if got := effectiveSettingsOf(t, handler)[config.RedisDBSettingKey]; got != "1" {
			t.Errorf("single=%v: effective redis.db = %q, want the 1 in REDIS_URL", single, got)
		}
	}
}

// redisCheckServer responds to go-redis initialization and reports which
// database each PING used, so the check exercises the production Redis client.
func redisCheckServer(t *testing.T) (string, <-chan int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	checked := make(chan int, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				r := bufio.NewReader(conn)
				db := 0
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
					if err != nil || n < 1 {
						return
					}
					args := make([]string, n)
					for i := range args {
						line, err := r.ReadString('\n')
						if err != nil {
							return
						}
						length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "$")))
						if err != nil || length < 0 {
							return
						}
						value := make([]byte, length+2)
						if _, err := io.ReadFull(r, value); err != nil {
							return
						}
						args[i] = string(value[:length])
					}
					reply := "+OK\r\n"
					switch strings.ToUpper(args[0]) {
					case "HELLO":
						reply = "-ERR unknown command\r\n"
					case "SELECT":
						if len(args) != 2 {
							return
						}
						db, err = strconv.Atoi(args[1])
						if err != nil {
							return
						}
					case "PING":
						checked <- db
						reply = "+PONG\r\n"
					}
					if _, err := fmt.Fprint(conn, reply); err != nil {
						return
					}
				}
			}()
		}
	}()
	return "redis://" + listener.Addr().String(), checked
}

func TestRedisConnectionCheckUsesRedisDB(t *testing.T) {
	baseURL, checked := redisCheckServer(t)
	const savedURL = "redis://cache.example.invalid:6379/3"
	for _, tc := range []struct {
		name    string
		handler *AdminHandler
		body    string
		wantDB  int
	}{
		{
			name:    "a number being edited",
			handler: &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL}}},
			body:    `{"values":{"redis.url":"","redis.db":"99"},"dirty_keys":["redis.db"]}`,
			wantDB:  99,
		},
		{
			name:    "the saved number when the field is untouched",
			handler: &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL, "redis.db": "5"}}},
			body:    `{"values":{"redis.url":"","redis.db":"5"},"dirty_keys":[]}`,
			wantDB:  5,
		},
		{
			name:    "the number in the URL when none is saved",
			handler: &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL}}},
			body:    `{"values":{"redis.url":"","redis.db":"3"},"dirty_keys":[]}`,
			wantDB:  3,
		},
		{
			name: "the number in REDIS_URL, not a saved redis.db",
			handler: redisFromEnvironment(&fakeServerSettingsStore{values: map[string]string{
				"redis.url": savedURL,
				"redis.db":  "5",
			}}),
			body:   `{"values":{"redis.url":"","redis.db":"1"},"dirty_keys":[]}`,
			wantDB: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.handler.SettingsRepo.(*fakeServerSettingsStore).values["redis.url"] = baseURL + "/3"
			if tc.handler.BootstrapSensitiveConfigured["redis.url"] {
				tc.handler.BootstrapSensitiveValues["redis.url"] = baseURL + "/1"
			}

			req := httptest.NewRequest(http.MethodPost, "/admin/settings/check/redis", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			tc.handler.HandleCheckSettingsConnection(rec, withChiParam(req, "kind", "redis"))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var response connectionCheckResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || !response.Success {
				t.Fatalf("Redis check failed: %s (%v)", rec.Body.String(), err)
			}
			select {
			case db := <-checked:
				if db != tc.wantDB {
					t.Errorf("checked database number = %d, want %d", db, tc.wantDB)
				}
			default:
				t.Fatal("Redis check did not send a PING")
			}
		})
	}
}
