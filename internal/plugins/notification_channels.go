package plugins

import (
	"context"
	"fmt"
	"log/slog"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// NotificationChannel is one notification_channel.v1 capability of an
// enabled installation: a channel profiles can add destinations on.
type NotificationChannel struct {
	InstallationID int
	PluginID       string
	CapabilityID   string
	Descriptor     *pluginv1.NotificationChannelDescriptor
}

// NotificationChannels lists the notification_channel.v1 capabilities of
// enabled installations, from their stored capability records, without
// starting any plugin. A capability whose record doesn't decode is skipped,
// and so is one whose settings form isn't the flat form the SDK requires:
// install and upgrade refuse such a form, so only a record stored before
// that rule can have one.
func (s *Service) NotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	installations, err := s.installations.ListEnabledWithCapabilityTypes(ctx, []string{capability.NotificationChannel})
	if err != nil {
		return nil, fmt.Errorf("list notification channel plugins: %w", err)
	}
	var out []NotificationChannel
	for _, installation := range installations {
		if installation.IsBuiltin() {
			continue
		}
		capabilities, err := s.installations.ListCapabilities(ctx, installation.ID)
		if err != nil {
			return nil, fmt.Errorf("list plugin capabilities: %w", err)
		}
		for _, record := range capabilities {
			if record == nil || record.Type != capability.NotificationChannel {
				continue
			}
			descriptor, err := DecodeCapability(record)
			if err != nil || descriptor.GetNotificationChannel() == nil {
				slog.WarnContext(ctx, "skipping a notification channel whose descriptor doesn't decode", "component", "plugins",
					"plugin_id", installation.PluginID, "capability_id", record.ID, "error", err)
				continue
			}
			if err := publicmanifest.ValidateDestinationSchema(descriptor.GetNotificationChannel().GetDestinationSchema()); err != nil {
				slog.WarnContext(ctx, "skipping a notification channel whose settings form isn't flat", "component", "plugins",
					"plugin_id", installation.PluginID, "capability_id", record.ID, "error", err)
				continue
			}
			out = append(out, NotificationChannel{
				InstallationID: installation.ID,
				PluginID:       installation.PluginID,
				CapabilityID:   record.ID,
				Descriptor:     descriptor.GetNotificationChannel(),
			})
		}
	}
	return out, nil
}

// NotificationChannelClientFor returns the client for a plugin's
// notification channel, by plugin and capability ID, starting the plugin
// when needed. It returns ErrInstallationNotFound when no enabled
// installation of the plugin provides the capability.
func (s *Service) NotificationChannelClientFor(ctx context.Context, pluginID, capabilityID string) (*pluginhost.NotificationChannelClient, error) {
	installations, err := s.installations.ListByPluginID(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	for _, installation := range installations {
		if !installation.Enabled || installation.IsBuiltin() {
			continue
		}
		return s.NotificationChannelClient(ctx, installation.ID, capabilityID)
	}
	return nil, ErrInstallationNotFound
}
