package handlers

import "testing"

func TestDateTimeFormatSettingValidation(t *testing.T) {
	valid := map[string][]string{
		dateFormatSettingKey: {"auto", "DD/MM/YYYY", "MM/DD/YYYY", "YYYY-MM-DD"},
		timeFormatSettingKey: {"auto", "12h", "24h"},
	}
	for key, values := range valid {
		for _, value := range values {
			if err := validateRegisteredSetting(key, value, scopeUser); err != nil {
				t.Errorf("expected %s=%q to validate, got %v", key, value, err)
			}
		}
	}

	invalid := map[string][]string{
		dateFormatSettingKey: {"", "YYYY/MM/DD", "dd/mm/yyyy", "iso"},
		timeFormatSettingKey: {"", "12", "24", "12H"},
	}
	for key, values := range invalid {
		for _, value := range values {
			if err := validateRegisteredSetting(key, value, scopeUser); err == nil {
				t.Errorf("expected %s=%q to be rejected", key, value)
			}
		}
	}

	for _, key := range []string{dateFormatSettingKey, timeFormatSettingKey} {
		if !keyUsesUserScope(key) {
			t.Errorf("expected %s to be user-scoped", key)
		}
		if err := validateRegisteredSetting(key, "auto", scopeDevice); err == nil {
			t.Errorf("expected %s to reject device scope", key)
		}
	}
}
