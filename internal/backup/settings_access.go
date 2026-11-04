package backup

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// accessSettings are the server settings that decide who can sign in, how
// they may, what reaches this server and what it reaches, and what a viewer
// sees. Each compares a restored value with the live one and reports
// whether the restored one grants more. Restores never widen them: a full
// restore keeps the live value where the backup's is wider
// (keepStricterSettings), and a selective restore never writes a wider one.
// One classification serves both; TestAccessSettingsAreClassified keeps it
// complete for the settings it names.
var accessSettings = func() map[string]widerFunc {
	m := map[string]widerFunc{
		// Who may sign in, and how.
		settingLocalPasswordLogin:             on,
		"auth.email_auto_match":               on,
		"signup.enabled":                      on,
		"password_reset.self_service_enabled": on,
		"jellyfin_compat.enabled":             on,
		"jellyfin_compat.web_enabled":         on,
		"audiobookshelf_compat.enabled":       on,
		// How long a session, or an account its provider dropped, stays
		// signed in, and whether an unreachable provider still vouches.
		"auth.access_token_expiry":                       longer,
		config.AuthRefreshTokenExpirySettingKey:          longer,
		config.AuthProviderRecheckIntervalSettingKey:     longer,
		"jellyfin_compat.session_ttl":                    longer,
		"jellyfin_compat.playback_session_ttl":           longer,
		config.AuthProviderRecheckOutagePolicySettingKey: byRank(map[string]int64{config.AuthRecheckFailClosed: 0, config.AuthRecheckFailOpen: 1}),
		// What a rating-capped profile sees.
		config.AccessUnratedContentSettingKey: func(restored, live string) bool {
			allow := func(v string) bool {
				return strings.EqualFold(strings.TrimSpace(v), config.AccessUnratedContentAllow)
			}
			return allow(restored) && !allow(live)
		},
		// Where this server's traffic and trust reach.
		"notifications.webhooks.allow_private_destinations": on,
		"media_servers.allow_private_destinations":          on,
		settingTrustedProxies:                               moreNetworks,
		settingSMTPSecurity:                                 byRank(map[string]int64{smtpStartTLS: 0, smtpTLS: 0, smtpNone: 1}),
		// What delivers notifications and mail to destinations without a
		// session: a channel an admin turned off stays off.
		settingEmailEnabled:                           on,
		"notifications.email_enabled":                 on,
		"notifications.web_push_enabled":              on,
		"notifications.webhooks_enabled":              on,
		"notifications.apple_push_delivery_enabled":   on,
		"notifications.android_push_delivery_enabled": on,
		"notifications.discord_enabled":               on,
		"notifications.server_channels_enabled":       on,
		"notifications.fanout_enabled":                on,
		settingReleaseEvents:                          on,
		"notifications.ui_enabled":                    on,
		// What leaves the server: downloads, library data sent to outside AI
		// and embedding providers, markers shared online, and the server's
		// advertisement on the local network.
		"download.enabled":                            on,
		"download.transcode_enabled":                  on,
		"metadata_ai.enabled":                         on,
		"subtitle_ai.enabled":                         on,
		"subtitle_ai.transcribe_enabled":              on,
		"recommendations.enabled":                     on,
		"catalog.search.meilisearch.semantic_enabled": on,
		markers.SettingShareByDefault: func(restored, live string) bool {
			return markers.ShareByDefaultEnabled(restored) && !markers.ShareByDefaultEnabled(live)
		},
		markers.SettingMode:    byRank(map[string]int64{string(markers.ModeLocal): 0, string(markers.ModeOnline): 1, string(markers.ModeBoth): 1}),
		"server.lan_discovery": on,
		// Sign-in throttling.
		"ratelimit.enabled": func(restored, live string) bool { return on(live, restored) },
	}
	for key := range ratelimit.DefaultSettings() {
		if strings.HasPrefix(key, "ratelimit.auth.") || strings.HasPrefix(key, "ratelimit.ip.") {
			m[key] = higher
		}
	}
	return m
}()

// Access setting keys and values named more than once.
const (
	settingLocalPasswordLogin = "auth.local_password_login" //nolint:gosec // G101: a setting key, not a credential
	settingTrustedProxies     = "clientip.trusted_proxies"
	settingSMTPSecurity       = "email.smtp_security"
	smtpStartTLS, smtpTLS     = "starttls", "tls"
	smtpNone                  = "none"
	settingReleaseEvents      = "notifications.release_events_enabled"
	settingEmailEnabled       = "email.enabled"
	settingSMTPHost           = "email.smtp_host"
	settingSMTPPassword       = "email.smtp_password" //nolint:gosec // G101: a setting key, not a credential
	settingAIBaseURL          = "ai.base_url"
	settingAIKey              = "ai.api_key"
)

// widerFunc reports whether restored grants more than live, both effective
// values (a setting's default for an unset one). A value it cannot read
// counts as wider, so the live value stays when the two cannot be compared.
type widerFunc func(restored, live string) bool

// on: a switch that grants when true.
func on(restored, live string) bool {
	r, rerr := strconv.ParseBool(restored)
	l, lerr := strconv.ParseBool(live)
	return rerr != nil || lerr != nil || r && !l
}

// longer: a duration that grants more when longer.
func longer(restored, live string) bool {
	r, rerr := config.ParseDuration(restored)
	l, lerr := config.ParseDuration(live)
	return rerr != nil || lerr != nil || r <= 0 || l <= 0 || r > l
}

// higher: a rate limit that grants more when higher.
func higher(restored, live string) bool {
	r, rerr := strconv.ParseFloat(restored, 64)
	l, lerr := strconv.ParseFloat(live, 64)
	return rerr != nil || lerr != nil || r <= 0 || l <= 0 || r > l
}

// byRank compares values by how much each grants.
func byRank(ranks map[string]int64) widerFunc {
	return func(restored, live string) bool {
		r, rok := ranks[restored]
		l, lok := ranks[live]
		return !rok || !lok || r > l
	}
}

// moreNetworks: a list of networks that grants more when it trusts an
// address the live list does not.
func moreNetworks(restored, live string) bool {
	networks := func(v string) ([]netip.Prefix, bool) {
		var out []netip.Prefix
		for _, field := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
			p, err := netip.ParsePrefix(field)
			if err != nil {
				addr, aerr := netip.ParseAddr(field)
				if aerr != nil {
					return nil, false
				}
				p = netip.PrefixFrom(addr, addr.BitLen())
			}
			out = append(out, p.Masked())
		}
		return out, true
	}
	r, rok := networks(restored)
	l, lok := networks(live)
	if !rok || !lok {
		return true
	}
	for _, p := range r {
		if !slices.ContainsFunc(l, func(q netip.Prefix) bool { return q.Bits() <= p.Bits() && q.Contains(p.Addr()) }) {
			return true
		}
	}
	return false
}

// settingDefault is key's value when no row sets it.
func settingDefault(key string) string {
	if v, ok := ratelimit.DefaultSettings()[key]; ok {
		return v
	}
	return config.EffectiveAdminSettings(nil)[key]
}

// AccessSettingWider reports whether key is an access setting and, if so,
// whether restored grants more than live. An empty value is the setting's
// default.
func AccessSettingWider(key, restored, live string) (access, wider bool) {
	wide, ok := accessSettings[key]
	if !ok {
		return false, false
	}
	effective := func(v string) string {
		if v == "" {
			return settingDefault(key)
		}
		return v
	}
	return true, wide(effective(restored), effective(live))
}

// AccessSettingKeys lists the access settings, sorted.
func AccessSettingKeys() []string {
	return slices.Sorted(maps.Keys(accessSettings))
}

// KeepStricterSettings writes, into dbURL, the live server's (liveURL)
// value of each access setting the restored data would widen: its row as
// stored, ciphertext bound to the same key name included, or none when the
// live server has the default. It returns what it kept. Values are
// compared decrypted with cipher; one that does not decrypt counts as
// wider.
func (l PGLoader) KeepStricterSettings(ctx context.Context, dbURL, liveURL string, cipher *secret.Cipher) ([]string, error) {
	keys := AccessSettingKeys()
	live, err := readSettingsAt(ctx, liveURL, keys)
	if err != nil {
		return nil, fmt.Errorf("reading the live server's settings: %w", err)
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var kept []string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		restored, err := settingRows(ctx, tx, keys)
		if err != nil {
			return err
		}
		for _, key := range keys {
			r, rok := plainSetting(cipher, key, restored[key])
			l, lok := plainSetting(cipher, key, live[key])
			if _, wider := AccessSettingWider(key, r, l); rok && lok && !wider {
				continue
			}
			if live[key] == restored[key] {
				continue
			}
			if _, set := live[key]; set {
				_, err = tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, $2)
					ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, live[key])
			} else {
				_, err = tx.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, key)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			kept = append(kept, key)
		}
		return nil
	})
	return kept, err
}

// plainSetting decrypts a stored setting value with cipher when it is
// encrypted; ok is false when it does not decrypt.
func plainSetting(cipher *secret.Cipher, key, value string) (string, bool) {
	if cipher == nil || value == "" {
		return value, true
	}
	plain, err := cipher.DecryptIfEncrypted(value, secret.SettingsAAD(key))
	return plain, err == nil
}

// readSettingsAt reads keys' rows of the database at url.
func readSettingsAt(ctx context.Context, url string, keys []string) (map[string]string, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	return settingRows(ctx, conn, keys)
}

func settingRows(ctx context.Context, q queryer, keys []string) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT key, value FROM server_settings WHERE key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

// keepStricterSettings keeps, in the restored database, the live value of
// each access setting the backup would widen, and prints what it kept.
func keepStricterSettings(ctx context.Context, loader DatabaseLoader, dbURL, liveURL string, cipher *secret.Cipher, out io.Writer) error {
	kept, err := loader.KeepStricterSettings(ctx, dbURL, liveURL, cipher)
	if err != nil {
		return fmt.Errorf("keeping the live server's stricter settings: %w", err)
	}
	if len(kept) > 0 {
		_, _ = fmt.Fprintf(out, "Kept %d settings as this server has them, stricter than the backup: %s.\n", len(kept), strings.Join(kept, ", "))
	}
	return nil
}
