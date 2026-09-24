package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeviceProfileRequestLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"body", `{"Ignored":"` + strings.Repeat("x", maxDeviceProfileRequestBytes) + `"}`},
		{"profile bytes", `{"DeviceProfile":{"Name":"` + strings.Repeat("x", maxDeviceProfileBytes) + `"}}`},
		{"profile entries", `{"DeviceProfile":{"DirectPlayProfiles":[` + strings.Repeat(`{},`, maxDeviceProfileEntries) + `{}]}}`},
		{"nested conditions", `{"DeviceProfile":{"CodecProfiles":[{"Conditions":[` + strings.Repeat(`{},`, maxDeviceProfileEntries) + `{}]}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewDeviceProfileStore(time.Hour, nil)
			h := &PlaybackHandler{deviceProfiles: store}
			rec := httptest.NewRecorder()
			h.HandleCapabilitiesFull(rec, viewerRequest(http.MethodPost, "/Sessions/Capabilities/Full?DeviceId=tv", tc.body, "", "", &Session{Token: "token"}))
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("capabilities status=%d body=%s", rec.Code, rec.Body.String())
			}
			_, _, err := h.parsePlaybackRequest(httptest.NewRequest(http.MethodPost, "/Items/item/PlaybackInfo?DeviceId=tv", strings.NewReader(tc.body)), "token")
			httpErr, ok := errors.AsType[*HTTPError](err)
			if !ok || httpErr.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("playback error=%v", err)
			}
			if len(store.profiles) != 0 {
				t.Fatal("rejected profile was stored")
			}
		})
	}
}

// Jellyfin Web derives DeviceId from the user agent; embedded browsers exceed
// the storage bound and must still negotiate playback.
func TestDeviceProfileLongDeviceIDsAreKeyedByHash(t *testing.T) {
	store := NewDeviceProfileStore(time.Hour, nil)
	long := strings.Repeat("x", maxDeviceIDBytes+8)
	otherLong := strings.Repeat("x", maxDeviceIDBytes+7) + "y"
	if err := store.PutForDevice(t.Context(), "token", long, DeviceProfile{Name: "long"}); err != nil {
		t.Fatalf("long device ID: %v", err)
	}
	if err := store.PutForDevice(t.Context(), "token", otherLong, DeviceProfile{Name: "other"}); err != nil {
		t.Fatalf("second long device ID: %v", err)
	}
	for id, want := range map[string]string{long: "long", otherLong: "other"} {
		profile, ok, err := store.GetForDevice(t.Context(), "token", id)
		if err != nil || !ok || profile.Name != want {
			t.Fatalf("GetForDevice = %+v, %t, %v; want %q", profile, ok, err, want)
		}
	}
	if got := deviceProfileStorageID(long); len(got) > maxDeviceIDBytes || got == deviceProfileStorageID(otherLong) {
		t.Fatalf("storage ID %q is unbounded or collides", got)
	}
	exact := strings.Repeat("x", maxDeviceIDBytes)
	if got := deviceProfileStorageID(exact); got != exact {
		t.Fatalf("device ID within the bound was rewritten: %q", got)
	}

	// A short ID spelled like a stored digest must not reach the long ID's key.
	lookalike := deviceProfileStorageID(long)
	if err := store.PutForDevice(t.Context(), "token", lookalike, DeviceProfile{Name: "lookalike"}); err != nil {
		t.Fatalf("digest-shaped device ID: %v", err)
	}
	for id, want := range map[string]string{long: "long", lookalike: "lookalike"} {
		profile, ok, err := store.GetForDevice(t.Context(), "token", id)
		if err != nil || !ok || profile.Name != want {
			t.Fatalf("GetForDevice(%q...) = %+v, %t, %v; want %q", id[:12], profile, ok, err, want)
		}
	}
}

func TestDeviceProfileRegistrationQuota(t *testing.T) {
	now := time.Now()
	store := NewDeviceProfileStore(time.Hour, func() time.Time { return now })
	assertDeviceProfileQuota(t, store, store, "token")
	now = now.Add(2 * time.Hour)
	if err := store.PutForDevice(t.Context(), "token", "new-after-expiry", DeviceProfile{Name: "new"}); err != nil {
		t.Fatalf("expired registrations consumed quota: %v", err)
	}
	if len(store.profiles) != 2 { // One registration belongs to another token.
		t.Fatalf("expired registrations retained: %d", len(store.profiles))
	}
}

func TestDeviceProfileRegistrationQuotaAcrossProcesses(t *testing.T) {
	pool := newCompatTestPool(t)
	token := fmt.Sprintf("profile-quota-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM jellycompat_device_profiles WHERE token_hash=ANY($1)`, []string{deviceProfileTokenHash(token), deviceProfileTokenHash(token + "-other")})
	})
	first := NewDeviceProfileStore(time.Hour, nil).WithDB(pool)
	second := NewDeviceProfileStore(time.Hour, nil).WithDB(pool)
	assertDeviceProfileQuota(t, first, second, token)
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM jellycompat_device_profiles WHERE token_hash=$1`, deviceProfileTokenHash(token)).Scan(&count); err != nil || count != maxDeviceProfilesPerToken {
		t.Fatalf("registration count=%d err=%v", count, err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE jellycompat_device_profiles SET expires_at=$2 WHERE token_hash=$1`, deviceProfileTokenHash(token), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := first.PutForDevice(t.Context(), token, "new-after-expiry", DeviceProfile{Name: "new"}); err != nil {
		t.Fatalf("expired registrations consumed quota: %v", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM jellycompat_device_profiles WHERE token_hash=$1`, deviceProfileTokenHash(token)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expired registration count=%d err=%v", count, err)
	}
}

func assertDeviceProfileQuota(t *testing.T, first, second *DeviceProfileStore, token string) {
	t.Helper()
	profile := DeviceProfile{Name: "profile", DirectPlayProfiles: []DirectPlayProfile{{Type: "Video", Container: "mp4", VideoCodec: "h264"}}}
	for i := range maxDeviceProfilesPerToken - 1 {
		if err := first.PutForDevice(t.Context(), token, fmt.Sprintf("device-%d", i), profile); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := range cap(results) {
		wg.Go(func() {
			<-start
			store := first
			if i%2 != 0 {
				store = second
			}
			results <- store.PutForDevice(t.Context(), token, fmt.Sprintf("concurrent-%d", i), profile)
		})
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if httpErr, ok := errors.AsType[*HTTPError](err); !ok || httpErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("registration error=%v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent registrations admitted=%d want=1", succeeded)
	}
	profile.Name = "updated"
	if err := second.PutForDevice(t.Context(), token, "device-0", profile); err != nil {
		t.Fatalf("existing device could not refresh at capacity: %v", err)
	}
	actual, ok, err := first.GetForDevice(t.Context(), token, "device-0")
	if err != nil || !ok || actual.Name != profile.Name {
		t.Fatalf("updated profile=%+v found=%v err=%v", actual, ok, err)
	}
	if err := second.PutForDevice(t.Context(), token+"-other", "other", profile); err != nil {
		t.Fatalf("quota leaked between tokens: %v", err)
	}
	body, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h := &PlaybackHandler{deviceProfiles: first}
	h.HandleCapabilitiesFull(rec, viewerRequest(http.MethodPost, "/Sessions/Capabilities/Full?DeviceId=rejected", string(body), "", "", &Session{Token: token}))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("quota response=%d body=%s", rec.Code, rec.Body.String())
	}
}
