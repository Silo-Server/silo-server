package backup

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/mail"
)

// settingOff is a switch setting turned off.
const settingOff = "false"

// The Simkl application's settings, and the push relay's key.
const (
	settingPushRelayKey = "notifications.push_relay_api_key"
	settingSimklID      = "watchsync.simkl.client_id"
	settingSimklSecret  = "watchsync.simkl.client_secret"
)

// outboundBundle is a set of settings that together say where this server
// connects outside and how it signs in there: an address with the secrets
// sent to it (settingRoutes), or a credential for a fixed service.
type outboundBundle struct {
	// name says what the bundle connects to.
	name string
	// keys are every setting of the bundle.
	keys []string
	// secrets are the ones cleared where the live server has no bundle.
	secrets []string
	// enabled is the switch turned off then, or "".
	enabled string
}

// outboundBundles are the settings a full restore never takes from the
// backup on their own (CarryOutboundSettings): the endpoint and secret
// routes, and the server's credentials for outside services.
var outboundBundles = func() []outboundBundle {
	// The mail and AI units are the settings their consumers read
	// (mail.SMTPSettingKeys, config.AIConnectionSettingKeys), so a restore
	// never mixes the live server's values with the backup's.
	ai := append(config.AIConnectionSettingKeys(), chatgpt.CredentialsKey)
	out := []outboundBundle{
		{name: "mail (SMTP)", keys: mail.SMTPSettingKeys(), secrets: []string{settingSMTPPassword}, enabled: settingEmailEnabled},
		{name: "AI providers", keys: ai, secrets: []string{config.AIProvidersSettingKey, chatgpt.CredentialsKey, settingAIKey, settingASRKey,
			settingSubtitleAIKey, settingEmbeddingToken, settingOpenAIEmbeddingKey}},
	}
	whole := func(keys ...string) outboundBundle { return outboundBundle{name: keys[0], keys: keys, secrets: keys} }
	out = append(out,
		outboundBundle{name: "the push relay", keys: []string{"notifications.push_relay_url", settingPushRelayKey, "notifications.push_relay_deployment_id",
			"notifications.push_relay_key_prefix", "notifications.push_relay_expires_at", "notifications.push_relay_reregistration_required"},
			secrets: []string{settingPushRelayKey}},
		outboundBundle{name: "the search engine", keys: []string{catalog.SearchSettingMeilisearchURL, catalog.SearchSettingMeilisearchAPIKey},
			secrets: []string{catalog.SearchSettingMeilisearchAPIKey}},
		outboundBundle{name: "the Discord application", keys: []string{"discord.client_id", "discord.client_secret", "discord.bot_token"},
			secrets: []string{"discord.client_secret", "discord.bot_token"}},
		whole("tmdb.api_key"), whole("mdblist.api_key"), whole("introdb.api_key"),
		whole("requests.radarr.api_key"), whole("requests.sonarr.api_key"),
		whole("watchsync.trakt.client_secret"), outboundBundle{name: settingSimklSecret, keys: []string{settingSimklID, settingSimklSecret}, secrets: []string{settingSimklID, settingSimklSecret}},
		whole("watchsync.healthy.client_secret"), whole("watchsync.slow.client_secret"),
	)
	return out
}()

// outboundKeys lists every setting of outboundBundles.
func outboundKeys() []string {
	var keys []string
	for _, b := range outboundBundles {
		keys = append(keys, b.keys...)
	}
	return keys
}

// CarryOutboundSettings gives the restored database (dbURL) the live
// server's (liveURL's) outbound settings, each bundle as a unit: a
// backup's address never reaches the live secrets, nor the backup's
// secrets a service again. A bundle the live server does not have, or
// every bundle when liveURL is "" (another deployment, or a separate
// restore), keeps the backup's other settings with its secrets cleared and
// its switch off. It returns the bundles it took from the live server and
// those it cleared, by name.
func (l PGLoader) CarryOutboundSettings(ctx context.Context, dbURL, liveURL string) (carried, cleared []string, err error) {
	live := map[string]string{}
	if liveURL != "" {
		if live, err = readSettingsAt(ctx, liveURL, outboundKeys()); err != nil {
			return nil, nil, fmt.Errorf("reading the live server's settings: %w", err)
		}
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		restored, err := settingRows(ctx, tx, outboundKeys())
		if err != nil {
			return err
		}
		for _, b := range outboundBundles {
			if slices.ContainsFunc(b.keys, func(k string) bool { _, ok := live[k]; return ok }) {
				if err := b.takeLive(ctx, tx, live); err != nil {
					return err
				}
				carried = append(carried, b.name)
				continue
			}
			if !slices.ContainsFunc(b.secrets, func(k string) bool { return restored[k] != "" }) {
				continue
			}
			if err := b.clear(ctx, tx); err != nil {
				return err
			}
			cleared = append(cleared, b.name)
		}
		return nil
	})
	return carried, cleared, err
}

// takeLive replaces the bundle's settings in tx with the live ones.
func (b outboundBundle) takeLive(ctx context.Context, tx pgx.Tx, live map[string]string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, b.keys); err != nil {
		return err
	}
	for _, k := range b.keys {
		if v, ok := live[k]; ok {
			if _, err := tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)`, k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// clear deletes the bundle's secrets in tx and turns its switch off.
func (b outboundBundle) clear(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, b.secrets); err != nil {
		return err
	}
	if b.enabled == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, b.enabled, settingOff)
	return err
}

// carryOutboundSettings takes the outbound settings from the live server
// (liveURL, or none for "") and says what it cleared.
func carryOutboundSettings(ctx context.Context, loader DatabaseLoader, dbURL, liveURL string, out io.Writer) error {
	carried, cleared, err := loader.CarryOutboundSettings(ctx, dbURL, liveURL)
	if err != nil {
		return fmt.Errorf("keeping the live server's connections to outside services: %w", err)
	}
	if len(carried) > 0 {
		_, _ = fmt.Fprintf(out, "Kept this server's connections to outside services in place of the backup's: %s.\n", strings.Join(carried, ", "))
	}
	if len(cleared) > 0 {
		_, _ = fmt.Fprintf(out, "Cleared the backup's credentials for outside services this server does not have, and turned them off; enter them again to use them: %s.\n",
			strings.Join(cleared, ", "))
	}
	return nil
}

// CarryServerSpecificSettings gives the restored database (dbURL) the live
// server's (liveURL's) value of every server-specific setting
// (SettingKindServerSpecific), deleting one the live server does not have
// so its default applies: they describe this server, whichever deployment
// the backup came from, as a selective restore never writes them. Signing
// keys a restore rotates (rotatedSettings) keep their new values. It
// returns how many settings it changed.
func (l PGLoader) CarryServerSpecificSettings(ctx context.Context, dbURL, liveURL string) (int, error) {
	serverSpecific := func(key string) bool {
		kind, ok := SettingKindOf(key)
		_, rotated := rotatedSettings[key]
		return ok && kind == SettingKindServerSpecific && !rotated
	}
	live, err := allSettingsAt(ctx, liveURL, serverSpecific)
	if err != nil {
		return 0, fmt.Errorf("reading the live server's settings: %w", err)
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	changed := 0
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		restored, err := allSettings(ctx, tx, serverSpecific)
		if err != nil {
			return err
		}
		for key := range restored {
			if _, keep := live[key]; keep {
				continue
			}
			if _, err := tx.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, key); err != nil {
				return err
			}
			changed++
		}
		for key, value := range live {
			if v, ok := restored[key]; ok && v == value {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	return changed, err
}

// allSettingsAt reads the settings of the database at url that keep says to.
func allSettingsAt(ctx context.Context, url string, keep func(string) bool) (map[string]string, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	return allSettings(ctx, conn, keep)
}

func allSettings(ctx context.Context, q queryer, keep func(string) bool) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT key, value FROM server_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if keep(key) {
			out[key] = value
		}
	}
	return out, rows.Err()
}

// carryServerSpecificSettings gives the restored database the live
// server's own settings and says how many changed.
func carryServerSpecificSettings(ctx context.Context, loader DatabaseLoader, dbURL, liveURL string, out io.Writer) error {
	changed, err := loader.CarryServerSpecificSettings(ctx, dbURL, liveURL)
	if err != nil {
		return fmt.Errorf("keeping this server's own settings: %w", err)
	}
	if changed > 0 {
		_, _ = fmt.Fprintf(out, "Kept this server's own settings (its address, storage, keys, and infrastructure) in place of the backup's: %d changed.\n", changed)
	}
	return nil
}
