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

// Sign-in policy refusals from external sign-in reach a Jellyfin client as a
// failed sign-in that names the reason; a provider that cannot answer is a
// 503, not a credentials failure.
func TestLoginErrorMapsExternalSignInRefusals(t *testing.T) {
	if status, code, message := mapLoginError(mapAuthError(auth.ErrProviderUnavailable)); status != http.StatusServiceUnavailable ||
		code != "ServiceUnavailable" || message == "" {
		t.Fatalf("provider unavailable = %d %q %q", status, code, message)
	}
	for _, err := range []error{auth.ErrLocalLoginDisabled, auth.ErrNotPermitted, auth.ErrEmailInUse,
		auth.ErrIdentityLinkedElsewhere, auth.ErrProviderPasswordExpired} {
		status, code, message := mapLoginError(mapAuthError(err))
		if status != http.StatusUnauthorized || code != "InvalidUsernameOrPassword" || message != err.Error() {
			t.Fatalf("%v = %d %q %q", err, status, code, message)
		}
	}
	if status, _, message := mapLoginError(mapAuthError(auth.ErrInvalidCredentials)); status != http.StatusUnauthorized ||
		message != "Invalid username or password" {
		t.Fatalf("invalid credentials = %d %q", status, message)
	}
}
