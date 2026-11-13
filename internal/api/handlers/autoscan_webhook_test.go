package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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

func newWebhookDeliveryRequest(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/autoscan/webhooks/delivery", strings.NewReader(body))
}

const sonarrDownloadBody = `{
	"eventType": "Download",
	"series": {"path": "/data/tv/Show"},
	"episodeFile": {"path": "/data/tv/Show/Season 01/e01.mkv"}
}`

const sonarrTestBody = `{"eventType": "Test", "series": {"path": "C:\\testpath"}}`

// TestDeliverAutoscanWebhook covers the public delivery seam: which failures
// reach the sender, when a delivery is ingested, and when it stamps the
// source's "Last delivery" (only deliveries the source took, plus provider
// Test events in any state so setup can be checked before enabling).
func TestDeliverAutoscanWebhook(t *testing.T) {
	cases := []struct {
		name            string
		token           string
		body            string
		sourceEnabled   bool
		settingsEnabled bool
		ingestResult    autoscan.IngestResult
		ingestErr       error
		wantStatus      int // 0 means accepted (nil error)
		wantIngest      bool
		wantTouched     bool
		wantRecordedErr bool
	}{
		{name: "download ingests", token: testWebhookToken, body: sonarrDownloadBody, sourceEnabled: true, settingsEnabled: true,
			ingestResult: autoscan.IngestResult{Enqueued: 1}, wantIngest: true},
		{name: "pending retry is accepted", token: testWebhookToken, body: sonarrDownloadBody, sourceEnabled: true, settingsEnabled: true,
			ingestResult: autoscan.IngestResult{Pending: true}, wantIngest: true},
		{name: "unknown token", token: "bogus-token", body: sonarrDownloadBody, sourceEnabled: true, settingsEnabled: true,
			wantStatus: http.StatusNotFound},
		{name: "test event stamps", token: testWebhookToken, body: sonarrTestBody, sourceEnabled: true, settingsEnabled: true,
			wantTouched: true},
		{name: "test event stamps while source disabled", token: testWebhookToken, body: sonarrTestBody, settingsEnabled: true,
			wantTouched: true},
		{name: "test event stamps while autoscan disabled", token: testWebhookToken, body: sonarrTestBody, sourceEnabled: true,
			wantTouched: true},
		{name: "unknown event type stamps without ingest", token: testWebhookToken,
			body: `{"eventType": "Health", "series": {"path": "/data/tv/Show"}}`, sourceEnabled: true, settingsEnabled: true,
			wantTouched: true},
		{name: "source disabled drops without stamp", token: testWebhookToken, body: sonarrDownloadBody, settingsEnabled: true},
		{name: "autoscan disabled drops without stamp", token: testWebhookToken, body: sonarrDownloadBody, sourceEnabled: true},
		{name: "oversized body", token: testWebhookToken,
			body:          `{"eventType": "Download", "pad": "` + strings.Repeat("x", maxWebhookBodyBytes+1) + `"}`,
			sourceEnabled: true, settingsEnabled: true, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "durable acceptance failure", token: testWebhookToken, body: sonarrDownloadBody, sourceEnabled: true, settingsEnabled: true,
			ingestErr: context.DeadlineExceeded, wantStatus: http.StatusInternalServerError, wantIngest: true, wantRecordedErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := webhookStore(webhookSource(tc.sourceEnabled), tc.settingsEnabled)
			svc := &fakeAutoscanTriggerer{ingestResult: tc.ingestResult, ingestErr: tc.ingestErr}
			h := NewAutoscanHandler(store, svc)

			err := h.DeliverAutoscanWebhook(httptest.NewRecorder(), newWebhookDeliveryRequest(tc.body), tc.token)

			gotStatus := 0
			if err != nil {
				failure, ok := errors.AsType[*AutoscanDeliveryFailure](err)
				if !ok {
					t.Fatalf("err = %T %v, want *AutoscanDeliveryFailure", err, err)
				}
				gotStatus = failure.Status
			}
			if gotStatus != tc.wantStatus {
				t.Fatalf("status = %d, want %d (err %v)", gotStatus, tc.wantStatus, err)
			}
			if tc.wantIngest {
				if len(svc.ingested) != 1 {
					t.Fatalf("ingested = %+v, want one call", svc.ingested)
				}
				in := svc.ingested[0]
				if in.SourceID != "src-1" || in.ProviderEventType != "Download" || len(in.Changes) != 1 ||
					in.Changes[0].SourcePath != "/data/tv/Show/Season 01/e01.mkv" {
					t.Fatalf("unexpected ingest input: %+v", in)
				}
			} else if len(svc.ingested) != 0 {
				t.Fatalf("ingested = %+v, want none", svc.ingested)
			}
			// An ingested delivery is stamped by IngestChanges in the statement
			// that durably accepts it, so the handler must not stamp it again.
			wantTouched := 0
			if tc.wantTouched {
				wantTouched = 1
			}
			if len(store.touchedSources) != wantTouched {
				t.Fatalf("touched = %+v, want %d stamps", store.touchedSources, wantTouched)
			}
			if tc.wantRecordedErr != (store.webhookErrs["src-1"] != "") {
				t.Fatalf("recorded webhook errors = %+v, want recorded=%v", store.webhookErrs, tc.wantRecordedErr)
			}
		})
	}
}

func TestDeliverAutoscanWebhookNeverLogsToken(t *testing.T) {
	recorder := &recordingLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(recorder))
	defer slog.SetDefault(prev)

	store := webhookStore(webhookSource(true), true)
	// Drive the failure, retry, and success logging paths.
	for _, svc := range []*fakeAutoscanTriggerer{
		{ingestErr: context.DeadlineExceeded},
		{ingestResult: autoscan.IngestResult{Pending: true}},
		{ingestResult: autoscan.IngestResult{Enqueued: 1}},
	} {
		h := NewAutoscanHandler(store, svc)
		_ = h.DeliverAutoscanWebhook(httptest.NewRecorder(), newWebhookDeliveryRequest(sonarrDownloadBody), testWebhookToken)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.entries) == 0 {
		t.Fatal("expected delivery log entries")
	}
	for _, entry := range recorder.entries {
		if strings.Contains(entry, testWebhookToken) {
			t.Fatalf("log entry leaks the webhook token: %s", entry)
		}
		if strings.Contains(entry, "/data/tv/Show/Season 01") {
			t.Fatalf("log entry leaks payload content: %s", entry)
		}
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
