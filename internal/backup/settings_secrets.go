package backup

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// rotatedSettings are the signing keys Silo generates for the tokens it
// issues (sessions, signed download and artwork links, OAuth state, cursors,
// Audiobookshelf tokens), with how each is generated. A restore never brings
// the backup's back: one leaked and rotated after the backup would sign
// again, including links to backup objects. It writes a fresh one instead,
// as the server would on its first start. Every session ends with a restore
// anyway (secretTables), so nothing legitimate needs the old ones.
var rotatedSettings = map[string]func([]byte) string{
	// cmd/silo generates it at startup when empty.
	settingJWTSecret: base64.StdEncoding.EncodeToString,
	// internal/audiobooks generates it on first use when empty.
	settingABSJWTSecret: hex.EncodeToString,
}

// The signing key settings, named once for this package.
const (
	settingJWTSecret    = "auth.jwt_secret"           //nolint:gosec // G101: a setting name, not a credential
	settingABSJWTSecret = "audiobooks.abs.jwt_secret" //nolint:gosec // G101: a setting name, not a credential
)

// restoredSecretSettings are the other sensitive settings
// (catalog.SensitiveSettingKeys): credentials an operator entered for other
// services, or keys other services hold the other half of. A restore brings
// them back with the rest of the settings; rotating them would break what
// they connect to. TestSensitiveSettingsAreClassified fails on a sensitive
// setting in neither list.
var restoredSecretSettings = strings.Fields(`
	s3.public_access_key s3.public_secret_key s3.public_token_secret
	s3.private_access_key s3.private_secret_key s3.user_db_access_key s3.user_db_secret_key
	s3.operational_access_key s3.operational_secret_key s3.operational_token_secret
	backup.s3.secret_key
	storage.transition.target redis.url redis.sentinel_password
	tmdb.api_key mdblist.api_key introdb.api_key
	ai.providers ai.chatgpt.credentials ai.api_key ai.asr_api_key subtitle_ai.api_key
	recommendations.embedding_auth_token recommendations.openai_api_key
	catalog.search.meilisearch.api_key watchsync.simkl.client_id watchsync.simkl.client_secret
	requests.radarr.api_key requests.sonarr.api_key email.smtp_password
	discord.client_secret discord.bot_token
	notifications.web_push.vapid_keypair notifications.push_relay_api_key`)

// RotateSigningSecrets writes a fresh value for each rotatedSettings key
// into dbURL, encrypted with cipher as the settings store encrypts it, or
// deletes the keys when there is no cipher, so the server generates them
// when it starts. It reports whether it wrote new values.
func (l PGLoader) RotateSigningSecrets(ctx context.Context, dbURL string, cipher *secret.Cipher) (bool, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return false, err
	}
	defer pool.Close()
	keys := rotatedSettingKeys()
	if cipher == nil {
		_, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = ANY($1)`, keys)
		return false, err
	}
	values := map[string]string{}
	for _, key := range keys {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return false, err
		}
		values[key] = rotatedSettings[key](b)
	}
	return true, catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(pool), cipher).SetMany(ctx, values)
}

func rotatedSettingKeys() []string {
	return slices.Sorted(maps.Keys(rotatedSettings))
}

// rotateSigningSecrets replaces the restored database's signing keys
// (rotatedSettings) and says so.
func rotateSigningSecrets(ctx context.Context, loader DatabaseLoader, dbURL string, cipher *secret.Cipher, out io.Writer) error {
	wrote, err := loader.RotateSigningSecrets(ctx, dbURL, cipher)
	if err != nil {
		return fmt.Errorf("replacing the restored signing keys: %w", err)
	}
	keys := strings.Join(rotatedSettingKeys(), ", ")
	if wrote {
		_, _ = fmt.Fprintf(out, "Replaced the backup's signing keys (%s) with new ones.\n", keys)
	} else {
		_, _ = fmt.Fprintf(out, "Cleared the backup's signing keys (%s); Silo generates new ones when it starts.\n", keys)
	}
	return nil
}
