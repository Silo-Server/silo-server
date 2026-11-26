package auth

import (
	"context"
	"errors"
	"testing"
)

func TestPasswordPolicyCheck(t *testing.T) {
	t.Parallel()
	strict := PasswordPolicy{MinimumLength: 12, RequireUppercase: true, RequireSpecial: true}
	for _, tt := range []struct {
		name     string
		policy   PasswordPolicy
		password string
		want     error
		message  string
	}{
		{"default policy takes a plain password", DefaultPasswordPolicy(), "plainpassword", nil, ""},
		{"meets every rule", strict, "Correct-horse1", nil, ""},
		{"eleven characters", strict, "Correct-hor", ErrPasswordTooShort, "Use at least 12 characters."},
		{"no capital letter", strict, "correct-horse1", ErrPasswordNeedsUppercase, "Include at least one capital letter."},
		{"non-Latin capital counts", strict, "Ωcorrect-horse", nil, ""},
		{"no special character", strict, "Correcthorse1", ErrPasswordNeedsSpecial, "Include at least one special character, such as ! or #."},
		{"a space is not special", strict, "Correct horse1", ErrPasswordNeedsSpecial, "Include at least one special character, such as ! or #."},
		{"a symbol counts as special", strict, "Correcthorse€", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.policy.Check(tt.password)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Check(%q) error = %v, want %v", tt.password, err, tt.want)
			}
			if tt.want != nil && err.Error() != tt.message {
				t.Fatalf("Check(%q) message = %q, want %q", tt.password, err.Error(), tt.message)
			}
		})
	}
}

type policySettings map[string]string

func (s policySettings) Get(_ context.Context, key string) (string, error) { return s[key], nil }

func TestLoadPasswordPolicy(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		settings policySettings
		want     PasswordPolicy
	}{
		{"no rows is the default", policySettings{}, PasswordPolicy{MinimumLength: 8}},
		{"stored policy", policySettings{
			"auth.password_min_length":        " 14 ",
			"auth.password_require_uppercase": "true",
			"auth.password_require_special":   "true",
		}, PasswordPolicy{MinimumLength: 14, RequireUppercase: true, RequireSpecial: true}},
		{"a minimum below 8 reads as 8", policySettings{"auth.password_min_length": "4"}, PasswordPolicy{MinimumLength: 8}},
		{"a minimum above 72 reads as 8", policySettings{"auth.password_min_length": "99"}, PasswordPolicy{MinimumLength: 8}},
		{"garbage reads as the default", policySettings{"auth.password_min_length": "x", "auth.password_require_special": "maybe"}, PasswordPolicy{MinimumLength: 8}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := LoadPasswordPolicy(t.Context(), tt.settings)
			if err != nil || got != tt.want {
				t.Fatalf("LoadPasswordPolicy() = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

type failingSettings struct{}

func (failingSettings) Get(context.Context, string) (string, error) { return "", errors.New("down") }

func TestLoadPasswordPolicyFailsClosed(t *testing.T) {
	t.Parallel()
	if _, err := LoadPasswordPolicy(t.Context(), failingSettings{}); err == nil {
		t.Fatal("LoadPasswordPolicy() error = nil; an unreadable policy must refuse the password")
	}
}
