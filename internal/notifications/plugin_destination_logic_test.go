package notifications

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// The failure classification docs/architecture/notifications.md states:
// only a plugin the host couldn't reach keeps the destination enabled when
// the schedule runs out. A timeout, the send's own included, and any status
// the plugin returned are ordinary failures.
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
		// keepEnabled: the plugin couldn't be reached, so an exhausted
		// schedule drops the message without disabling the destination.
		keepEnabled bool
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
		// No enabled installation, or the client wasn't acquired in time:
		// Connect wraps both in ErrPluginChannelUnavailable.
		{name: "plugin gone", err: fmt.Errorf("%w: not installed", ErrPluginChannelUnavailable), want: pluginSendRetry, keepEnabled: true},
		{name: "start timed out", err: fmt.Errorf("%w: %w", ErrPluginChannelUnavailable, context.DeadlineExceeded), want: pluginSendRetry, keepEnabled: true},
		// The connection failed or closed: the plugin process exited mid-call.
		{name: "transport unavailable", err: status.Error(codes.Unavailable, "error reading from server: EOF"), want: pluginSendRetry, keepEnabled: true},
		{name: "connection error", err: errors.New("connection refused"), want: pluginSendRetry, keepEnabled: true},
		// The send's 15 seconds ran out, or the plugin returned a timeout or
		// cancellation itself: ordinary failures.
		{name: "send timed out", err: status.Error(codes.DeadlineExceeded, "context deadline exceeded"), want: pluginSendRetry},
		{name: "send timed out, raw", err: context.DeadlineExceeded, want: pluginSendRetry},
		{name: "plugin canceled", err: status.Error(codes.Canceled, "canceled"), want: pluginSendRetry},
		{name: "plugin error status", err: status.Error(codes.Internal, "boom"), want: pluginSendRetry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pluginSendResultOf(tc.resp, tc.err)
			if got.outcome != tc.want || got.retryAfter != tc.retryAfter || got.keepEnabled != tc.keepEnabled {
				t.Fatalf("pluginSendResultOf() = %+v, want outcome %v retryAfter %v keepEnabled %v", got, tc.want, tc.retryAfter, tc.keepEnabled)
			}
		})
	}
	if pluginAttemptMaxAge != 36*time.Hour {
		t.Fatalf("pluginAttemptMaxAge = %s, want half again the 24-hour schedule", pluginAttemptMaxAge)
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
		// A plugin update dropped "server"; the owner can't see it, so an
		// edit mustn't keep sending it to validation.
		{name: "stored field the schema dropped", stored: map[string]any{"topic": "alerts", "server": "old"}, write: map[string]any{"priority": float64(1)},
			want: map[string]any{"topic": "alerts", "priority": float64(1)}},
		{name: "undeclared field", write: map[string]any{"server_url": "http://10.0.0.1"}, wantErr: true},
		{name: "clearing a plain field", stored: stored, clear: []string{"topic"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergePluginSettings(channel, pluginSettings{Values: tc.stored}, tc.write, tc.clear)
			if tc.wantErr {
				if !errors.Is(err, ErrPluginDestinationInvalid) {
					t.Fatalf("mergePluginSettings() error = %v, want ErrPluginDestinationInvalid", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got.Values, tc.want) {
				t.Fatalf("mergePluginSettings() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}

	// "api_key" was a secret before a plugin upgrade renamed it; the current
	// schema doesn't declare it, so it stays as hidden as it was.
	public, set := publicPluginSettings(channel, pluginSettings{Values: map[string]any{"topic": "alerts", "token": "s3cret", "api_key": "old"}})
	if !reflect.DeepEqual(public, map[string]any{"topic": "alerts"}) || !reflect.DeepEqual(set, []string{"token"}) {
		t.Fatalf("publicPluginSettings() = %v, %v; want only the declared public field, and the secret named", public, set)
	}
}

// A secret saved under one manifest stays write-only when a plugin upgrade
// keeps the key but drops its secret marking, until the owner replaces it.
func TestPluginSettingsSavedSecretOutlivesItsMarking(t *testing.T) {
	schema := `{"type":"object","properties":{"topic":{"type":"string"},"token":{"type":"string"}}}`
	v1 := PluginChannel{Schema: &pluginv1.ConfigSchema{JsonSchema: schema}, PublicFields: []string{"topic"}, SecretFields: []string{"token"}}
	v2 := PluginChannel{Schema: &pluginv1.ConfigSchema{JsonSchema: schema}, PublicFields: []string{"token", "topic"}}

	saved, err := mergePluginSettings(v1, pluginSettings{}, map[string]any{"topic": "alerts", "token": "s3cret"}, nil)
	if err != nil || !reflect.DeepEqual(saved.WriteOnly, []string{"token"}) {
		t.Fatalf("saved under v1 = %+v, %v; want token recorded as secret", saved, err)
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := decodePluginSettings(raw)
	if err != nil {
		t.Fatal(err)
	}

	public, set := publicPluginSettings(v2, stored)
	if _, leaked := public["token"]; leaked || !reflect.DeepEqual(set, []string{"token"}) {
		t.Fatalf("under v2: settings %v, set %v; want the token kept write-only and shown as set", public, set)
	}
	if got := redactPluginText("bad token s3cret", &v2, stored); strings.Contains(got, "s3cret") {
		t.Fatalf("redactPluginText() = %q, leaks the saved secret", got)
	}
	// An editor that sends the field blank keeps it, still secret.
	kept, err := mergePluginSettings(v2, stored, map[string]any{"token": "", "topic": "news"}, nil)
	if err != nil || kept.Values["token"] != "s3cret" || !reflect.DeepEqual(kept.WriteOnly, []string{"token"}) {
		t.Fatalf("blank edit under v2 = %+v, %v; want the secret kept and still secret", kept, err)
	}
	// Replacing it takes v2's classification.
	replaced, err := mergePluginSettings(v2, kept, map[string]any{"token": "visible"}, nil)
	if err != nil || len(replaced.WriteOnly) != 0 {
		t.Fatalf("replaced under v2 = %+v, %v; want nothing secret", replaced, err)
	}

	// Settings stored without a write_only record don't say which values
	// were secret: under v2, which unmarked token, every value stays hidden.
	// A save records the value it replaced by v2 and keeps the rest hidden.
	for _, raw := range []string{
		`{"topic":"alerts","token":"s3cret"}`,
		`{"settings":{"topic":"alerts","token":"s3cret"}}`,
		`{"settings":{"topic":"alerts","token":"s3cret"},"secret_keys":["token"]}`,
	} {
		legacy, err := decodePluginSettings([]byte(raw))
		if err != nil || legacy.Values["token"] != "s3cret" {
			t.Fatalf("decode %s = %+v, %v", raw, legacy, err)
		}
		public, set := publicPluginSettings(v2, legacy)
		if len(public) != 0 || !reflect.DeepEqual(set, []string{"token", "topic"}) {
			t.Fatalf("%s under v2: settings %v, set %v; want every value write-only", raw, public, set)
		}
		if got := redactPluginText("bad token s3cret", &v2, legacy); strings.Contains(got, "s3cret") {
			t.Fatalf("%s: redactPluginText() = %q, leaks the value", raw, got)
		}
		saved, err := mergePluginSettings(v2, legacy, map[string]any{"topic": "news"}, nil)
		if err != nil || saved.unrecorded || saved.Values["token"] != "s3cret" || !reflect.DeepEqual(saved.WriteOnly, []string{"token"}) {
			t.Fatalf("%s: save under v2 = %+v, %v; want topic public and the untouched token still write-only", raw, saved, err)
		}
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
			ReasonFlags: []byte(`{"next_up":true}`), CreatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)},
		SeriesTitle: "Severance", EpisodeTitle: "Hello, Ms. Cobel", SeasonNumber: &season, EpisodeNumber: &episode,
	}

	message := sender.message(t.Context(), row, false)
	if message.GetTitle() != "Severance" || message.GetBody() != "New episode · S02E05 · Hello, Ms. Cobel" ||
		message.GetUrl() != "https://silo.example.test/item/episode-1" || message.GetImageUrl() != "https://image.example.test/p.jpg" {
		t.Fatalf("message = %v", message)
	}
	// Details carry the event catalog's shape, with the account.
	details := message.GetDetails().AsMap()
	if details["user_id"] != "7" || details["profile_id"] != "p1" || details["series_title"] != "Severance" {
		t.Fatalf("details = %v", details)
	}

	test := sender.message(t.Context(), row, true)
	if test.GetType() != "test" || test.GetDetails() != nil {
		t.Fatalf("test message = %v, want type test and no details", test)
	}
}

// The host enforces the destination form's own rules before the plugin sees
// the settings, and reports each failing field.
func TestCheckPluginSettings(t *testing.T) {
	channels, _ := (&scriptedChannelHost{}).Channels(t.Context())
	channel := channels[0]
	for _, tc := range []struct {
		name     string
		settings map[string]any
		want     []PluginSettingError
	}{
		{name: "valid", settings: map[string]any{"topic": "alerts", "token": "s3cret", "priority": float64(3), "sound": "bell"}},
		{name: "required missing", settings: map[string]any{"token": "s3cret"}, want: []PluginSettingError{{Field: "topic", Code: "required"}}},
		{name: "object refused", settings: map[string]any{"topic": map[string]any{"nested": "x"}}, want: []PluginSettingError{{Field: "topic", Code: "invalid_type"}}},
		{name: "array refused", settings: map[string]any{"topic": "alerts", "token": []any{"a"}}, want: []PluginSettingError{{Field: "token", Code: "invalid_type"}}},
		{name: "integer type", settings: map[string]any{"topic": "alerts", "priority": 2.5}, want: []PluginSettingError{{Field: "priority", Code: "invalid_type"}}},
		{name: "bounds", settings: map[string]any{"topic": "this topic name is longer than thirty-two", "priority": float64(9)},
			want: []PluginSettingError{{Field: "priority", Code: "out_of_range"}, {Field: "topic", Code: "out_of_range"}}},
		{name: "enum", settings: map[string]any{"topic": "alerts", "sound": "siren"}, want: []PluginSettingError{{Field: "sound", Code: "invalid_enum"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPluginSettings(channel, tc.settings)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("checkPluginSettings() = %v, want nil", err)
				}
				return
			}
			invalid, ok := errors.AsType[*PluginDestinationSettingsError](err)
			if !ok || !errors.Is(err, ErrPluginDestinationInvalid) {
				t.Fatalf("checkPluginSettings() = %v, want a settings error", err)
			}
			got := make([]PluginSettingError, 0, len(invalid.Fields))
			for _, field := range invalid.Fields {
				got = append(got, PluginSettingError{Field: field.Field, Code: field.Code})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("fields = %+v, want %+v", got, tc.want)
			}
		})
	}

	// An integer a plugin can't receive exactly is refused, even where the
	// schema sets no bound. 9007199254740993 decodes to ...992, still beyond.
	unbounded := channel
	unbounded.Schema = &pluginv1.ConfigSchema{JsonSchema: `{"type":"object","properties":{"count":{"type":"integer"}}}`}
	unbounded.PublicFields, unbounded.SecretFields = []string{"count"}, nil
	for _, value := range []any{float64(9007199254740993), float64(-9007199254740993), int64(1 << 60)} {
		err := checkPluginSettings(unbounded, map[string]any{"count": value})
		invalid, ok := errors.AsType[*PluginDestinationSettingsError](err)
		if !ok || len(invalid.Fields) != 1 || invalid.Fields[0].Field != "count" || invalid.Fields[0].Code != "out_of_range" {
			t.Fatalf("checkPluginSettings(%v) = %v, want count out_of_range", value, err)
		}
	}
	if err := checkPluginSettings(unbounded, map[string]any{"count": float64(9007199254740991)}); err != nil {
		t.Fatalf("checkPluginSettings(2^53-1) = %v, want nil", err)
	}
	// Too large to carry outranks not being one of a select's options.
	choice := channel
	choice.Schema = &pluginv1.ConfigSchema{JsonSchema: `{"type":"object","properties":{"count":{"type":"integer","enum":[1,2]}}}`}
	choice.PublicFields, choice.SecretFields = []string{"count"}, nil
	err := checkPluginSettings(choice, map[string]any{"count": float64(1 << 53)})
	if invalid, ok := errors.AsType[*PluginDestinationSettingsError](err); !ok || len(invalid.Fields) != 1 || invalid.Fields[0].Code != "out_of_range" {
		t.Fatalf("checkPluginSettings(2^53 on a select) = %v, want one out_of_range", err)
	}
}

// A plugin's text can quote a credential it was given. Every value that
// isn't a declared public field is masked, including a stored secret the
// current schema no longer declares; public values stay readable.
func TestRedactPluginText(t *testing.T) {
	channel := &PluginChannel{PublicFields: []string{"topic"}, SecretFields: []string{"token"}}
	stored := pluginSettings{Values: map[string]any{"topic": "alerts", "token": "tk_live_123", "old_key": "legacy-key-9", "pin": float64(4321), "loud": true}}
	got := redactPluginText("token tk_live_123 rejected for alerts; legacy-key-9 expired; pin 4321; loud true; https://user:pw@example.com", channel, stored)
	for _, leaked := range []string{"tk_live_123", "legacy-key-9", "4321", "user:pw"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("redactPluginText() = %q, leaks %q", got, leaked)
		}
	}
	if !strings.Contains(got, "alerts") || !strings.Contains(got, "loud true") {
		t.Fatalf("redactPluginText() = %q, want public values kept", got)
	}

	// A provider can quote a credential encoded, stray space included, or
	// trimmed. A value too short for its encodings to be telling ("4321") is
	// masked only as written.
	secret := " tk_live_123/+? "
	var forms []string
	for _, value := range []string{secret, strings.TrimSpace(secret)} {
		forms = append(forms, value,
			base64.StdEncoding.EncodeToString([]byte(value)), base64.RawStdEncoding.EncodeToString([]byte(value)),
			base64.URLEncoding.EncodeToString([]byte(value)), base64.RawURLEncoding.EncodeToString([]byte(value)),
			url.QueryEscape(value), url.PathEscape(value), hex.EncodeToString([]byte(value)), strings.ToUpper(hex.EncodeToString([]byte(value))))
	}
	for _, encoded := range forms {
		withSecret := pluginSettings{Values: map[string]any{"token": secret}, WriteOnly: []string{}}
		if got := redactPluginText("auth header "+encoded+" refused", channel, withSecret); strings.Contains(got, encoded) {
			t.Fatalf("redactPluginText() = %q, leaks the encoded secret %q", got, encoded)
		}
	}
	// The stored spelling can be long enough for its encodings to be masked
	// when the trimmed one isn't.
	padded := pluginSettings{Values: map[string]any{"token": "tk_live "}, WriteOnly: []string{}}
	paddedEncoded := base64.StdEncoding.EncodeToString([]byte("tk_live "))
	if got := redactPluginText("auth header "+paddedEncoded+" refused", channel, padded); strings.Contains(got, paddedEncoded) {
		t.Fatalf("redactPluginText() = %q, leaks the encoding of a secret stored with a trailing space", got)
	}
	if got := redactPluginText("code NDMyMQ== at 34333231", channel, stored); !strings.Contains(got, "NDMyMQ==") || !strings.Contains(got, "34333231") {
		t.Fatalf("redactPluginText() = %q, masked encodings of a short value", got)
	}
}

// Plugin text is stored as PostgreSQL text: a NUL byte or invalid UTF-8
// would fail the finalize and resend the message forever.
func TestRedactPluginTextIsStorable(t *testing.T) {
	got := redactPluginText("bad\x00 gate\xff\xfeway\x1b[31m\nline two\t"+strings.Repeat("é", 400), nil)
	if strings.ContainsRune(got, 0) || !utf8.ValidString(got) || strings.ContainsRune(got, 0x1b) {
		t.Fatalf("redactPluginText() = %q, not storable", got)
	}
	if !strings.HasPrefix(got, "bad gateway[31m\nline two\t") || utf8.RuneCountInString(got) != maxPluginTextRunes {
		t.Fatalf("redactPluginText() = %q (%d runes), want control bytes dropped, newline and tab kept, %d runes",
			got, utf8.RuneCountInString(got), maxPluginTextRunes)
	}
}

// A stored schema that doesn't compile is a permanent condition: the
// channel counts as not provided, which the API answers without Retry-After.
func TestCheckPluginSettingsUncompilableSchema(t *testing.T) {
	channel := PluginChannel{Schema: &pluginv1.ConfigSchema{JsonSchema: `{"type":"object","properties":{"topic":{"type":"string","pattern":"("}}}`}}
	if err := checkPluginSettings(channel, map[string]any{"topic": "x"}); !errors.Is(err, ErrPluginChannelNotProvided) {
		t.Fatalf("checkPluginSettings() = %v, want ErrPluginChannelNotProvided", err)
	}
}
