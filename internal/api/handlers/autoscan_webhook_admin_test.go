package handlers

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func TestAdminSourceWebhookStore(t *testing.T) {
	for _, action := range []string{"create", "rotate", "delete"} {
		t.Run(action, func(t *testing.T) {
			var calls []string
			failure := errors.New("store failure")
			var writeErr, readErr error
			mode := autoscan.DeliveryModeWebhook
			store := &fakeAutoscanStore{
				getSourceFn: func(id string) (autoscan.Source, error) {
					calls = append(calls, "source:"+id)
					return autoscan.Source{ID: id, DeliveryMode: mode}, readErr
				},
				getWebhookFn: func(id string) (autoscan.WebhookEndpoint, error) {
					calls = append(calls, "read:"+id)
					return autoscan.WebhookEndpoint{SourceID: id, SecretSuffix: "tail"}, nil
				},
				revealTokenFn: func(id string) (string, error) { calls = append(calls, "reveal:"+id); return "synthetic-current", nil },
			}
			write := func(id string) (autoscan.WebhookEndpoint, string, error) {
				calls = append(calls, "write:"+id)
				return autoscan.WebhookEndpoint{}, "unused-receipt", writeErr
			}
			store.createWebhookFn = write
			store.rotateWebhookFn = write
			store.deleteWebhookFn = func(id string) error { _, _, err := write(id); return err }
			h := NewAutoscanHandler(store, nil)
			run := func() (AdminAutoscanSourceView, error) {
				switch action {
				case "create":
					return h.CreateAdminAutoscanSourceWebhook(t.Context(), " source-a ")
				case "rotate":
					return h.RotateAdminAutoscanSourceWebhook(t.Context(), " source-a ")
				default:
					return AdminAutoscanSourceView{}, h.DeleteAdminAutoscanSourceWebhook(t.Context(), " source-a ")
				}
			}
			out, err := run()
			want := []string{"source:source-a", "write:source-a", "read:source-a", "reveal:source-a"}
			if action == "delete" {
				want = []string{"write:source-a"}
			} else if out.WebhookURL != "/api/v2/autoscan/webhooks/synthetic-current" || !out.WebhookConfigured {
				t.Fatal(out)
			}
			if err != nil || !reflect.DeepEqual(calls, want) {
				t.Fatal(err, calls)
			}
			calls = nil
			writeErr = failure
			if _, err = run(); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if action != "delete" && len(calls) != 2 {
				t.Fatal("readback after failed mutation", calls)
			}
			writeErr = nil
			readErr = autoscan.ErrNotFound
			calls = nil
			if action != "delete" {
				if _, err = run(); !errors.Is(err, autoscan.ErrNotFound) || len(calls) != 1 {
					t.Fatal(err, calls)
				}
			}
			readErr = nil
			mode = autoscan.DeliveryModePoll
			calls = nil
			if action == "create" {
				if _, err = run(); !errors.Is(err, ErrAdminSourceWebhookMode) || len(calls) != 1 {
					t.Fatal(err, calls)
				}
			}
			if action == "rotate" {
				if _, err = run(); err != nil {
					t.Fatal("existing poll endpoint rotation refused", err)
				}
			}
			mode = autoscan.DeliveryModeWebhook
			store.revealTokenFn = func(string) (string, error) { return "", failure }
			if action != "delete" {
				out, err = run()
				if err != nil || out.WebhookURL != "" || !out.WebhookConfigured {
					t.Fatal("reveal failure must not replay mutation", out, err)
				}
			}
			h = nil
			if _, err = run(); !errors.Is(err, ErrAdminSourceWebhookUnavailable) {
				t.Fatal(err)
			}
		})
	}
}
