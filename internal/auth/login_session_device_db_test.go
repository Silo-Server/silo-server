package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestLoginSessionRecordsReportedDeviceDB: a password login records the
// device the request reported in its X-Silo-Device-* headers; without them it
// keeps today's User-Agent name and no id or platform. A malformed id is
// dropped without failing the login, and oversized values are clamped.
func TestLoginSessionRecordsReportedDeviceDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	env.setSetting(t, config.AuthLocalPasswordLoginSettingKey, "true")
	user := env.localAccount(t, "session-device", models.RoleUser)
	users, sessions := NewUserRepository(env.pool), NewSessionRepository(env.pool)
	svc := NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-session-device-secret-0123456789", time.Minute, 24*time.Hour), sessions, users, nil, nil, nil)

	headers := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	type row struct {
		name           string
		id, platform   *string
		listedID       string
		listedPlatform string
	}
	login := func(t *testing.T, h http.Header) row {
		t.Helper()
		pair, _, err := svc.Login(WithClientDevice(ctx, h), user.Username, "correct horse battery", "okhttp/4.12.0", "192.0.2.10")
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		claims, err := svc.jwt.ValidateToken(pair.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		var r row
		if err := env.pool.QueryRow(ctx, `SELECT device_name, device_id, device_platform FROM auth_sessions WHERE id = $1`, claims.SessionID).Scan(&r.name, &r.id, &r.platform); err != nil {
			t.Fatal(err)
		}
		listed, err := sessions.GetByID(ctx, claims.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		r.listedID, r.listedPlatform = listed.DeviceID, listed.DevicePlatform
		return r
	}

	t.Run("reported device", func(t *testing.T) {
		r := login(t, headers(ClientDeviceIDHeader, "android-7f3c9a", ClientDeviceNameHeader, "Google Pixel 8 Pro", ClientDevicePlatformHeader, "android"))
		if r.name != "Google Pixel 8 Pro" || r.id == nil || *r.id != "android-7f3c9a" || r.platform == nil || *r.platform != "android" {
			t.Fatalf("row = %q %v %v", r.name, deref(r.id), deref(r.platform))
		}
		if r.listedID != "android-7f3c9a" || r.listedPlatform != "android" {
			t.Fatalf("read back %q %q", r.listedID, r.listedPlatform)
		}
	})
	t.Run("no device headers keeps the User-Agent", func(t *testing.T) {
		r := login(t, headers("User-Agent", "okhttp/4.12.0"))
		if r.name != "okhttp/4.12.0" || r.id != nil || r.platform != nil || r.listedID != "" || r.listedPlatform != "" {
			t.Fatalf("row = %q %v %v", r.name, deref(r.id), deref(r.platform))
		}
	})
	t.Run("malformed id dropped and long values clamped", func(t *testing.T) {
		r := login(t, headers(ClientDeviceIDHeader, "id one,id two", ClientDeviceNameHeader, strings.Repeat("n", 300), ClientDevicePlatformHeader, strings.Repeat("p", 90)))
		if r.id != nil || utf8.RuneCountInString(r.name) != maxClientDeviceNameLen || r.platform == nil || len(*r.platform) != maxClientDevicePlatformLen {
			t.Fatalf("row = %q %v %v", r.name, deref(r.id), deref(r.platform))
		}
	})
}

// TestDeviceLoginSessionRecordsPollingDeviceDB: a device-code sign-in's
// session keeps the name and platform the device started with and records
// the id its collecting poll reported.
func TestDeviceLoginSessionRecordsPollingDeviceDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("tv"), DevicePlatform: "android-tv", UserAgent: "okhttp/4.12.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, userID); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set(ClientDeviceIDHeader, "tv-0c4d2e")
	poll, err := svc.Poll(WithClientDevice(ctx, h), start.DeviceCode)
	if err != nil || poll.TokenPair == nil {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	var deviceName string
	var deviceID, platform *string
	if err := pool.QueryRow(ctx, `SELECT device_name, device_id, device_platform FROM auth_sessions
		WHERE id = (SELECT auth_session_id FROM device_login_requests WHERE device_name = $1)`, name("tv")).Scan(&deviceName, &deviceID, &platform); err != nil {
		t.Fatal(err)
	}
	if deviceName != name("tv") || deref(deviceID) != "tv-0c4d2e" || deref(platform) != "android-tv" {
		t.Fatalf("session device = %q %q %q", deviceName, deref(deviceID), deref(platform))
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
