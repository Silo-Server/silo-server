package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/go-chi/chi/v5"
)

func TestOrderedPushLegacyWriterConflict(t *testing.T) {
	store := &handlerPushStore{err: notifications.ErrPushLegacyWriter}
	h := NewNotificationsHandler(&notifications.System{PushDevices: notifications.NewPushDeviceService(store, handlerPushCipher(t))}, nil)
	router := chi.NewRouter()
	router.Post("/devices", h.HandleRegisterPushDevice)
	router.Delete("/devices/{device_id}", h.HandleUnregisterPushDevice)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		path := "/devices"
		body := `{"platform":"android","device_id":"install","token":"` + strings.Repeat("a", 64) + `"}`
		if method == http.MethodDelete {
			path += "/install"
			body = ""
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, newPushDevicesRequest(method, path, body))
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "push_registration_upgrade_required") {
			t.Fatalf("legacy %s=%d %s", method, rec.Code, rec.Body.String())
		}
	}
}
