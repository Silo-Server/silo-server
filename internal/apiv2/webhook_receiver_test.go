package apiv2

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type webhookUnreadBody struct{ reads int }

func (b *webhookUnreadBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*webhookUnreadBody) Close() error               { return nil }

func TestWebhookReceiverRealDeliveryDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TEMP TABLE webhook_sync_connections (LIKE public.webhook_sync_connections INCLUDING DEFAULTS);
 CREATE TEMP TABLE webhook_sync_event_logs (LIKE public.webhook_sync_event_logs INCLUDING DEFAULTS);
 CREATE TEMP SEQUENCE receiver_log_ids;
 ALTER TABLE webhook_sync_event_logs ALTER COLUMN id SET DEFAULT nextval('receiver_log_ids');
 INSERT INTO webhook_sync_connections(id,user_id,provider,webhook_secret) VALUES('00000000-0000-4000-8000-000000000001',1,'plex','receiver-secret');`)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("synthetic-webhook-receiver-test-key-material"))
	if err != nil {
		t.Fatal(err)
	}
	app := handlers.NewWebhookSyncHandler(webhooksync.NewService(webhooksync.NewRepository(pool, cipher), nil, nil))
	deps := pilotDeps(nil, nil)
	deps.WebhookReceiver = app
	h := newTestHandler(t, deps)
	bridge := chi.NewRouter()
	bridge.Post("/api/v1/webhook-sync/webhooks/{secret}", app.HandleWebhook)
	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	if err := form.WriteField("payload", `{"event":"media.play","Account":{"id":1},"Metadata":{"ratingKey":"1","type":"movie"}}`); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"ignored", payload.String(), 204}, {"malformed", "invalid multipart", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, v2 := range []bool{false, true} {
				path := "/api/v1/webhook-sync/webhooks/receiver-secret"
				var router http.Handler = bridge
				if v2 {
					path = Prefix + "/webhook-sync/webhooks/receiver-secret"
					router = h
				}
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", form.FormDataContentType())
				req.Header.Set("Authorization", "Bearer irrelevant-invalid-token")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("v2=%v: %d %s", v2, rec.Code, rec.Body)
				}
				if v2 && (rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "receiver-secret")) {
					t.Fatal("unsafe response", rec.Header(), rec.Body)
				}
				if tc.status == 204 && rec.Body.Len() != 0 {
					t.Fatal("ack has body")
				}
			}
		})
	}
	var logs int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM webhook_sync_event_logs").Scan(&logs); err != nil || logs != 4 {
		t.Fatal(logs, err)
	}
	var pattern string
	if err := pool.QueryRow(t.Context(), "SELECT attrs->>'path_pattern' FROM webhook_sync_event_logs ORDER BY id DESC LIMIT 1").Scan(&pattern); err != nil || pattern != Prefix+"/webhook-sync/webhooks/{secret}" {
		t.Fatal(pattern, err)
	}
	// Invalid proof is rejected without reading even an unknown-length body.
	unread := new(webhookUnreadBody)
	req := httptest.NewRequest(http.MethodPost, Prefix+"/webhook-sync/webhooks/invalid", unread)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 || unread.reads != 0 {
		t.Fatal(rec.Code, unread.reads)
	}
	// Oversize suffix cannot follow a valid JSON prefix into provider side effects.
	if _, err := pool.Exec(t.Context(), "UPDATE webhook_sync_connections SET provider='jellyfin',last_webhook_received_at=NULL"); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, Prefix+"/webhook-sync/webhooks/receiver-secret", strings.NewReader(`{}`+strings.Repeat(" ", int(webhookDeliveryLimit))))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatal(rec.Code, rec.Body)
	}
	var untouched bool
	if err := pool.QueryRow(t.Context(), "SELECT last_webhook_received_at IS NULL FROM webhook_sync_connections").Scan(&untouched); err != nil || !untouched {
		t.Fatal(untouched, err)
	}
	var status int
	if err := pool.QueryRow(t.Context(), "SELECT http_status FROM webhook_sync_event_logs ORDER BY id DESC LIMIT 1").Scan(&status); err != nil || status != 413 {
		t.Fatal(status, err)
	}
	// Exact body boundary is accepted; one byte over is rejected before parsing.
	for _, delta := range []int{0, 1} {
		body := `{}` + strings.Repeat(" ", int(webhookDeliveryLimit)-2+delta)
		req = httptest.NewRequest(http.MethodPost, Prefix+"/webhook-sync/webhooks/receiver-secret", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		want := 204
		if delta > 0 {
			want = 413
		}
		if rec.Code != want {
			t.Fatal(delta, rec.Code, rec.Body)
		}
	}
	// Multipart cleanup also removes files spilled by an upstream parser.
	if _, err := pool.Exec(t.Context(), "UPDATE webhook_sync_connections SET provider='plex'"); err != nil {
		t.Fatal(err)
	}
	var diskBody bytes.Buffer
	diskForm := multipart.NewWriter(&diskBody)
	if err := diskForm.WriteField("payload", `{"event":"media.play","Account":{"id":1},"Metadata":{"ratingKey":"1","type":"movie"}}`); err != nil {
		t.Fatal(err)
	}
	part, err := diskForm.CreateFormFile("thumb", "test.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), 64)); err != nil {
		t.Fatal(err)
	}
	if err := diskForm.Close(); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, Prefix+"/webhook-sync/webhooks/receiver-secret", bytes.NewReader(diskBody.Bytes()))
	req.Header.Set("Content-Type", diskForm.FormDataContentType())
	if err := req.ParseMultipartForm(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = req.MultipartForm.RemoveAll() })
	diskFile, err := req.MultipartForm.File["thumb"][0].Open()
	if err != nil {
		t.Fatal(err)
	}
	diskPath := diskFile.(*os.File).Name()
	_ = diskFile.Close()
	req.Body = io.NopCloser(bytes.NewReader(diskBody.Bytes()))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatal(rec.Code, rec.Body)
	}
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatal("multipart file remains", err)
	}
	// Rotation immediately makes the old URL invalid, without an ambient-session fallback.
	if _, err := pool.Exec(t.Context(), "UPDATE webhook_sync_connections SET webhook_secret='rotated-secret'"); err != nil {
		t.Fatal(err)
	}
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/webhook-sync/webhooks/receiver-secret", `{}`, bearer(memberToken)), TypeNotFound)
}

func TestWebhookReceiverUnavailable(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/webhook-sync/webhooks/secret", "", nil), TypeDependencyUnavailable)
	rec := do(t, h, http.MethodGet, Prefix+"/webhook-sync/capabilities", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatal(rec.Code, rec.Body)
	}
}

func (f *fakeWebhookManagement) RotateWebhookConnection(context.Context, int, string) (*webhooksync.RotateWebhookResult, error) {
	return &webhooksync.RotateWebhookResult{WebhookURL: "/api/v2/webhook-sync/webhooks/new-secret"}, nil
}
func TestWebhookRotationEmitsV2Receiver(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.WebhookSync = new(fakeWebhookManagement)
	rec := do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/webhook-sync/connections/"+webhookFixtureID+"/webhook/rotate", "", bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), Prefix+"/webhook-sync/webhooks/new-secret") {
		t.Fatal(rec.Code, rec.Body)
	}
}
