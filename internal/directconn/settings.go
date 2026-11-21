// Package directconn reports the API host's direct-connection readiness.
package directconn

import (
	"context"
	"strings"

	"github.com/Silo-Server/silo-server/internal/config"
)

// SettingsStore reads persisted administrator settings.
type SettingsStore interface {
	GetAll(context.Context) (map[string]string, error)
}

type settings struct {
	enabled, portMapping bool
	stunServers          string
}

func readSettings(ctx context.Context, store SettingsStore) (settings, error) {
	stored, err := store.GetAll(ctx)
	if err != nil {
		return settings{}, err
	}
	effective := config.EffectiveAdminSettings(stored)
	return settings{enabled: effective[config.DirectConnectionsEnabledSettingKey] == "true", portMapping: effective[config.DirectConnectionsPortMappingSettingKey] == "true", stunServers: effective[config.DirectConnectionsSTUNServersSettingKey]}, nil
}

func (s settings) servers() []string { return strings.Split(s.stunServers, ",") }
