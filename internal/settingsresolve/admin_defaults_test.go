package settingsresolve

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeServerSettings replays fixed server settings and records each read, so a
// test can assert that keys without admin_default never cost one.
type fakeServerSettings struct {
	values map[string]string
	err    error
	reads  []string
}

func (f *fakeServerSettings) Get(_ context.Context, key string) (string, error) {
	f.reads = append(f.reads, key)
	if f.err != nil {
		return "", f.err
	}
	return f.values[key], nil
}

func titleArtAdminDefault(value string) *fakeServerSettings {
	return &fakeServerSettings{values: map[string]string{
		settingscontract.AdminDefaultSettingKey(settingskeys.UiTitleArt): value,
	}}
}

func resolveTitleArt(t *testing.T, settings ServerSettings, rows []userstore.SettingValue) Effective {
	t.Helper()
	got, err := New(mustContract(t)).WithAdminDefaults(settings).Resolve(context.Background(),
		&fakeStore{rows: rows}, Context{ProfileID: "p1", DeviceID: "phone"},
		[]string{settingskeys.UiTitleArt}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Resolve returned %d results, want 1", len(got))
	}
	return got[0]
}

func TestContractDefaultAppliesWithoutAUsableAdminDefault(t *testing.T) {
	cases := map[string]ServerSettings{
		"no reader":                nil,
		"no admin value":           &fakeServerSettings{},
		"empty admin value":        titleArtAdminDefault(""),
		"value the schema rejects": titleArtAdminDefault(`"off"`),
		"failed read":              &fakeServerSettings{err: errors.New("database unavailable")},
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			got := resolveTitleArt(t, settings, nil)
			if string(got.Value) != "true" || got.Source != settingscontract.ScopeDefault {
				t.Fatalf("got %s from %q, want the contract default true", got.Value, got.Source)
			}
		})
	}
}
