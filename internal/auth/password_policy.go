package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/config"
)

var (
	// ErrPasswordNeedsUppercase refuses a new password without a capital
	// letter while the policy requires one.
	ErrPasswordNeedsUppercase = errors.New("password needs a capital letter")
	// ErrPasswordNeedsSpecial refuses a new password without a special
	// character while the policy requires one.
	ErrPasswordNeedsSpecial = errors.New("password needs a special character")
)

// PasswordPolicy is the server's rule set for a new local password
// (Settings → Sign-in). It applies when a password is chosen or replaced:
// setup, signup, a password change, a reset, and an administrator setting
// one. Passwords already stored keep working.
type PasswordPolicy struct {
	// MinimumLength counts characters, never fewer than MinimumPasswordLength.
	MinimumLength int
	// RequireUppercase asks for a capital letter (Unicode category Lu).
	RequireUppercase bool
	// RequireSpecial asks for a punctuation mark or symbol (Unicode
	// categories P and S); letters, digits and spaces do not count.
	RequireSpecial bool
}

// DefaultPasswordPolicy is the policy of a server with no policy settings.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{MinimumLength: MinimumPasswordLength}
}

// passwordRuleError is a refused password: errors.Is matches the rule's
// sentinel, and Error is the English sentence a person reads.
type passwordRuleError struct {
	rule    error
	message string
}

func (e *passwordRuleError) Error() string { return e.message }
func (e *passwordRuleError) Unwrap() error { return e.rule }

// PasswordRuleMessage reports whether err is a password the policy refused,
// with the English sentence that says which rule it broke.
func PasswordRuleMessage(err error) (string, bool) {
	var rule *passwordRuleError
	if errors.As(err, &rule) {
		return rule.message, true
	}
	return "", false
}

// Check reports the first rule password breaks, or nil. The 72-byte bcrypt
// limit applies under every policy.
func (p PasswordPolicy) Check(password string) error {
	minimum := max(p.MinimumLength, MinimumPasswordLength)
	if utf8.RuneCountInString(password) < minimum {
		return &passwordRuleError{ErrPasswordTooShort, fmt.Sprintf("Use at least %d characters.", minimum)}
	}
	if len(password) > MaximumPasswordBytes {
		return &passwordRuleError{ErrPasswordTooLong, fmt.Sprintf("Use at most %d bytes.", MaximumPasswordBytes)}
	}
	if p.RequireUppercase && !strings.ContainsFunc(password, unicode.IsUpper) {
		return &passwordRuleError{ErrPasswordNeedsUppercase, "Include at least one capital letter."}
	}
	if p.RequireSpecial && !strings.ContainsFunc(password, isSpecialPasswordRune) {
		return &passwordRuleError{ErrPasswordNeedsSpecial, "Include at least one special character, such as ! or #."}
	}
	return nil
}

func isSpecialPasswordRune(r rune) bool {
	return unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// passwordPolicyKeys are the settings LoadPasswordPolicy reads.
var passwordPolicyKeys = []string{
	config.AuthPasswordMinLengthSettingKey,
	config.AuthPasswordRequireUppercaseSettingKey,
	config.AuthPasswordRequireSpecialSettingKey,
}

// LoadPasswordPolicy reads the policy settings. They are read on every
// check, so a change made on any node applies at once everywhere. A value
// normalization would refuse reads as its default.
func LoadPasswordPolicy(ctx context.Context, settings SettingsGetter) (PasswordPolicy, error) {
	policy := DefaultPasswordPolicy()
	if settings == nil {
		return policy, nil
	}
	values, err := readPasswordPolicySettings(ctx, settings)
	if err != nil {
		return PasswordPolicy{}, err
	}
	key := config.AuthPasswordMinLengthSettingKey
	if length, err := config.NormalizeAdminSetting(key, values[key]); err == nil {
		policy.MinimumLength, _ = strconv.Atoi(length)
	}
	policy.RequireUppercase = settingBool(values[config.AuthPasswordRequireUppercaseSettingKey], false)
	policy.RequireSpecial = settingBool(values[config.AuthPasswordRequireSpecialSettingKey], false)
	return policy, nil
}

func readPasswordPolicySettings(ctx context.Context, settings SettingsGetter) (map[string]string, error) {
	// One query reads the keys as one consistent snapshot.
	if batch, ok := settings.(config.ManySettingReader); ok {
		values, err := batch.GetMany(ctx, passwordPolicyKeys...)
		if err != nil {
			return nil, fmt.Errorf("reading password policy settings: %w", err)
		}
		return values, nil
	}
	values := make(map[string]string, len(passwordPolicyKeys))
	for _, key := range passwordPolicyKeys {
		value, err := settings.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("reading setting %s: %w", key, err)
		}
		values[key] = value
	}
	return values, nil
}

// CheckNewPassword applies the server's password policy, read from settings,
// before a new password is persisted. Every path that lets a person choose a
// password calls it first; a nil settings reader applies the default policy.
func CheckNewPassword(ctx context.Context, settings SettingsGetter, password string) error {
	policy, err := LoadPasswordPolicy(ctx, settings)
	if err != nil {
		return err
	}
	return policy.Check(password)
}
