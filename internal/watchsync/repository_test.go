package watchsync

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestMediaDurationQueryUsesActiveMediaFilesPredicate(t *testing.T) {
	if !strings.Contains(mediaDurationQuery, "missing_since IS NULL") {
		t.Fatalf("media duration query must filter active files with missing_since IS NULL:\n%s", mediaDurationQuery)
	}
	if strings.Contains(mediaDurationQuery, "missing = false") {
		t.Fatalf("media duration query references removed media_files.missing column:\n%s", mediaDurationQuery)
	}
}

func TestPendingHistoryExportsQueryPagesInAStableOrder(t *testing.T) {
	// Losing either half of this leaves a drain re-reading rejected exports
	// instead of paging past them.
	if !strings.Contains(pendingHistoryExportsQuery, "(watched_at, id) > ($3::timestamptz, $4::uuid)") {
		t.Fatalf("pending history export query must page past a cursor:\n%s", pendingHistoryExportsQuery)
	}
	if !strings.Contains(pendingHistoryExportsQuery, "ORDER BY watched_at ASC, id ASC") {
		t.Fatalf("pending history export query must order by the full cursor key:\n%s", pendingHistoryExportsQuery)
	}
}

func TestPluginCredentialBundleRoundTrip(t *testing.T) {
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(nil, cipher)
	expiresAt := time.Now().UTC().Truncate(time.Second)
	input := Connection{
		Provider: "plugin:4:tracker", UserID: 7, ProfileID: "profile",
		AccessToken: testAccessToken, RefreshToken: testRefreshToken, TokenExpiresAt: &expiresAt,
		TokenType: testDPoPTokenType, Scopes: []string{testHistoryScope, "watchlist"},
		SecretAttributes: map[string]string{"instance": testOneValue},
	}
	encoded, err := repository.encodePluginCredentials(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, input.AccessToken) || !strings.HasPrefix(encoded, "enc:v1:") {
		t.Fatalf("credential bundle was not encrypted: %q", encoded)
	}
	output := Connection{Provider: input.Provider, UserID: input.UserID, ProfileID: input.ProfileID}
	if err := repository.decodePluginCredentials(&output, encoded); err != nil {
		t.Fatal(err)
	}
	if output.AccessToken != input.AccessToken || output.RefreshToken != input.RefreshToken ||
		output.TokenType != input.TokenType || !output.TokenExpiresAt.Equal(expiresAt) ||
		!reflect.DeepEqual(output.Scopes, input.Scopes) || !reflect.DeepEqual(output.SecretAttributes, input.SecretAttributes) {
		t.Fatalf("decoded credentials = %#v", output)
	}
}

func TestPluginCredentialBundleUsesConnectionIdentityAsAAD(t *testing.T) {
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(nil, cipher)
	input := Connection{
		Provider: "plugin:4:tracker", UserID: 7, ProfileID: "profile-a",
		AccessToken: testAccessToken,
	}
	encoded, err := repository.encodePluginCredentials(input)
	if err != nil {
		t.Fatal(err)
	}
	wrongIdentity := Connection{Provider: input.Provider, UserID: input.UserID, ProfileID: "profile-b"}
	if err := repository.decodePluginCredentials(&wrongIdentity, encoded); err == nil {
		t.Fatal("decodePluginCredentials with different profile identity succeeded")
	}
}

func TestFormerBuiltInConnectionKeepsTokenColumnsUntilRewritten(t *testing.T) {
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(nil, cipher)
	// A connection a former built-in provider wrote has no bundle, so reading it
	// keeps the tokens from the dedicated columns.
	conn := Connection{Provider: "trakt", UserID: 7, ProfileID: "profile", AccessToken: testAccessToken, RefreshToken: "refresh"}
	if err := repository.decodePluginCredentials(&conn, ""); err != nil {
		t.Fatal(err)
	}
	if conn.AccessToken != testAccessToken || conn.RefreshToken != "refresh" {
		t.Fatalf("tokens after empty bundle = %q/%q", conn.AccessToken, conn.RefreshToken)
	}
	// The next write stores the full bundle, so plugin-only fields survive.
	conn.SecretAttributes = map[string]string{"scope_hint": "sync"}
	encoded, err := repository.encodePluginCredentials(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "enc:v1:") {
		t.Fatalf("credential bundle = %q, want encrypted value", encoded)
	}
	reread := Connection{Provider: conn.Provider, UserID: conn.UserID, ProfileID: conn.ProfileID}
	if err := repository.decodePluginCredentials(&reread, encoded); err != nil {
		t.Fatal(err)
	}
	if reread.AccessToken != testAccessToken || reread.SecretAttributes["scope_hint"] != "sync" {
		t.Fatalf("decoded bundle = %#v", reread)
	}
}
