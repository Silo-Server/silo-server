package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/auth/passwordhash"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrNoPasswordConflict refuses a request for an account with no password
// that also names a password or turns local sign-in on.
var ErrNoPasswordConflict = errors.New("an account with no password takes no password and no local sign-in")

// newUserPassword is the password_hash and local_password_login_enabled a
// new account stores. Local sign-in defaults to on. An account with no
// password (input.NoPassword) stores NULL and local sign-in off, never a hash
// of the empty string.
func newUserPassword(ctx context.Context, input models.CreateUserInput) (*string, bool, error) {
	local := input.LocalPasswordLoginEnabled == nil || *input.LocalPasswordLoginEnabled
	if input.NoPassword {
		if input.Password != "" || input.LocalPasswordLoginEnabled != nil && local {
			return nil, false, ErrNoPasswordConflict
		}
		return nil, false, nil
	}
	hash, err := passwordhash.Generate(ctx, []byte(input.Password), passwordHashCost)
	if err != nil {
		return nil, false, fmt.Errorf("hashing password: %w", err)
	}
	return new(string(hash)), local, nil
}
