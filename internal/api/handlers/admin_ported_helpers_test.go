package handlers

import (
	"errors"
	"testing"
)

// saveAdminSetting saves one key through the batch or the single-key settings
// write, the two service methods the native settings operations call.
func saveAdminSetting(t *testing.T, h *AdminHandler, key, value string, single bool) error {
	t.Helper()
	if single {
		_, err := h.UpdateAdminSetting(t.Context(), key, value, nil)
		return err
	}
	_, err := h.UpdateAdminSettings(t.Context(), map[string]string{key: value}, nil)
	return err
}

// adminErrorStatus is the HTTP status a settings service error maps to: the
// APIError's own status, or 500 for anything else.
func adminErrorStatus(err error) int {
	if err == nil {
		return 200
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		return apiErr.Status
	}
	return 500
}

// requireAdminError fails unless err is an APIError with status and, when
// code is not empty, that code.
func requireAdminError(t *testing.T, err error, status int, code string) {
	t.Helper()
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != status || (code != "" && apiErr.Code != code) {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
}

// effectiveSettingsOf is what the effective settings read returns.
func effectiveSettingsOf(t *testing.T, h *AdminHandler) map[string]string {
	t.Helper()
	values, err := h.InspectAdminSettings(t.Context(), true)
	if err != nil {
		t.Fatalf("InspectAdminSettings: %v", err)
	}
	if _, leaked := values["redis.url"]; leaked {
		t.Fatal("effective settings leaked redis.url")
	}
	return values
}
