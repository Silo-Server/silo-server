package handlers

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"net"
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

// redisCheckServer responds to go-redis initialization and reports which
// database each PING used, so the check exercises the production Redis client.
func redisCheckServer(t *testing.T) (string, <-chan int) {
	t.Helper()
	return fakeRedisServer(t, fakeRedis{})
}

// fakeRedis is the Redis fakeRedisServer plays.
type fakeRedis struct {
	// databases is how many databases the server has: it refuses a SELECT of
	// any other number the way Redis does. With 0 it accepts every number.
	databases int
	// denyChannels refuses every SUBSCRIBE, as Redis does for a user whose
	// ACL does not grant the channels.
	denyChannels bool
}

// fakeRedisServer is redisCheckServer for the Redis server describes. It also
// refuses a subscription to a channel not scoped to the selected database, so
// a check that names the channels wrongly fails.
func fakeRedisServer(t *testing.T, server fakeRedis) (string, <-chan int) {
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
						n, err := strconv.Atoi(args[1])
						if err != nil {
							return
						}
						if server.databases > 0 && (n < 0 || n >= server.databases) {
							reply = "-ERR DB index is out of range\r\n"
							break
						}
						db = n
					case "PING":
						select {
						case checked <- db:
						default:
						}
						reply = "+PONG\r\n"
					case "SUBSCRIBE":
						reply = subscribeReply(server, db, args[1:])
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

// subscribeReply answers a SUBSCRIBE of channels on database db.
func subscribeReply(server fakeRedis, db int, channels []string) string {
	var reply strings.Builder
	for i, channel := range channels {
		if server.denyChannels {
			return "-NOPERM User default has no permissions to access the '" + channel + "' channel\r\n"
		}
		_, scope, _ := strings.Cut(channel, "@")
		if want := "db" + strconv.Itoa(db); (db > 0 && scope != want) || (db == 0 && scope != "") {
			return "-ERR channel " + channel + " is not scoped to database " + strconv.Itoa(db) + "\r\n"
		}
		fmt.Fprintf(&reply, "*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:%d\r\n", len(channel), channel, i+1)
	}
	return reply.String()
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

func TestAdminSettingsSaveRedisDB(t *testing.T) {
	acceptRedisSaves(t)
	const savedURL = "redis://cache.example.invalid:6379/3"
	save := func(t *testing.T, stored, values map[string]string) (*fakeServerSettingsStore, AdminSettingsUpdateResult) {
		t.Helper()
		settings := &fakeServerSettingsStore{values: stored}
		response, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSettings(t.Context(), values, nil)
		if err != nil {
			t.Fatal(err)
		}
		return settings, response
	}

	t.Run("a new number is stored and needs a restart", func(t *testing.T) {
		settings, response := save(t, map[string]string{"redis.url": savedURL}, map[string]string{"redis.db": " 05 "})
		if got := settings.values["redis.db"]; got != "5" {
			t.Errorf("stored redis.db = %q, want 5", got)
		}
		if response.Values["redis.db"] != "5" || !slices.Contains(response.RestartRequiredKeys, "redis.db") {
			t.Errorf("response = %+v, want redis.db 5 and a restart for it", response)
		}
	})

	t.Run("the number already in use stores nothing", func(t *testing.T) {
		settings, response := save(t, map[string]string{"redis.url": savedURL}, map[string]string{"redis.db": "3"})
		if got, stored := settings.values["redis.db"]; stored {
			t.Errorf("stored redis.db = %q, want no row for the number the URL already names", got)
		}
		if response.RestartRequired {
			t.Errorf("response = %+v, want no restart for an unchanged database number", response)
		}
	})

	t.Run("clearing it returns to the number in the URL", func(t *testing.T) {
		settings, response := save(t, map[string]string{"redis.url": savedURL, "redis.db": "5"}, map[string]string{"redis.db": ""})
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
		settings, _ := save(t, map[string]string{"redis.url": savedURL}, map[string]string{"redis.url": "redis://other.example.invalid:6379", "redis.db": "3"})
		if got := settings.values["redis.db"]; got != "3" {
			t.Errorf("stored redis.db = %q, want 3", got)
		}
		if db := redisDatabaseAfterRestart(t, settings); db != 3 {
			t.Errorf("database number after the save = %d, want 3", db)
		}
	})

	t.Run("a number equal to the one in the new URL stores no row", func(t *testing.T) {
		settings, _ := save(t, map[string]string{"redis.url": savedURL}, map[string]string{"redis.url": "redis://other.example.invalid:6379/7", "redis.db": "7"})
		if got, stored := settings.values["redis.db"]; stored {
			t.Errorf("stored redis.db = %q, want no row for the number the new URL names", got)
		}
	})
}

// The row holds a number only while it differs from the one the URL names, so
// what a save stores cannot depend on the saves before it. Each of these
// installs shows 3 or 5 and is then given the 3 its URL already names.
func TestAdminSettingsRedisDBRowIgnoresEarlierSaves(t *testing.T) {
	acceptRedisSaves(t)
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
			if err := saveAdminSetting(t, handler, config.RedisDBSettingKey, "3", single); err != nil {
				t.Fatalf("%s (single=%v): %v", name, single, err)
			}
			if got := settings.values["redis.db"]; got != "" {
				t.Errorf("%s (single=%v): stored redis.db = %q, want no number for the 3 the URL names", name, single, got)
			}
			if db := redisDatabaseAfterRestart(t, settings); db != 3 {
				t.Errorf("%s (single=%v): database number = %d, want 3", name, single, db)
			}

			// A save of the URL alone then moves every one of them alike.
			if err := saveAdminSetting(t, handler, "redis.url", "redis://cache.example.invalid:6379/7", false); err != nil {
				t.Fatalf("%s (single=%v): URL save: %v", name, single, err)
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
		// The answer matches what was stored: no number, and no restart for it.
		if single {
			response, err := handler.UpdateAdminSetting(t.Context(), config.RedisDBSettingKey, "4", nil)
			if err != nil || response.Value != "" || response.RestartRequired {
				t.Errorf("single save answered %+v, %v; want no number and no restart", response, err)
			}
		} else {
			response, err := handler.UpdateAdminSettings(t.Context(), map[string]string{config.RedisDBSettingKey: "4"}, nil)
			if err != nil || response.Values["redis.db"] != "" || response.RestartRequired {
				t.Errorf("batch save answered %+v, %v; want no number and no restart", response, err)
			}
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
		if _, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSettings(t.Context(), map[string]string{"redis.url": "", "redis.db": "7"}, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if settings.values["redis.url"] != "" || settings.values["redis.db"] != "" {
			t.Errorf("%s: stored redis.url = %q and redis.db = %q after Redis was switched off, want neither",
				name, settings.values["redis.url"], settings.values["redis.db"])
		}
	}
}

// A redis.url saved by an older build can be a bare address, which names no
// number to compare with, so the row keeps the number saved next to it.
func TestAdminSettingsStoreRedisDBNextToAnUnreadableURL(t *testing.T) {
	acceptRedisSaves(t)
	for _, single := range []bool{false, true} {
		settings := &fakeServerSettingsStore{values: map[string]string{"redis.url": "cache.example.invalid:6379"}}
		if err := saveAdminSetting(t, &AdminHandler{SettingsRepo: settings}, config.RedisDBSettingKey, "0", single); err != nil {
			t.Fatalf("single=%v: %v", single, err)
		}
		if got := settings.values["redis.db"]; got != "0" {
			t.Errorf("single=%v: stored redis.db = %q, want 0", single, got)
		}
	}
}

func TestAdminSettingsRefuseRedisDBManagedByEnvironment(t *testing.T) {
	for _, single := range []bool{false, true} {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		requireAdminError(t, saveAdminSetting(t, redisFromEnvironment(settings), config.RedisDBSettingKey, "5", single), 400, "managed_by_environment")
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

		requireAdminError(t, saveAdminSetting(t, handler, config.RedisDBSettingKey, "5", single), 400, "")
		if got := settings.values["redis.db"]; got != "99" {
			t.Fatalf("single=%v: stored redis.db = %q after a refused save, want the 99 left alone", single, got)
		}
		if err := saveAdminSetting(t, handler, config.RedisDBSettingKey, " ", single); err != nil {
			t.Fatalf("single=%v: clearing: %v", single, err)
		}
		if got := settings.values["redis.db"]; got != "" {
			t.Errorf("single=%v: stored redis.db = %q after clearing, want it empty", single, got)
		}
		if got := effectiveSettingsOf(t, handler)[config.RedisDBSettingKey]; got != "1" {
			t.Errorf("single=%v: effective redis.db = %q, want the 1 in REDIS_URL", single, got)
		}
	}
}

func TestRedisConnectionCheckUsesRedisDB(t *testing.T) {
	baseURL, checked := redisCheckServer(t)
	const savedURL = "redis://cache.example.invalid:6379/3"
	for _, tc := range []struct {
		name    string
		handler *AdminHandler
		values  map[string]string
		dirty   []string
		wantDB  int
	}{
		{
			name:    "a number being edited",
			handler: &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL}}},
			values:  map[string]string{"redis.url": "", "redis.db": "99"},
			dirty:   []string{"redis.db"},
			wantDB:  99,
		},
		{
			name:    "the saved number when the field is untouched",
			handler: &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL, "redis.db": "5"}}},
			values:  map[string]string{"redis.url": "", "redis.db": "5"},
			wantDB:  5,
		},
		{
			name:    "the number in the URL when none is saved",
			handler: &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{"redis.url": savedURL}}},
			values:  map[string]string{"redis.url": "", "redis.db": "3"},
			wantDB:  3,
		},
		{
			name: "the number in REDIS_URL, not a saved redis.db",
			handler: redisFromEnvironment(&fakeServerSettingsStore{values: map[string]string{
				"redis.url": savedURL,
				"redis.db":  "5",
			}}),
			values: map[string]string{"redis.url": "", "redis.db": "1"},
			wantDB: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.handler.SettingsRepo.(*fakeServerSettingsStore).values["redis.url"] = baseURL + "/3"
			if tc.handler.BootstrapSensitiveConfigured["redis.url"] {
				tc.handler.BootstrapSensitiveValues["redis.url"] = baseURL + "/1"
			}

			result, err := tc.handler.CheckAdminSettingsConnection(t.Context(), "redis", tc.values, tc.dirty)
			if err != nil || !result.Success {
				t.Fatalf("Redis check failed: %+v, %v", result, err)
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
