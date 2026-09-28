package jellycompat

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// A Jellyfin client cannot run the change a temporary password requires, so
// sign-in is refused with a reason the user can act on.
func TestLoginErrorExplainsTemporaryPassword(t *testing.T) {
	status, code, message := mapLoginError(mapAuthError(auth.ErrPasswordChangeRequired))
	if status != http.StatusUnauthorized || code != "InvalidUsernameOrPassword" || !strings.Contains(message, "temporary password") {
		t.Fatalf("mapLoginError = %d %q %q", status, code, message)
	}
}
