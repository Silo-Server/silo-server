package notifications

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotificationDestinationTestSends(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, item := range []struct{ file, table string }{
		{"20260611120000_notification_webhooks.sql", "notification_webhooks"},
		{"20260612020209_server_notification_channels.sql", "notification_server_channels"},
	} {
		data, err := os.ReadFile("../../migrations/sql/" + item.file)
		if err != nil {
			t.Fatal(err)
		}
		_, ddl, ok := strings.Cut(string(data), "CREATE TABLE public."+item.table+" (")
		if !ok {
			t.Fatal("missing table")
		}
		ddl, _, ok = strings.Cut(ddl, "\n);")
		if !ok {
			t.Fatal("missing terminator")
		}
		if _, err = pool.Exec(ctx, "CREATE TEMP TABLE "+item.table+" ("+ddl+"\n);"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE notification_webhooks ADD COLUMN notify_requests boolean NOT NULL DEFAULT false, ADD COLUMN revision bigint NOT NULL DEFAULT 1;
 ALTER TABLE notification_server_channels ADD COLUMN notify_new_audiobooks boolean NOT NULL DEFAULT true, ADD COLUMN notify_new_ebooks boolean NOT NULL DEFAULT true;`); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	encrypt := func(value, aad string) string {
		t.Helper()
		v, err := cipher.Encrypt(value, aad)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	url := "https://destination.example.test/webhook"
	if _, err = pool.Exec(ctx, `INSERT INTO notification_webhooks (id,user_id,profile_id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,consecutive_failures) VALUES ('hook',1,'owner','test','generic',$1,'destination.example.test',$2,2)`, encrypt(url, webhookURLAAD("hook")), encrypt("test-secret", webhookSecretAAD("hook"))); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO notification_server_channels (id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,created_by_user_id,consecutive_failures) VALUES ('channel','test','generic',$1,'destination.example.test',$2,1,2)`, encrypt(url, serverChannelURLAAD("channel")), encrypt("test-secret", serverChannelSecretAAD("channel"))); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: relayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.String() != url {
			t.Fatalf("unexpected send: %s %s", req.Method, req.URL)
		}
		var payload struct {
			Test bool `json:"test"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || !payload.Test {
			t.Fatalf("missing test marker: %+v %v", payload, err)
		}
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"30"}}, Body: io.NopCloser(strings.NewReader("private provider detail"))}, nil
	})}
	settings := NewSettings(mapSettingReader{SettingWebhooksEnabled: "true"})
	hooks := NewWebhookRepository(pool)
	channels := NewServerChannelRepository(pool)
	hookBefore, err := hooks.GetByID(ctx, "owner", "hook")
	if err != nil {
		t.Fatal(err)
	}
	channelBefore, err := channels.GetByID(ctx, "channel")
	if err != nil {
		t.Fatal(err)
	}
	webhookSvc := newWebhookService(hooks, cipher, settings, &webhookSender{cipher: cipher, client: client})
	channelSvc := newServerChannelService(channels, cipher, settings, &serverChannelSender{cipher: cipher, client: client})
	for _, send := range []func() (*WebhookTestResult, error){
		func() (*WebhookTestResult, error) { return webhookSvc.Test(ctx, "owner", "hook") },
		func() (*WebhookTestResult, error) { return channelSvc.Test(ctx, "channel") },
	} {
		before := calls
		result, err := send()
		if err != nil || result.OK || result.HTTPStatus != 429 || result.Message != "429 Too Many Requests" || calls != before+1 {
			t.Fatalf("send: %+v %v calls=%d", result, err, calls)
		}
	}
	if _, err = webhookSvc.Test(ctx, "other", "hook"); !errors.Is(err, ErrWebhookNotFound) || calls != 2 {
		t.Fatalf("cross-profile send: %v calls=%d", err, calls)
	}
	hookAfter, err := hooks.GetByID(ctx, "owner", "hook")
	if err != nil {
		t.Fatal(err)
	}
	channelAfter, err := channels.GetByID(ctx, "channel")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hookBefore, hookAfter) || !reflect.DeepEqual(channelBefore, channelAfter) {
		t.Fatal("test send changed destination metadata/counters/watermark")
	}
}
