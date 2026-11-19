package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminSettingsInspection struct {
	effective bool
	reads     int
	err       error
}

func (f *fakeAdminSettingsInspection) InspectAdminSettings(_ context.Context, effective bool) (map[string]string, error) {
	f.effective = effective
	f.reads++
	return map[string]string{"server.log_level": "debug"}, f.err
}
func (f *fakeAdminSettingsInspection) InspectAdminSensitiveSettings(context.Context) (handlers.AdminSensitiveSettingsStatus, error) {
	f.reads++
	return handlers.AdminSensitiveSettingsStatus{}, f.err
}
