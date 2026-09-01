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
