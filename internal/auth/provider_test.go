package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/models"
)

// looksLikeEmail gates the email-column fallback in
// LocalProvider.Authenticate: only inputs that parse as a bare address may
// trigger the second lookup, so plain usernames keep the exact pre-fallback
// behavior (one lookup, one failure mode).
func TestLooksLikeEmail(t *testing.T) {
	valid := []string{
		"marco@example.com",
		"anna.k+silo@sub.example.co.uk",
		" marco@example.com ", // surrounding whitespace is trimmed
	}
	for _, input := range valid {
		if !looksLikeEmail(input) {
			t.Errorf("looksLikeEmail(%q) = false, want true", input)
		}
	}

	invalid := []string{
		"marco",                       // ordinary username
		"marco@",                      // no domain
		"@example.com",                // no local part
		"Marco <marco@example.com>",   // display-name form, not a bare address
		"marco@example.com, b@x.com",  // address list
		"marco example@example.com x", // trailing junk
		"",
	}
	for _, input := range invalid {
		if looksLikeEmail(input) {
			t.Errorf("looksLikeEmail(%q) = true, want false", input)
		}
	}
}

// loginDirectory is a LoginDirectory over a fixed set of accounts.
type loginDirectory map[string]*models.User

func (d loginDirectory) GetByUsername(_ context.Context, username string) (*models.User, error) {
	if user, ok := d[username]; ok {
		return user, nil
	}
	return nil, ErrNotFound
}

func (d loginDirectory) GetByEmail(_ context.Context, email string) (*models.User, error) {
	for _, user := range d {
		if user.Email == email {
			return user, nil
		}
	}
	return nil, ErrNotFound
}

// A password sign-in compares one bcrypt hash whatever the outcome, so an
// unknown name or an account without local password sign-in is rejected in
// the same time as a wrong password. Not parallel: it swaps the package's
// comparePasswordHash.
func TestCheckLocalPasswordComparesOneHash(t *testing.T) {
	const password = "correct horse battery"
	local := passwordUser(t, password)
	local.Username, local.Email = "anna", "anna@example.test"
	external := passwordUser(t, password)
	external.Username, external.Email, external.LocalPasswordLoginEnabled = "marco", "marco@example.test", false
	users := loginDirectory{local.Username: local, external.Username: external}

	placeholder := placeholderPasswordHash()
	if cost, err := bcrypt.Cost(placeholder); err != nil || cost != passwordHashCost {
		t.Fatalf("placeholder hash cost = %d, %v; want %d", cost, err, passwordHashCost)
	}

	var compared [][]byte
	t.Cleanup(func() { comparePasswordHash = bcrypt.CompareHashAndPassword })
	comparePasswordHash = func(hash, plain []byte) error {
		compared = append(compared, hash)
		return bcrypt.CompareHashAndPassword(hash, plain)
	}

	tests := []struct {
		name     string
		username string
		password string
		wantHash []byte
		wantUser *models.User
		wantErr  error
	}{
		{name: "unknown name", username: "nobody", password: password, wantHash: placeholder, wantErr: ErrInvalidCredentials},
		{name: "unknown email", username: "nobody@example.test", password: password, wantHash: placeholder, wantErr: ErrInvalidCredentials},
		{name: "no local password sign-in", username: external.Username, password: password, wantHash: placeholder, wantErr: ErrInvalidCredentials},
		{name: "wrong password", username: local.Username, password: "wrong password", wantHash: []byte(local.PasswordHash), wantErr: ErrInvalidCredentials},
		{name: "right password", username: local.Username, password: password, wantHash: []byte(local.PasswordHash), wantUser: local},
		{name: "right password by email", username: local.Email, password: password, wantHash: []byte(local.PasswordHash), wantUser: local},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compared = nil
			user, err := checkLocalPassword(t.Context(), users, Credentials{Username: tt.username, Password: tt.password})
			if !errors.Is(err, tt.wantErr) || user != tt.wantUser {
				t.Fatalf("checkLocalPassword() = %v, %v; want %v, %v", user, err, tt.wantUser, tt.wantErr)
			}
			if len(compared) != 1 || !bytes.Equal(compared[0], tt.wantHash) {
				t.Fatalf("compared hashes %q, want exactly %q", compared, tt.wantHash)
			}
		})
	}
}
