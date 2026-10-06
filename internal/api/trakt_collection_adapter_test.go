package api

import (
	"context"
	"errors"
	"testing"

	metatrakt "github.com/Silo-Server/silo-server/internal/metadata/trakt"
)

type fakeAppClientIDs struct {
	clientID string
	err      error
}

func (f fakeAppClientIDs) AppClientID(context.Context, string) (string, error) {
	return f.clientID, f.err
}

type fakeTraktSettings map[string]string

func (f fakeTraktSettings) Get(_ context.Context, key string) (string, error) { return f[key], nil }
func (f fakeTraktSettings) Set(context.Context, string, string) error         { return nil }
func (f fakeTraktSettings) GetAll(context.Context) (map[string]string, error) { return f, nil }

func TestTraktCollectionAdapterClientID(t *testing.T) {
	legacy := fakeTraktSettings{traktClientIDSettingKey: "legacy-id"}
	for _, tc := range []struct {
		name    string
		plugins watchProviderAppClientIDs
		want    string
	}{
		{name: "plugin app wins", plugins: fakeAppClientIDs{clientID: "plugin-id"}, want: "plugin-id"},
		{name: "legacy setting without a plugin app", plugins: fakeAppClientIDs{}, want: "legacy-id"},
		{name: "legacy setting without watch sync", want: "legacy-id"},
		{name: "plugin read failure keeps the last value", plugins: fakeAppClientIDs{err: errors.New("down")}, want: "previous-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &traktCollectionAdapter{
				client:         metatrakt.NewClient("previous-id", 5),
				watchProviders: tc.plugins,
				settings:       legacy,
			}
			adapter.refreshClientID(t.Context())
			if got := adapter.client.ClientID(); got != tc.want {
				t.Fatalf("client ID = %q, want %q", got, tc.want)
			}
		})
	}
}
