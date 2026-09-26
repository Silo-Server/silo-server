package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"golang.org/x/crypto/bcrypt"
)

func passwordUser(t *testing.T, password string) *models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return &models.User{
		PasswordHash:              string(hash),
		LocalPasswordLoginEnabled: true,
	}
}

func TestValidatePasswordChange(t *testing.T) {
	t.Parallel()

	user := passwordUser(t, "current password")
	tests := []struct {
		name            string
		user            *models.User
		currentPassword string
		newPassword     string
		wantErr         error
	}{
		{name: "valid", user: user, currentPassword: "current password", newPassword: "new password"},
		{name: "unicode minimum counts characters", user: user, currentPassword: "current password", newPassword: "密码密码密码密码"},
		{name: "wrong current password", user: user, currentPassword: "wrong", newPassword: "new password", wantErr: ErrCurrentPasswordInvalid},
		{name: "too short", user: user, currentPassword: "current password", newPassword: "short", wantErr: ErrPasswordTooShort},
		{name: "bcrypt byte limit", user: user, currentPassword: "current password", newPassword: strings.Repeat("a", MaximumPasswordBytes+1), wantErr: ErrPasswordTooLong},
		{name: "local login disabled", user: &models.User{PasswordHash: user.PasswordHash}, currentPassword: "current password", newPassword: "new password", wantErr: ErrPasswordLoginDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePasswordChange(tt.user, tt.currentPassword, tt.newPassword)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validatePasswordChange() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateNewPasswordBoundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		password string
		want     error
	}{
		{"seven characters", "1234567", ErrPasswordTooShort},
		{"eight characters", "12345678", nil},
		{"seven unicode characters", strings.Repeat("界", 7), ErrPasswordTooShort},
		{"eight unicode characters", strings.Repeat("界", 8), nil},
		{"72 bytes", strings.Repeat("界", 24), nil},
		{"73 bytes", strings.Repeat("界", 24) + "a", ErrPasswordTooLong},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateNewPassword(tt.password); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRegistrationRejectsPasswordBeforeDependencies(t *testing.T) {
	t.Parallel()
	// No stores are wired: rejection must precede setup checks, invite redemption,
	// account creation, or any other storage access.
	s := &Service{}
	for _, password := range []string{"short", strings.Repeat("界", 25)} {
		want := ValidateNewPassword(password)
		if _, _, err := s.SetupInitialUser(t.Context(), "user", "user@example.test", password, true, "Home", "", ""); !errors.Is(err, want) {
			t.Fatalf("setup error = %v, want %v", err, want)
		}
		if _, _, err := s.Signup(t.Context(), "user", "user@example.test", password, "invite", true, "Home", "", ""); !errors.Is(err, want) {
			t.Fatalf("signup error = %v, want %v", err, want)
		}
	}
}
