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

func TestPluginCredentialBundleIsOnlyWrittenForPluginProviders(t *testing.T) {
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(nil, cipher)
	for _, provider := range []string{"trakt", "simkl", "mdblist"} {
		encoded, err := repository.pluginCredentialsForConnection(Connection{
			Provider: provider, UserID: 7, ProfileID: "profile", AccessToken: testAccessToken,
		})
		if err != nil {
			t.Fatalf("pluginCredentialsForConnection(%q): %v", provider, err)
		}
		if encoded != "" {
			t.Fatalf("pluginCredentialsForConnection(%q) = %q, want empty", provider, encoded)
		}
	}
	encoded, err := repository.pluginCredentialsForConnection(Connection{
		Provider: "plugin:4:tracker", UserID: 7, ProfileID: "profile", AccessToken: testAccessToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "enc:v1:") {
		t.Fatalf("plugin credential bundle = %q, want encrypted value", encoded)
	}
}
