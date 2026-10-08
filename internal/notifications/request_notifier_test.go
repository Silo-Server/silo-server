package notifications

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Silo-Server/silo-server/internal/requests"
)

type fakeFulfillmentBackend struct {
	disabled    map[string]bool
	deliveries  []Delivery
	channelPost int
}

func (f *fakeFulfillmentBackend) PostServerChannelRequestEvent(context.Context, string, RequestEventInfo) {
	f.channelPost++
}

func (f *fakeFulfillmentBackend) notificationsEnabled(_ context.Context, profileID string) (bool, error) {
	return !f.disabled[profileID], nil
}

func (f *fakeFulfillmentBackend) fulfilledDelivered(context.Context, requests.Follower, string) (bool, error) {
	return false, nil
}

func (f *fakeFulfillmentBackend) dispatchFulfilled(_ context.Context, delivery Delivery) error {
	f.deliveries = append(f.deliveries, delivery)
	return nil
}

func fulfilledRequest(followers ...requests.Follower) requests.Request {
	return requests.Request{
		ID: "req-1", MediaType: requests.MediaTypeMovie, TMDBID: 949, Title: "Heat",
		RequestedByUserID: 1, RequestedByProfileID: "requester", Followers: followers,
	}
}

func TestNotifyFulfilledTellsRequesterAndFollowers(t *testing.T) {
	backend := &fakeFulfillmentBackend{disabled: map[string]bool{"muted": true}}
	notifier := &RequestFulfillmentNotifier{backend: backend}

	err := notifier.NotifyFulfilled(context.Background(), fulfilledRequest(
		requests.Follower{UserID: 2, ProfileID: "follower"},
		requests.Follower{UserID: 1, ProfileID: "requester"}, // a leftover follow by the requester
		requests.Follower{UserID: 3, ProfileID: "muted"},
	), "movie-tmdb-949")
	if err != nil {
		t.Fatalf("NotifyFulfilled: %v", err)
	}
	if len(backend.deliveries) != 2 {
		t.Fatalf("deliveries = %+v, want the requester and the one unmuted follower", backend.deliveries)
	}
	requester, follower := backend.deliveries[0], backend.deliveries[1]
	if requester.ProfileID != "requester" || parseRequestFlags(requester.ReasonFlags).Follower {
		t.Fatalf("first delivery = %+v, want the requester's own copy", requester)
	}
	if follower.ProfileID != "follower" || follower.UserID != 2 || !parseRequestFlags(follower.ReasonFlags).Follower {
		t.Fatalf("second delivery = %+v, want the follower's copy marked as such", follower)
	}
	if flags := parseRequestFlags(follower.ReasonFlags); flags.RequestID != "req-1" || flags.TMDBID != 949 {
		t.Fatalf("follower flags = %+v, want the request identity", flags)
	}
	// The server-wide post waits for AnnounceFulfilled, which the caller runs
	// once the request is stamped, so a retried delivery never repeats it.
	if backend.channelPost != 0 {
		t.Fatalf("channel posts after NotifyFulfilled = %d, want none", backend.channelPost)
	}
	notifier.AnnounceFulfilled(context.Background(), fulfilledRequest())
	if backend.channelPost != 1 {
		t.Fatalf("channel posts after AnnounceFulfilled = %d, want 1", backend.channelPost)
	}
}

func TestFulfilledCopyForFollowers(t *testing.T) {
	requester := DeliveryRow{Delivery: Delivery{Type: DeliveryTypeRequestFulfilled, ReasonFlags: []byte(`{"request_id":"req-1"}`)}}
	follower := DeliveryRow{Delivery: Delivery{Type: DeliveryTypeRequestFulfilled, ReasonFlags: []byte(`{"request_id":"req-1","follower":true}`)}}

	if got := BuildNotificationDisplay(requester); got.Title != "Your request is now available" {
		t.Fatalf("requester title = %q", got.Title)
	}
	if got := BuildNotificationDisplay(follower); got.Title != followedTitleAvailable || got.Body == "Your media request has arrived in the library." {
		t.Fatalf("follower display = %+v, want copy that does not claim the request", got)
	}
	if got := requestLine(follower); got != followedTitleAvailable {
		t.Fatalf("follower email line = %q", got)
	}
	if got := discordEmbedAuthorLine(follower); got != "Now available on Silo" {
		t.Fatalf("follower Discord author = %q", got)
	}
}

// Two accounts' legacy "default" profiles, one the requester and one a
// follower, each get exactly one request.fulfilled delivery, on their own
// account, and only their own account's devices are pushed. A second pass is
// deduped per account.
func TestNotifyFulfilledDeliversOncePerAccountForSharedProfileID(t *testing.T) {
	p := inboxPageDB(t)
	ctx := t.Context()
	if _, err := p.Exec(ctx, `
		CREATE TABLE push_devices (LIKE public.push_devices INCLUDING ALL);
		CREATE TABLE push_delivery_attempts (LIKE public.push_delivery_attempts INCLUDING ALL);
		INSERT INTO push_devices
			(id, user_id, profile_id, device_id, platform, provider, apns_environment, apns_topic,
			 apns_token_ciphertext, apns_token_hash, server_device_id, push_mode, enabled)
		VALUES
			('device-account-1', 1, 'default', 'local-1', 'apple', 'silo_relay', 'sandbox',
			 'org.siloserver.silo', 'ciphertext', 'hash-1', 'server-1', 'private_push', true),
			('device-account-2', 2, 'default', 'local-2', 'apple', 'silo_relay', 'sandbox',
			 'org.siloserver.silo', 'ciphertext', 'hash-2', 'server-2', 'private_push', true)`); err != nil {
		t.Fatalf("create push tables: %v", err)
	}
	catalogItem := seedAccessCatalog(t, p).series
	system := &System{
		pool:           p,
		Settings:       NewSettings(mapSettingReader{SettingApplePushDeliveryEnabled: "true"}),
		Deliveries:     NewDeliveryRepository(p),
		Preferences:    NewPreferencesRepository(p),
		pushDeviceRepo: NewPushDeviceRepository(p),
		dispatcher:     NewMultiDispatcher(),
		scopes:         scopeByProfile{"default": {}},
		logger:         slog.New(slog.DiscardHandler),
	}
	notifier := NewRequestFulfillmentNotifier(system)
	req := fulfilledRequest(requests.Follower{UserID: 2, ProfileID: "default"})
	req.RequestedByProfileID = "default"

	for range 2 {
		if err := notifier.NotifyFulfilled(ctx, req, catalogItem); err != nil {
			t.Fatalf("NotifyFulfilled: %v", err)
		}
	}

	rows, err := p.Query(ctx, `
		SELECT d.id, d.user_id, d.reason_flags, a.push_device_id
		FROM notification_deliveries d
		LEFT JOIN push_delivery_attempts a ON a.notification_delivery_id = d.id
		WHERE d.type = $1
		ORDER BY d.user_id, a.push_device_id`, DeliveryTypeRequestFulfilled)
	if err != nil {
		t.Fatalf("query deliveries: %v", err)
	}
	type got struct {
		userID   int
		follower bool
		device   *string
	}
	var out []got
	for rows.Next() {
		var id string
		var row got
		var flags []byte
		if err := rows.Scan(&id, &row.userID, &flags, &row.device); err != nil {
			t.Fatalf("scan delivery: %v", err)
		}
		row.follower = parseRequestFlags(flags).Follower
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("deliveries with push attempts = %+v, want one per account", out)
	}
	for i, want := range []struct {
		userID   int
		follower bool
		device   string
	}{{1, false, "device-account-1"}, {2, true, "device-account-2"}} {
		if out[i].userID != want.userID || out[i].follower != want.follower || out[i].device == nil || *out[i].device != want.device {
			t.Fatalf("delivery %d = {user %d follower %v device %v}, want %+v", i, out[i].userID, out[i].follower, out[i].device, want)
		}
	}
}
