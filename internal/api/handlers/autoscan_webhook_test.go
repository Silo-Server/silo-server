package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

const testWebhookToken = "wh_test_token_abcdef123456"

func webhookSource(enabled bool) autoscan.Source {
	return autoscan.Source{
		ID:           "src-1",
		PluginID:     autoscan.BuiltinArrWebhookPluginID,
		CapabilityID: autoscan.BuiltinArrWebhookCapabilityID,
		Enabled:      enabled,
		DeliveryMode: autoscan.DeliveryModeWebhook,
		SourceConfig: map[string]string{"webhook_provider": "auto"},
	}
}

// webhookStore returns a store whose resolveTokenFn accepts testWebhookToken.
func webhookStore(source autoscan.Source, settingsEnabled bool) *fakeAutoscanStore {
	return &fakeAutoscanStore{
		getSettingsFn: func() (autoscan.Settings, error) {
			return autoscan.Settings{Enabled: settingsEnabled, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60}, nil
		},
		resolveTokenFn: func(token string) (autoscan.Source, autoscan.WebhookEndpoint, error) {
			if token == testWebhookToken {
				return source, autoscan.WebhookEndpoint{SourceID: source.ID}, nil
			}
			return autoscan.Source{}, autoscan.WebhookEndpoint{}, autoscan.ErrNotFound
		},
	}
}

func newWebhookDeliveryRequest(token, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/autoscan/webhooks/"+token, strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("token", token)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}

const sonarrDownloadBody = `{
	"eventType": "Download",
	"series": {"path": "/data/tv/Show"},
	"episodeFile": {"path": "/data/tv/Show/Season 01/e01.mkv"}
}`

func TestWebhookDeliveryIngestsAndAccepts(t *testing.T) {
	store := webhookStore(webhookSource(true), true)
	svc := &fakeAutoscanTriggerer{ingestResult: autoscan.IngestResult{Enqueued: 1}}
	h := NewAutoscanHandler(store, svc)

	rec := httptest.NewRecorder()
	h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken, sonarrDownloadBody))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	if len(svc.ingested) != 1 {
		t.Fatalf("ingested = %+v, want one call", svc.ingested)
	}
	in := svc.ingested[0]
	if in.SourceID != "src-1" || in.ProviderEventType != "Download" || len(in.Changes) != 1 {
		t.Fatalf("unexpected ingest input: %+v", in)
	}
	if in.Changes[0].SourcePath != "/data/tv/Show/Season 01/e01.mkv" {
		t.Fatalf("change path = %q", in.Changes[0].SourcePath)
	}
	// IngestChanges stamps last_received_at in the statement that durably
	// accepts the delivery, so the handler must not stamp it a second time.
	if len(store.touchedSources) != 0 {
		t.Fatalf("handler must leave the stamp to durable acceptance, touched = %+v", store.touchedSources)
	}
}

func TestWebhookDeliveryUnknownTokenIs404(t *testing.T) {
	store := webhookStore(webhookSource(true), true)
	svc := &fakeAutoscanTriggerer{}
	h := NewAutoscanHandler(store, svc)

	rec := httptest.NewRecorder()
	h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest("bogus-token", sonarrDownloadBody))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if len(svc.ingested) != 0 {
		t.Fatalf("unknown token must not ingest, got %+v", svc.ingested)
	}
}

func TestWebhookDeliveryTestEventIsAcceptedNoOp(t *testing.T) {
	store := webhookStore(webhookSource(true), true)
	svc := &fakeAutoscanTriggerer{}
	h := NewAutoscanHandler(store, svc)

	rec := httptest.NewRecorder()
	h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken,
		`{"eventType": "Test", "series": {"path": "C:\\testpath"}}`))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if len(svc.ingested) != 0 {
		t.Fatalf("test event must not ingest, got %+v", svc.ingested)
	}
	if len(store.touchedSources) != 1 {
		t.Fatalf("test event must stamp last_received_at, touched = %+v", store.touchedSources)
	}
}

func TestWebhookDeliveryUnknownEventTypeIsAcceptedNoOp(t *testing.T) {
	store := webhookStore(webhookSource(true), true)
	svc := &fakeAutoscanTriggerer{}
	h := NewAutoscanHandler(store, svc)

	rec := httptest.NewRecorder()
	h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken,
		`{"eventType": "Health", "series": {"path": "/data/tv/Show"}}`))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if len(svc.ingested) != 0 {
		t.Fatalf("unknown event must not ingest, got %+v", svc.ingested)
	}
	if len(store.touchedSources) != 1 {
		t.Fatalf("a delivery to an enabled source must stamp last_received_at, touched = %+v", store.touchedSources)
	}
}

// The setup instructions tell the operator to press Test in Sonarr/Radarr, and
// that can happen before the source or Autoscan is switched on. The wiring
// check must still show up as a delivery.
func TestWebhookDeliveryTestEventStampsWhileDisabled(t *testing.T) {
	for name, tc := range map[string]struct {
		sourceEnabled   bool
		settingsEnabled bool
	}{
		"source disabled":   {sourceEnabled: false, settingsEnabled: true},
		"autoscan disabled": {sourceEnabled: true, settingsEnabled: false},
	} {
		t.Run(name, func(t *testing.T) {
			store := webhookStore(webhookSource(tc.sourceEnabled), tc.settingsEnabled)
			svc := &fakeAutoscanTriggerer{}
			h := NewAutoscanHandler(store, svc)

			rec := httptest.NewRecorder()
			h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken,
				`{"eventType": "Test", "series": {"path": "C:\\testpath"}}`))

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", rec.Code)
			}
			if len(svc.ingested) != 0 {
				t.Fatalf("test event must not ingest, got %+v", svc.ingested)
			}
			if len(store.touchedSources) != 1 || store.touchedSources[0] != "src-1" {
				t.Fatalf("test event must stamp last_received_at while disabled, touched = %+v", store.touchedSources)
			}
		})
	}
}

func TestWebhookDeliveryDisabledSourceIsAcceptedNoOp(t *testing.T) {
	for name, tc := range map[string]struct {
		sourceEnabled   bool
		settingsEnabled bool
	}{
		"source disabled":   {sourceEnabled: false, settingsEnabled: true},
		"autoscan disabled": {sourceEnabled: true, settingsEnabled: false},
	} {
		t.Run(name, func(t *testing.T) {
			store := webhookStore(webhookSource(tc.sourceEnabled), tc.settingsEnabled)
			svc := &fakeAutoscanTriggerer{}
			h := NewAutoscanHandler(store, svc)

			rec := httptest.NewRecorder()
			h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken, sonarrDownloadBody))

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", rec.Code)
			}
			if len(svc.ingested) != 0 {
				t.Fatalf("disabled state must not ingest, got %+v", svc.ingested)
			}
			// The delivery is dropped, so "Last delivery" must not claim it.
			if len(store.touchedSources) != 0 {
				t.Fatalf("dropped delivery must not stamp last_received_at, touched = %+v", store.touchedSources)
			}
		})
	}
}

func TestWebhookDeliveryOversizedBodyIs413(t *testing.T) {
	store := webhookStore(webhookSource(true), true)
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	big := `{"eventType": "Download", "pad": "` + strings.Repeat("x", maxWebhookBodyBytes+1) + `"}`
	rec := httptest.NewRecorder()
	h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken, big))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestWebhookDeliveryDurableAcceptanceFailureIs500(t *testing.T) {
	store := webhookStore(webhookSource(true), true)
	svc := &fakeAutoscanTriggerer{ingestErr: context.DeadlineExceeded}
	h := NewAutoscanHandler(store, svc)

	rec := httptest.NewRecorder()
	h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken, sonarrDownloadBody))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the delivery could not be persisted", rec.Code)
	}
	if store.webhookErrs["src-1"] == "" {
		t.Fatal("ingest failure must be recorded on the endpoint")
	}
	if len(store.touchedSources) != 0 {
		t.Fatalf("a delivery answered with 500 must not stamp last_received_at, touched = %+v", store.touchedSources)
	}
}

func TestWebhookDeliveryPendingRetryIsAccepted(t *testing.T) {
	store := webhookStore(webhookSource(true), true)
	svc := &fakeAutoscanTriggerer{ingestResult: autoscan.IngestResult{Pending: true}}
	h := NewAutoscanHandler(store, svc)

	rec := httptest.NewRecorder()
	h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken, sonarrDownloadBody))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 for a durably queued retry", rec.Code)
	}
}

// recordingLogHandler captures every log record produced during a test.
type recordingLogHandler struct {
	mu      sync.Mutex
	entries []string
}

func (h *recordingLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingLogHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" ")
		b.WriteString(a.String())
		return true
	})
	h.mu.Lock()
	h.entries = append(h.entries, b.String())
	h.mu.Unlock()
	return nil
}
func (h *recordingLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingLogHandler) WithGroup(string) slog.Handler      { return h }

func TestWebhookDeliveryNeverLogsToken(t *testing.T) {
	recorder := &recordingLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(recorder))
	defer slog.SetDefault(prev)

	store := webhookStore(webhookSource(true), true)
	// Force both the failure and success logging paths.
	for _, svc := range []*fakeAutoscanTriggerer{
		{ingestErr: context.DeadlineExceeded},
		{ingestResult: autoscan.IngestResult{Enqueued: 1}},
	} {
		h := NewAutoscanHandler(store, svc)
		rec := httptest.NewRecorder()
		h.HandleWebhookDelivery(rec, newWebhookDeliveryRequest(testWebhookToken, sonarrDownloadBody))
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, entry := range recorder.entries {
		if strings.Contains(entry, testWebhookToken) {
			t.Fatalf("log entry leaks the webhook token: %s", entry)
		}
		if strings.Contains(entry, "/data/tv/Show/Season 01") {
			t.Fatalf("log entry leaks payload content: %s", entry)
		}
	}
}

func TestResolveDeliveryMode(t *testing.T) {
	builtin := autoscan.BuiltinArrWebhookPluginID
	builtinCap := autoscan.BuiltinArrWebhookCapabilityID
	cases := []struct {
		name      string
		requested string
		pluginID  string
		capID     string
		want      string
		wantErr   bool
	}{
		{"plugin defaults to poll", "", "sonarr", "arr", autoscan.DeliveryModePoll, false},
		{"builtin defaults to webhook", "", builtin, builtinCap, autoscan.DeliveryModeWebhook, false},
		{"plugin poll ok", "poll", "sonarr", "arr", autoscan.DeliveryModePoll, false},
		{"builtin webhook ok", "webhook", builtin, builtinCap, autoscan.DeliveryModeWebhook, false},
		{"plugin webhook rejected", "webhook", "sonarr", "arr", "", true},
		{"builtin poll rejected", "poll", builtin, builtinCap, "", true},
		{"garbage rejected", "carrier-pigeon", "sonarr", "arr", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveDeliveryMode(tc.requested, tc.pluginID, tc.capID)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- Admin endpoint management ---

func TestSourceResponsesOmitWebhookURLWhenRevealFails(t *testing.T) {
	source := webhookSource(true)
	store := &fakeAutoscanStore{
		listSourcesFn: func() ([]autoscan.Source, error) { return []autoscan.Source{source}, nil },
		listWebhooksFn: func() ([]autoscan.WebhookEndpoint, error) {
			return []autoscan.WebhookEndpoint{{SourceID: source.ID, SecretSuffix: "def456"}}, nil
		},
		revealTokenFn: func(string) (string, error) { return "", context.DeadlineExceeded },
	}
	h := NewAutoscanHandler(store, &fakeAutoscanTriggerer{})

	rec := httptest.NewRecorder()
	h.HandleListSources(rec, newAutoscanRequest(http.MethodGet, "/admin/autoscan/sources", "", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "webhook_url") {
		t.Fatalf("reveal failure must omit webhook_url, got %s", body)
	}
	if !strings.Contains(body, `"webhook_configured":true`) {
		t.Fatalf("status fields must survive reveal failure: %s", body)
	}
}
