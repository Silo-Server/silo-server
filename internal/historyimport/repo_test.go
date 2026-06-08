package historyimport

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// newTestRepo builds a Repository with a real cipher for scan-method tests.
func newTestRepo(t *testing.T) *Repository {
	t.Helper()
	key := make([]byte, 48)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	c, err := secret.New(key)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	return &Repository{cipher: c}
}

type plexSessionScannerStub struct {
	authToken  *string
	servers    []PlexServer
	serversRaw []byte
}

func (s plexSessionScannerStub) Scan(dest ...any) error {
	if len(dest) != 10 {
		return fmt.Errorf("unexpected scan dest count: %d", len(dest))
	}

	id, ok := dest[0].(*string)
	if !ok {
		return fmt.Errorf("dest[0] type = %T, want *string", dest[0])
	}
	*id = "plex-session-1"

	userID, ok := dest[1].(*int)
	if !ok {
		return fmt.Errorf("dest[1] type = %T, want *int", dest[1])
	}
	*userID = 42

	pinID, ok := dest[2].(*string)
	if !ok {
		return fmt.Errorf("dest[2] type = %T, want *string", dest[2])
	}
	*pinID = "12345"

	pinCode, ok := dest[3].(*string)
	if !ok {
		return fmt.Errorf("dest[3] type = %T, want *string", dest[3])
	}
	*pinCode = "pin-code"

	if authToken, ok := dest[4].(*string); ok {
		if s.authToken == nil {
			return fmt.Errorf("can't scan into dest[4] (col: auth_token): cannot scan NULL into *string")
		}
		*authToken = *s.authToken
	} else if authToken, ok := dest[4].(**string); ok {
		*authToken = s.authToken
	} else {
		return fmt.Errorf("dest[4] type = %T, want *string or **string", dest[4])
	}

	serversJSON := s.serversRaw
	if serversJSON == nil {
		var err error
		serversJSON, err = json.Marshal(s.servers)
		if err != nil {
			return err
		}
	}
	serversDest, ok := dest[5].(*[]byte)
	if !ok {
		return fmt.Errorf("dest[5] type = %T, want *[]byte", dest[5])
	}
	*serversDest = serversJSON

	expiresAt, ok := dest[6].(*time.Time)
	if !ok {
		return fmt.Errorf("dest[6] type = %T, want *time.Time", dest[6])
	}
	*expiresAt = time.Date(2026, time.April, 1, 13, 0, 0, 0, time.UTC)

	consumedAt, ok := dest[7].(**time.Time)
	if !ok {
		return fmt.Errorf("dest[7] type = %T, want **time.Time", dest[7])
	}
	*consumedAt = nil

	createdAt, ok := dest[8].(*time.Time)
	if !ok {
		return fmt.Errorf("dest[8] type = %T, want *time.Time", dest[8])
	}
	*createdAt = time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC)

	updatedAt, ok := dest[9].(*time.Time)
	if !ok {
		return fmt.Errorf("dest[9] type = %T, want *time.Time", dest[9])
	}
	*updatedAt = time.Date(2026, time.April, 1, 12, 30, 0, 0, time.UTC)

	return nil
}

func TestScanPlexSession_AllowsNullAuthToken(t *testing.T) {
	t.Parallel()

	session, err := newTestRepo(t).scanPlexSession(plexSessionScannerStub{
		authToken: nil,
		servers: []PlexServer{{
			Name:             "Plex Server",
			ClientIdentifier: "server-1",
			AccessToken:      "server-token",
			RemoteURL:        "https://plex.example",
			LocalURL:         "http://192.168.1.2:32400",
			Owned:            true,
			HasRemoteURL:     true,
			HasLocalURL:      true,
		}},
	})
	if err != nil {
		t.Fatalf("scanPlexSession returned error for NULL auth_token: %v", err)
	}

	if session.AuthToken != "" {
		t.Fatalf("session.AuthToken = %q, want empty string", session.AuthToken)
	}
	if got := len(session.Servers); got != 1 {
		t.Fatalf("len(session.Servers) = %d, want 1", got)
	}
	if session.Servers[0].ClientIdentifier != "server-1" {
		t.Fatalf("session.Servers[0].ClientIdentifier = %q, want server-1", session.Servers[0].ClientIdentifier)
	}
	if session.Servers[0].AccessToken != "server-token" {
		t.Fatalf("session.Servers[0].AccessToken = %q, want server-token", session.Servers[0].AccessToken)
	}
}

type connectSessionScannerStub struct {
	accessToken string
	servers     []ConnectServer
	serversRaw  []byte
}

func (s connectSessionScannerStub) Scan(dest ...any) error {
	if len(dest) != 9 {
		return fmt.Errorf("unexpected scan dest count: %d", len(dest))
	}

	id, ok := dest[0].(*string)
	if !ok {
		return fmt.Errorf("dest[0] type = %T, want *string", dest[0])
	}
	*id = "connect-session-1"

	userID, ok := dest[1].(*int)
	if !ok {
		return fmt.Errorf("dest[1] type = %T, want *int", dest[1])
	}
	*userID = 42

	connectUserID, ok := dest[2].(*string)
	if !ok {
		return fmt.Errorf("dest[2] type = %T, want *string", dest[2])
	}
	*connectUserID = "connect-user-1"

	accessToken, ok := dest[3].(*string)
	if !ok {
		return fmt.Errorf("dest[3] type = %T, want *string", dest[3])
	}
	*accessToken = s.accessToken

	serversJSON := s.serversRaw
	if serversJSON == nil {
		var err error
		serversJSON, err = json.Marshal(s.servers)
		if err != nil {
			return err
		}
	}
	serversDest, ok := dest[4].(*[]byte)
	if !ok {
		return fmt.Errorf("dest[4] type = %T, want *[]byte", dest[4])
	}
	*serversDest = serversJSON

	expiresAt, ok := dest[5].(*time.Time)
	if !ok {
		return fmt.Errorf("dest[5] type = %T, want *time.Time", dest[5])
	}
	*expiresAt = time.Date(2026, time.April, 1, 13, 0, 0, 0, time.UTC)

	consumedAt, ok := dest[6].(**time.Time)
	if !ok {
		return fmt.Errorf("dest[6] type = %T, want **time.Time", dest[6])
	}
	*consumedAt = nil

	createdAt, ok := dest[7].(*time.Time)
	if !ok {
		return fmt.Errorf("dest[7] type = %T, want *time.Time", dest[7])
	}
	*createdAt = time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC)

	updatedAt, ok := dest[8].(*time.Time)
	if !ok {
		return fmt.Errorf("dest[8] type = %T, want *time.Time", dest[8])
	}
	*updatedAt = time.Date(2026, time.April, 1, 12, 30, 0, 0, time.UTC)

	return nil
}

func TestConnectSessionServersEncryptedAtRestAndDecryptedOnRead(t *testing.T) {
	t.Parallel()

	repo := newTestRepo(t)
	servers := []ConnectServer{{
		ID:           "emby-server-1",
		Name:         "Emby Server",
		AccessKey:    "server-access-key",
		URL:          "https://emby.example",
		HasRemoteURL: true,
	}}
	raw, err := repo.marshalConnectServersForWrite("connect-session-1", servers)
	if err != nil {
		t.Fatalf("marshalConnectServersForWrite: %v", err)
	}
	if bytes.Contains(raw, []byte("server-access-key")) {
		t.Fatalf("stored connect servers JSON leaked access key: %s", raw)
	}

	session, err := repo.scanConnectSession(connectSessionScannerStub{
		accessToken: "connect-token",
		serversRaw:  raw,
	})
	if err != nil {
		t.Fatalf("scanConnectSession: %v", err)
	}
	if got := session.Servers[0].AccessKey; got != "server-access-key" {
		t.Fatalf("session.Servers[0].AccessKey = %q, want server-access-key", got)
	}
}

func TestPlexSessionServersEncryptedAtRestAndDecryptedOnRead(t *testing.T) {
	t.Parallel()

	repo := newTestRepo(t)
	servers := []PlexServer{{
		Name:             "Plex Server",
		ClientIdentifier: "server-1",
		AccessToken:      "server-token",
		RemoteURL:        "https://plex.example",
		Owned:            true,
		HasRemoteURL:     true,
	}}
	raw, err := repo.marshalPlexServersForWrite("plex-session-1", servers)
	if err != nil {
		t.Fatalf("marshalPlexServersForWrite: %v", err)
	}
	if bytes.Contains(raw, []byte("server-token")) {
		t.Fatalf("stored plex servers JSON leaked access token: %s", raw)
	}

	session, err := repo.scanPlexSession(plexSessionScannerStub{
		authToken:  nil,
		serversRaw: raw,
	})
	if err != nil {
		t.Fatalf("scanPlexSession: %v", err)
	}
	if got := session.Servers[0].AccessToken; got != "server-token" {
		t.Fatalf("session.Servers[0].AccessToken = %q, want server-token", got)
	}
}

func TestSourceAdminTokenEncryptionUsesSourceAAD(t *testing.T) {
	t.Parallel()

	repo := newTestRepo(t)
	ciphertext, err := repo.encryptSourceAdminToken(42, "admin-token")
	if err != nil {
		t.Fatalf("encryptSourceAdminToken: %v", err)
	}
	if ciphertext == "admin-token" || bytes.Contains([]byte(ciphertext), []byte("admin-token")) {
		t.Fatalf("encrypted admin token leaked plaintext: %q", ciphertext)
	}
	plaintext, err := repo.cipher.Decrypt(ciphertext, sourceAdminTokenAAD(42))
	if err != nil {
		t.Fatalf("decrypt with matching source AAD: %v", err)
	}
	if plaintext != "admin-token" {
		t.Fatalf("decrypted admin token = %q, want admin-token", plaintext)
	}
	if _, err := repo.cipher.Decrypt(ciphertext, sourceAdminTokenAAD(43)); err == nil {
		t.Fatalf("decrypt with different source AAD succeeded, want authentication failure")
	}
}
