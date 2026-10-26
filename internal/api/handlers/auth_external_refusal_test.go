package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

// A sign-in with no account while account creation is off keeps v1's
// not_permitted answer; the cause lets the v2 login tell it apart.
func TestExternalSignInErrorKeepsV1NotPermitted(t *testing.T) {
	for _, err := range []error{auth.ErrNotPermitted, auth.ErrAccountRequired, fmt.Errorf("resolve: %w", auth.ErrAccountRequired)} {
		apiErr := externalSignInError(err)
		if apiErr == nil || apiErr.Status != http.StatusForbidden || apiErr.Code != "not_permitted" {
			t.Fatalf("%v: got %+v, want 403 not_permitted", err, apiErr)
		}
		if got, want := errors.Is(apiErr, auth.ErrAccountRequired), errors.Is(err, auth.ErrAccountRequired); got != want {
			t.Fatalf("%v: errors.Is(ErrAccountRequired) = %v, want %v", err, got, want)
		}
	}
}
