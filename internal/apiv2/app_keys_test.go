package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

type fakeAppKeys struct{ enabled bool }

func (f fakeAppKeys) AppKeySignInEnabled(context.Context) (bool, error) { return f.enabled, nil }

// Apps read the state to decide whether to offer key sign-in, so it must
// follow the server setting.
func TestAppKeyCapabilityFollowsSetting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keys  fakeAppKeys
		state string
	}{
		{name: "setting on", keys: fakeAppKeys{enabled: true}, state: StateAvailable},
		{name: "setting off", keys: fakeAppKeys{}, state: StateDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			deps.AppKeys = tc.keys
			rec := do(t, NewHandler(deps), http.MethodGet, Prefix+"/auth/keys/capability", "", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var body AppKeyCapability
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.State != tc.state || body.SignIn || len(body.SignatureSuites) != 1 {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}
