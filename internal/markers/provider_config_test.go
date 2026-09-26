package markers

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProviderConfigEnabledForFetchSortsAndFilters(t *testing.T) {
	s := &ProviderConfigStore{cache: map[string]ProviderConfig{
		"alpha":   {Provider: "alpha", FetchEnabled: true, FetchPriority: 200},
		"bravo":   {Provider: "bravo", FetchEnabled: true, FetchPriority: 100},
		"charlie": {Provider: "charlie", FetchEnabled: false, FetchPriority: 50},
	}}

	got := s.EnabledForFetch()
	if len(got) != 2 {
		t.Fatalf("EnabledForFetch returned %d providers, want 2 (charlie disabled)", len(got))
	}
	if got[0].Provider != "bravo" || got[1].Provider != "alpha" {
		t.Errorf("order = [%s %s], want [bravo alpha] by fetch_priority", got[0].Provider, got[1].Provider)
	}
}

func TestProviderConfigGet(t *testing.T) {
	s := &ProviderConfigStore{cache: map[string]ProviderConfig{
		"introdb": {Provider: "introdb", FetchEnabled: true, ContributeEnabled: false},
	}}
	c, ok := s.Get("introdb")
	if !ok {
		t.Fatal("expected introdb config")
	}
	if c.ContributeEnabled {
		t.Error("contribute should default off")
	}
	if _, ok := s.Get("missing"); ok {
		t.Error("unexpected config for unknown provider")
	}
}

func TestProviderConfigRuntimeRevisionRequiresDatabase(t *testing.T) {
	for _, store := range []*ProviderConfigStore{nil, NewProviderConfigStore(nil)} {
		if revision, err := store.RuntimeRevision(t.Context()); err == nil || revision != "" {
			t.Fatalf("RuntimeRevision without database = %q, %v; want an error", revision, err)
		}
	}
}

func TestProviderConfigRuntimeRevisionTracksDatabaseChanges(t *testing.T) {
	fixture := newContributionStoreFixture(t)
	ctx := t.Context()
	store := NewProviderConfigStore(fixture.pool)
	var installationID int
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO plugin_installations(plugin_id,version,install_path)
		VALUES($1,'1.0.0','revision-test') RETURNING id`, fixture.provider).Scan(&installationID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id=$1`, installationID)
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM marker_provider_config WHERE provider=$1`, fixture.provider)
	})
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO plugin_capabilities(plugin_installation_id,capability_type,capability_id)
		VALUES($1,'marker_provider.v1','test')`, installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO plugin_runtime_configs(plugin_installation_id,config_key,config_value)
		VALUES($1,'account','{"api_key":"revision-test-value"}')`, installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO marker_provider_config(provider) VALUES($1)`, fixture.provider); err != nil {
		t.Fatal(err)
	}
	previous, err := store.RuntimeRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherTimezone := fixture.pool.Config()
	otherTimezone.ConnConfig.RuntimeParams["timezone"] = "Asia/Tokyo"
	otherPool, err := pgxpool.NewWithConfig(ctx, otherTimezone)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(otherPool.Close)
	if revision, err := NewProviderConfigStore(otherPool).RuntimeRevision(ctx); err != nil || revision != previous {
		t.Fatalf("timezone changed revision: %q != %q, err=%v", revision, previous, err)
	}
	for _, tt := range []struct {
		name    string
		query   string
		key     any
		changed bool
	}{
		{"config value excluded", `UPDATE plugin_runtime_configs SET config_value='{"api_key":"changed-value"}' WHERE plugin_installation_id=$1`, installationID, false},
		{"config timestamp", `UPDATE plugin_runtime_configs SET updated_at=updated_at+interval '1 second' WHERE plugin_installation_id=$1`, installationID, true},
		{"capability timestamp", `UPDATE plugin_capabilities SET updated_at=updated_at+interval '1 second' WHERE plugin_installation_id=$1`, installationID, true},
		{"plugin version", `UPDATE plugin_installations SET version='1.1.0' WHERE id=$1`, installationID, true},
		{"disable installation", `UPDATE plugin_installations SET enabled=false WHERE id=$1`, installationID, true},
		{"disabled config excluded", `UPDATE plugin_runtime_configs SET updated_at=updated_at+interval '1 second' WHERE plugin_installation_id=$1`, installationID, false},
		{"enable installation", `UPDATE plugin_installations SET enabled=true WHERE id=$1`, installationID, true},
		{"builtin excluded", `UPDATE plugin_installations SET kind='builtin' WHERE id=$1`, installationID, true},
		{"builtin version excluded", `UPDATE plugin_installations SET version='1.2.0' WHERE id=$1`, installationID, false},
		{"fetch enabled without installation", `UPDATE marker_provider_config SET fetch_enabled=false WHERE provider=$1`, fixture.provider, true},
		{"fetch priority", `UPDATE marker_provider_config SET fetch_priority=fetch_priority+1 WHERE provider=$1`, fixture.provider, true},
		{"other provider setting", `UPDATE marker_provider_config SET contribute_enabled=true,updated_at=updated_at+interval '1 second' WHERE provider=$1`, fixture.provider, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := fixture.pool.Exec(ctx, tt.query, tt.key); err != nil {
				t.Fatal(err)
			}
			revision, err := store.RuntimeRevision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if (revision != previous) != tt.changed {
				t.Errorf("revision changed = %v, want %v", revision != previous, tt.changed)
			}
			previous = revision
		})
	}
}
