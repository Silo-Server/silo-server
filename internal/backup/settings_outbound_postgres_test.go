package backup

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/serversettings"
)

// checkOutboundSettings: a full restore keeps the live mail route as a
// unit, an SMTP host and password changed since the backup included; a
// route the live server does not have comes back with its secret cleared
// and turned off, and so does every route of a restore that carries none.
func checkOutboundSettings(t *testing.T, loader PGLoader, pool *pgxpool.Pool, dsn, into string, restored *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	set := func(db *pgxpool.Pool, values map[string]string) {
		t.Helper()
		for key, value := range values {
			if _, err := db.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func(key string) string {
		t.Helper()
		var v string
		if err := restored.QueryRow(ctx, `SELECT coalesce((SELECT value FROM server_settings WHERE key = $1), '')`, key).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	keys := []string{settingSMTPHost, settingSMTPPassword, mail.SettingSMTPUsername, settingEmailEnabled, settingAIBaseURL, settingAIKey}
	var saved []string
	for _, key := range keys {
		var v *string
		_ = pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&v)
		if v != nil {
			saved = append(saved, key, *v)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = ANY($1)`, keys)
		for i := 0; i+1 < len(saved); i += 2 {
			_, _ = pool.Exec(context.Background(), `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, saved[i], saved[i+1])
		}
	})
	set(restored, map[string]string{settingSMTPHost: "smtp.then.invalid", settingSMTPPassword: "then", mail.SettingSMTPUsername: "then-user", settingEmailEnabled: "true",
		settingAIBaseURL: "https://ai.then.invalid", settingAIKey: "then-key",
		config.AIProvidersSettingKey:    `{"version":2,"providers":[{"id":"custom-then","type":"custom","name":"Then","base_url":"http://ai.then.invalid/v1","api_key":"then-key","capabilities":["chat","embeddings"]}]}`,
		config.AITextProviderSettingKey: "custom-then", config.EmbeddingProviderSettingKey: "custom-then", "recommendations.enabled": "true"})
	set(pool, map[string]string{settingSMTPHost: "smtp.now.invalid", settingSMTPPassword: "now", mail.SettingSMTPUsername: "now-user", settingEmailEnabled: "true"})
	if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, []string{settingAIBaseURL, settingAIKey}); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("outbound-settings-test-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	// The AI providers go as a unit: no job stays bound to a cleared
	// provider, and the next start converts no provider back from what is left.
	checkAIReset := func(when string) {
		t.Helper()
		ai, err := serversettings.NewEncryptedSettingsRepo(serversettings.NewServerSettingsRepo(restored), cipher).
			GetMany(ctx, append(config.AIConnectionSettingKeys(), "recommendations.enabled")...)
		if err != nil {
			t.Fatal(err)
		}
		if err := config.ValidateAIProviderBindings(ai); err != nil {
			t.Fatalf("%s: %v; want no job bound to a cleared AI provider", when, err)
		}
		if writes, _, err := config.PlanAIProviderConversion(ai); err != nil || writes != nil {
			t.Fatalf("%s: the next start converts %v (%v), want nothing", when, writes, err)
		}
		if registry, err := config.ParseAIProviderRegistry(ai[config.AIProvidersSettingKey]); err != nil || len(registry.Providers) != 0 {
			t.Fatalf("%s: AI providers %v (%v), want none", when, registry.Providers, err)
		}
	}
	if _, _, err := loader.CarryOutboundSettings(ctx, into, dsn, cipher); err != nil {
		t.Fatal(err)
	}
	if read(settingSMTPHost) != "smtp.now.invalid" || read(settingSMTPPassword) != "now" || read(mail.SettingSMTPUsername) != "now-user" {
		t.Fatalf("mail route %s / %s, want the live one", read(settingSMTPHost), read(settingSMTPPassword))
	}
	if read(settingAIBaseURL) != "https://ai.then.invalid" || read(settingAIKey) != "" {
		t.Fatalf("a route the live server lacks: %s / %q, want the address kept and the key cleared", read(settingAIBaseURL), read(settingAIKey))
	}
	checkAIReset("a registry the live server lacks")
	if !secret.IsEncrypted(read(config.AIProvidersSettingKey)) {
		t.Fatalf("the emptied AI provider registry is stored as %q, want it encrypted", read(config.AIProvidersSettingKey))
	}
	// A restore with another key has already dropped the providers it cannot
	// read; the bindings left behind still reset them.
	if _, err := restored.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, config.AIProvidersSettingKey); err != nil {
		t.Fatal(err)
	}
	set(restored, map[string]string{config.AISpeechProviderSettingKey: "custom-then"})
	if _, cleared, err := loader.CarryOutboundSettings(ctx, into, "", nil); err != nil || read(settingSMTPPassword) != "" || read(settingEmailEnabled) != settingOff ||
		!slices.Contains(cleared, "mail (SMTP)") || !slices.Contains(cleared, "AI providers") {
		t.Fatalf("carrying none (%v): password %q, enabled %q, cleared %v; want it cleared and off", err, read(settingSMTPPassword), read(settingEmailEnabled), cleared)
	}
	checkAIReset("bindings without their providers")
}
