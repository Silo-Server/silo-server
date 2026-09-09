package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSeekIntervalsSettingsLifecycle(t *testing.T) {
	for _, media := range []string{"video", "audiobook"} {
		for _, direction := range []string{"back", "forward"} {
			key := "player." + media + "_skip_" + direction + "_seconds"
			t.Run(key, func(t *testing.T) {
				h, _ := newValuesTestHandler(t)
				for _, value := range []int{5, 10, 15, 30, 45, 60, 90} {
					rec := routeValues(t, h, http.MethodPut, key, "scope=profile", []byte(fmt.Sprintf(`{"value":%d}`, value)))
					if rec.Code != http.StatusOK {
						t.Fatalf("write %d: %d %s", value, rec.Code, rec.Body.String())
					}
				}
				for _, value := range []string{`0`, `-5`, `7`, `120`, `1.5`, `"30"`, `null`, `true`} {
					rec := routeValues(t, h, http.MethodPut, key, "scope=profile", []byte(`{"value":`+value+`}`))
					if rec.Code != http.StatusBadRequest {
						t.Fatalf("invalid %s: %d %s", value, rec.Code, rec.Body.String())
					}
				}
				for _, scope := range []string{"account", "profile_device", "profile_client", "profile_library&library_id=1", "profile_series&series_id=series-1"} {
					rec := routeValues(t, h, http.MethodPut, key, "scope="+scope, []byte(`{"value":30}`))
					if rec.Code < 400 || rec.Code >= 500 {
						t.Fatalf("scope %s: %d %s", scope, rec.Code, rec.Body.String())
					}
				}
				rec := routeValues(t, h, http.MethodGet, key, "scope=profile", nil)
				var stored settingValueResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
					t.Fatal(err)
				}
				if string(stored.Value) != "90" {
					t.Fatalf("rejected writes changed value: %s", stored.Value)
				}
				rec = routeValues(t, h, http.MethodDelete, key, "scope=profile", nil)
				if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
					t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
				}
				req := valuesRequest(http.MethodGet, "/settings/values/effective?keys="+key, nil)
				rec = httptest.NewRecorder()
				h.HandleGetEffective(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("effective: %d %s", rec.Code, rec.Body.String())
				}
				var result struct {
					Settings []struct {
						Value  int    `json:"value"`
						Source string `json:"source"`
					} `json:"settings"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				want := 10
				if direction == "forward" {
					want = 30
				}
				if len(result.Settings) != 1 || result.Settings[0].Value != want || result.Settings[0].Source != "default" {
					t.Fatalf("reset default: %s", rec.Body.String())
				}
			})
		}
	}
}
