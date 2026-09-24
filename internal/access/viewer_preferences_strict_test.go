package access

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type failedViewerPreferences struct{ err error }

func (s failedViewerPreferences) ListSettingValuesForResolution(context.Context, userstore.SettingResolutionQuery) ([]userstore.SettingValue, error) {
	return nil, s.err
}
func TestViewerPreferencesStrictDoesNotDegrade(t *testing.T) {
	failure := errors.New("synthetic read failure")
	if _, err := ResolveViewerPreferencesStrict(t.Context(), failedViewerPreferences{failure}, "profile"); !errors.Is(err, failure) {
		t.Fatalf("strict reader swallowed error: %v", err)
	}
}
