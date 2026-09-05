package jellycompat

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestDeviceProfilesAreScopedByDevice(t *testing.T) {
	store := NewDeviceProfileStore(time.Hour, nil)
	if err := store.PutForDevice(t.Context(), "shared-token", "tv", DeviceProfile{Name: "tv"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.GetForDevice(t.Context(), "shared-token", "phone"); err != nil || ok {
		t.Fatalf("other device ok=%v err=%v", ok, err)
	}
	profile, ok, err := store.GetForDevice(t.Context(), "shared-token", "tv")
	if err != nil || !ok || profile.Name != "tv" {
		t.Fatalf("profile=%+v ok=%v err=%v", profile, ok, err)
	}
}

func TestDeviceProfileRegistrationAcrossProcesses(t *testing.T) {
	pool := newCompatTestPool(t)
	token := fmt.Sprintf("profile-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM jellycompat_device_profiles WHERE token_hash=$1`, deviceProfileTokenHash(token))
	})
	first := NewDeviceProfileStore(time.Hour, nil).WithDB(pool)
	second := NewDeviceProfileStore(time.Hour, nil).WithDB(pool)
	expected := DeviceProfile{Name: "tv", MaxStreamingBitrate: 4000000, DirectPlayProfiles: []DirectPlayProfile{{Type: "Video", VideoCodec: "h264"}}}
	if err := first.PutForDevice(t.Context(), token, "tv", expected); err != nil {
		t.Fatal(err)
	}
	actual, ok, err := second.GetForDevice(t.Context(), token, "tv")
	if err != nil || !ok || actual.MaxStreamingBitrate != 4000000 || actual.DirectPlayProfiles[0].VideoCodec != "h264" {
		t.Fatalf("profile=%+v ok=%v err=%v", actual, ok, err)
	}
	if _, ok, err := second.GetForDevice(t.Context(), token, "phone"); err != nil || ok {
		t.Fatalf("other device ok=%v err=%v", ok, err)
	}
	restarted := NewDeviceProfileStore(time.Hour, nil).WithDB(pool)
	if _, ok, err := restarted.GetForDevice(t.Context(), token, "tv"); err != nil || !ok {
		t.Fatalf("restart ok=%v err=%v", ok, err)
	}
}

func TestDeviceProfileExpirationCleanup(t *testing.T) {
	pool := newCompatTestPool(t)
	now := time.Now().UTC().Add(-2 * time.Hour)
	store := NewDeviceProfileStore(time.Hour, func() time.Time { return now }).WithDB(pool)
	token := fmt.Sprintf("profile-expiry-test-%d", now.UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM jellycompat_device_profiles WHERE token_hash=$1`, deviceProfileTokenHash(token))
	})
	if err := store.PutForDevice(t.Context(), token, "expired", DeviceProfile{Name: "expired"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := store.PutForDevice(t.Context(), token, "active", DeviceProfile{Name: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.GetForDevice(t.Context(), token, "expired"); err != nil || ok {
		t.Fatalf("expired profile visible: %v %v", ok, err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO jellycompat_device_profiles(token_hash,device_id,profile,expires_at)
 SELECT $1,'backlog-' || i,'{}'::jsonb,$2 FROM generate_series(1,2501) AS i`, deviceProfileTokenHash(token), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if deleted, err := cleanupExpiredDeviceProfiles(t.Context(), store); err != nil || deleted < 2502 {
		t.Fatalf("backlog deleted=%d err=%v", deleted, err)
	}
	var remaining int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM jellycompat_device_profiles WHERE token_hash=$1 AND expires_at<=$2`, deviceProfileTokenHash(token), now).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expired remaining=%d err=%v", remaining, err)
	}
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM jellycompat_device_profiles WHERE token_hash=$1 AND device_id='expired')`, deviceProfileTokenHash(token)).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expired registration remained in database")
	}
	if profile, ok, err := store.GetForDevice(t.Context(), token, "active"); err != nil || !ok || profile.Name != "active" {
		t.Fatalf("cleanup removed active profile: %+v %v %v", profile, ok, err)
	}
}

func TestDeviceProfileExpirationCleanupIsBounded(t *testing.T) {
	now := time.Now()
	store := NewDeviceProfileStore(time.Hour, func() time.Time { return now })
	for i := range 1001 {
		store.Put(fmt.Sprintf("expired-%d", i), DeviceProfile{Name: "expired"})
	}
	now = now.Add(2 * time.Hour)
	deleted, err := store.DeleteExpired(t.Context())
	if err != nil || deleted != 1000 || len(store.profiles) != 1 {
		t.Fatalf("cleanup deleted=%d remaining=%d err=%v", deleted, len(store.profiles), err)
	}
}
