package notifications

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestPluginSendResultOf(t *testing.T) {
	outcome := func(o pluginv1.NotificationSendOutcome, retryAfter int32) *pluginv1.SendNotificationResponse {
		return &pluginv1.SendNotificationResponse{Outcome: o, RetryAfterSeconds: retryAfter, Message: "m"}
	}
	for _, tc := range []struct {
		name       string
		resp       *pluginv1.SendNotificationResponse
		err        error
		want       pluginSendOutcome
		retryAfter time.Duration
	}{
		// An empty response acknowledges, as the SDK documents.
		{name: "empty response", resp: &pluginv1.SendNotificationResponse{}, want: pluginSendDelivered},
		{name: "delivered", resp: outcome(pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DELIVERED, 0), want: pluginSendDelivered},
		{name: "retry with a wait", resp: outcome(pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY, 120), want: pluginSendRetry, retryAfter: 2 * time.Minute},
		{name: "retry asks for days", resp: outcome(pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_RETRY, 7*86400), want: pluginSendRetry, retryAfter: maxPluginRetryAfter},
		{name: "failed", resp: outcome(pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_FAILED, 0), want: pluginSendFailed},
		{name: "destination invalid", resp: outcome(pluginv1.NotificationSendOutcome_NOTIFICATION_SEND_OUTCOME_DESTINATION_INVALID, 0), want: pluginSendInvalid},
		// An outcome from a newer SDK drops the message and keeps the destination.
		{name: "unknown outcome", resp: outcome(pluginv1.NotificationSendOutcome(99), 0), want: pluginSendFailed},
		{name: "rpc error", err: errors.New("rpc error: deadline exceeded"), want: pluginSendRetry},
		{name: "plugin gone", err: fmt.Errorf("%w: not installed", ErrPluginChannelUnavailable), want: pluginSendRetry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pluginSendResultOf(tc.resp, tc.err)
			if got.outcome != tc.want || got.retryAfter != tc.retryAfter {
				t.Fatalf("pluginSendResultOf() = %+v, want outcome %v retryAfter %v", got, tc.want, tc.retryAfter)
			}
		})
	}
}

func TestMergePluginSettings(t *testing.T) {
	channel := PluginChannel{Schema: &pluginv1.ConfigSchema{
		JsonSchema: `{"type":"object","properties":{"topic":{"type":"string"},"token":{"type":"string"},"priority":{"type":"integer"}}}`,
		AdminForm: &pluginv1.AdminFormDescriptor{Fields: []*pluginv1.AdminFormField{
			{Key: "topic"}, {Key: "token", Secret: true}, {Key: "priority"},
		}},
	}, PublicFields: []string{"priority", "topic"}, SecretFields: []string{"token"}}
	stored := map[string]any{"topic": "alerts", "token": "s3cret", "priority": float64(3)}
	for _, tc := range []struct {
		name    string
		stored  map[string]any
		write   map[string]any
		clear   []string
		want    map[string]any
		wantErr bool
	}{
		{name: "create", write: map[string]any{"topic": "alerts", "token": "s3cret"}, want: map[string]any{"topic": "alerts", "token": "s3cret"}},
		// The editor never sees the secret, so it sends it blank.
		{name: "blank secret keeps the stored one", stored: stored, write: map[string]any{"topic": "news", "token": ""},
			want: map[string]any{"topic": "news", "token": "s3cret", "priority": float64(3)}},
		{name: "null secret keeps the stored one", stored: stored, write: map[string]any{"token": nil},
			want: stored},
		{name: "new secret replaces it", stored: stored, write: map[string]any{"token": "n3w"},
			want: map[string]any{"topic": "alerts", "token": "n3w", "priority": float64(3)}},
		{name: "null removes a plain field", stored: stored, write: map[string]any{"priority": nil},
			want: map[string]any{"topic": "alerts", "token": "s3cret"}},
		{name: "clear removes a secret", stored: stored, clear: []string{"token"},
			want: map[string]any{"topic": "alerts", "priority": float64(3)}},
		{name: "undeclared field", write: map[string]any{"server_url": "http://10.0.0.1"}, wantErr: true},
		{name: "clearing a plain field", stored: stored, clear: []string{"topic"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergePluginSettings(channel, tc.stored, tc.write, tc.clear)
			if tc.wantErr {
				if !errors.Is(err, ErrPluginDestinationInvalid) {
					t.Fatalf("mergePluginSettings() error = %v, want ErrPluginDestinationInvalid", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergePluginSettings() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}

	// "api_key" was a secret before a plugin upgrade renamed it; the current
	// schema doesn't declare it, so it stays as hidden as it was.
	public, set := publicPluginSettings(channel, map[string]any{"topic": "alerts", "token": "s3cret", "api_key": "old"})
	if !reflect.DeepEqual(public, map[string]any{"topic": "alerts"}) || !reflect.DeepEqual(set, []string{"token"}) {
		t.Fatalf("publicPluginSettings() = %v, %v; want only the declared public field, and the secret named", public, set)
	}
}

func TestPluginDestinationMessage(t *testing.T) {
	season, episode := 2, 5
	series, episodeID := "series-1", "episode-1"
	sender := &pluginDestinationSender{
		linkBase:  func(context.Context) string { return "https://silo.example.test/" },
		posterURL: func(context.Context, string, string) string { return "https://image.example.test/p.jpg" },
	}
	row := DeliveryRow{
		Delivery: Delivery{ID: "d1", UserID: 7, ProfileID: "p1", Type: DeliveryTypeEpisodeAvailable, SeriesID: &series, EpisodeID: &episodeID,
			ReasonFlags: []byte(`{"favorite":true}`), CreatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)},
		SeriesTitle: "Severance", EpisodeTitle: "Hello, Ms. Cobel", SeasonNumber: &season, EpisodeNumber: &episode,
	}

	message := sender.message(t.Context(), row, false)
	if message.GetTitle() != "Severance" || message.GetBody() != "New episode · S02E05 · Hello, Ms. Cobel" ||
		message.GetUrl() != "https://silo.example.test/item/episode-1" || message.GetImageUrl() != "https://image.example.test/p.jpg" {
		t.Fatalf("message = %v", message)
	}
	// Details carry the event catalog's shape, with the account.
	details := message.GetDetails().AsMap()
	if details["user_id"] != float64(7) || details["profile_id"] != "p1" || details["series_title"] != "Severance" {
		t.Fatalf("details = %v", details)
	}

	test := sender.message(t.Context(), row, true)
	if test.GetType() != "test" || test.GetDetails() != nil {
		t.Fatalf("test message = %v, want type test and no details", test)
	}
}
