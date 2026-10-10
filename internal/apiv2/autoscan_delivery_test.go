package apiv2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type autoscanDeliveryFixture struct {
	calls int
	body  string
	fail  error
}

func (f *autoscanDeliveryFixture) DeliverAutoscanWebhook(_ http.ResponseWriter, r *http.Request, token string) error {
	if token != "synthetic-capability" {
		return &handlers.AutoscanDeliveryFailure{Status: 404, Message: "Not found"}
	}
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	data, err := io.ReadAll(r.Body)
	f.body = string(data)
	return err
}
func TestAutoscanDeliveryTypedAdmission(t *testing.T) {
	f := new(autoscanDeliveryFixture)
	h := NewHandler(Dependencies{AutoscanDelivery: f})
	path := Prefix + "/autoscan/webhooks/synthetic-capability"
	body := `{ "eventType" : "Test", "provider_extra" : [1,2] }`
	rec := do(t, h, "POST", path, body, nil)
	if rec.Code != 202 || f.calls != 1 || f.body != body || !strings.Contains(rec.Body.String(), `"status":"accepted"`) {
		t.Fatal(rec.Code, rec.Body.String(), f)
	}
	f.fail = &handlers.AutoscanDeliveryFailure{Status: 500, Message: "Could not durably accept delivery"}
	requireProblem(t, do(t, h, "POST", path, body, nil), TypeInternalError)
	requireProblem(t, do(t, NewHandler(Dependencies{}), "POST", path, body, nil), TypeDependencyUnavailable)
}
func TestAutoscanDeliveryRejectsBeforeBodyAndPreservesBucket(t *testing.T) {
	f := new(autoscanDeliveryFixture)
	h := NewHandler(Dependencies{AutoscanDelivery: f})
	unread := &ingressUnreadBody{}
	r := httptest.NewRequest("POST", Prefix+"/autoscan/webhooks/unknown", unread)
	r.Header.Set("Content-Type", mediaTypeJSON)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeNotFound)
	if unread.read || f.calls != 0 {
		t.Fatal("pre-read provider bytes before capability resolution")
	}
	r = httptest.NewRequest("POST", Prefix+"/autoscan/webhooks/synthetic-capability", unread)
	r.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeUnsupportedMediaType)
	bucket := ""
	deps := Dependencies{AutoscanDelivery: f, BucketRateLimit: func(name string) func(http.Handler) http.Handler {
		bucket = name
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"Too many requests."}`))
			})
		}
	}}
	rec = do(t, NewHandler(deps), "POST", Prefix+"/autoscan/webhooks/synthetic-capability", `{}`, nil)
	requireProblem(t, rec, TypeRateLimited)
	if bucket != "autoscan_webhook" || f.calls != 0 || rec.Header().Get("Retry-After") != "3" {
		t.Fatal(bucket, f.calls, rec.Header())
	}
}

// deliveryStore is the slice of the autoscan repository the real delivery
// handler touches. The embedded nil repository satisfies the rest of the
// handler's store surface; reaching any of it would panic and fail the test.
type deliveryStore struct {
	*autoscan.Repository
	source   autoscan.Source
	settings autoscan.Settings
	touched  []string
}

func (s *deliveryStore) ResolveWebhookToken(_ context.Context, token string) (autoscan.Source, autoscan.WebhookEndpoint, error) {
	if token != "synthetic-capability" {
		return autoscan.Source{}, autoscan.WebhookEndpoint{}, autoscan.ErrNotFound
	}
	return s.source, autoscan.WebhookEndpoint{SourceID: s.source.ID}, nil
}
func (s *deliveryStore) GetSettings(context.Context) (autoscan.Settings, error) {
	return s.settings, nil
}
func (s *deliveryStore) TouchWebhookReceived(_ context.Context, sourceID string) error {
	s.touched = append(s.touched, sourceID)
	return nil
}
func (s *deliveryStore) RecordWebhookError(context.Context, string, string) error { return nil }

type deliveryIngester struct {
	*autoscan.Service
	ingested int
}

func (i *deliveryIngester) IngestChanges(context.Context, autoscan.ChangeIngest) (autoscan.IngestResult, error) {
	i.ingested++
	return autoscan.IngestResult{Enqueued: 1}, nil
}

// The v2 route runs the shared delivery handler: it keeps answering 202 for a
// disabled source, but only a provider Test event may move the admin "Last
// delivery" timestamp. The full stamping rules are covered by the handler
// tests; these rows check the wiring.
func TestAutoscanDeliveryDisabledSourceStampsOnlyTestEvents(t *testing.T) {
	const download = `{"eventType":"Download","series":{"path":"/data/tv/Show"},"episodeFile":{"path":"/data/tv/Show/Season 01/e01.mkv"}}`
	const testEvent = `{"eventType":"Test","series":{"path":"/data/tv/Show"}}`
	for name, tc := range map[string]struct {
		body        string
		wantTouched int
	}{
		"disabled source delivery":   {body: download},
		"disabled source test event": {body: testEvent, wantTouched: 1},
	} {
		t.Run(name, func(t *testing.T) {
			store := &deliveryStore{
				source: autoscan.Source{
					ID:           "src-1",
					PluginID:     autoscan.BuiltinArrWebhookPluginID,
					CapabilityID: autoscan.BuiltinArrWebhookCapabilityID,
					Enabled:      false,
					DeliveryMode: autoscan.DeliveryModeWebhook,
					SourceConfig: map[string]string{autoscan.WebhookProviderConfigKey: "auto"},
				},
				settings: autoscan.Settings{Enabled: true},
			}
			ingester := &deliveryIngester{}
			h := NewHandler(Dependencies{AutoscanDelivery: handlers.NewAutoscanHandler(store, ingester)})

			rec := do(t, h, "POST", Prefix+"/autoscan/webhooks/synthetic-capability", tc.body, nil)
			if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"status":"accepted"`) {
				t.Fatalf("status = %d body %s, want 202 accepted", rec.Code, rec.Body.String())
			}
			if len(store.touched) != tc.wantTouched {
				t.Fatalf("last_received_at stamps = %d, want %d", len(store.touched), tc.wantTouched)
			}
			if ingester.ingested != 0 {
				t.Fatalf("a disabled source must not ingest, ingested = %d", ingester.ingested)
			}
		})
	}
}
