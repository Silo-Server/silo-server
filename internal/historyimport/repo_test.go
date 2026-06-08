package historyimport

import (
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
	authToken *string
	servers   []PlexServer
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

	serversJSON, err := json.Marshal(s.servers)
	if err != nil {
		return err
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
}
