package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

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
//
// A channel is named by plugin and capability ID, and direct archive
// installs can leave two enabled installations of one plugin. Each channel
// is listed once, from the oldest enabled installation that provides it;
// NotificationChannelClientFor calls the same one, so a destination's
// settings are checked against the schema of the plugin that receives them.
func (s *Service) NotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	installations, err := s.installations.ListEnabledWithCapabilityTypes(ctx, []string{capability.NotificationChannel})
	if err != nil {
		return nil, fmt.Errorf("list notification channel plugins: %w", err)
	}
	var out []NotificationChannel
	listed := make(map[[2]string]struct{})
	// installations come oldest first, by ID.
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
			key := [2]string{installation.PluginID, record.ID}
			if _, dup := listed[key]; dup {
				continue
			}
			listed[key] = struct{}{}
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

// NotificationChannelClientFor returns the channel a plugin and capability
// ID name, as NotificationChannels lists it, and a client for the
// installation that provides it, starting the plugin when needed. It returns
// ErrInstallationNotFound when no enabled installation provides the
// channel. A plugin start continues without the caller, so it returns when
// ctx ends even while the plugin is still starting.
func (s *Service) NotificationChannelClientFor(ctx context.Context, pluginID, capabilityID string) (*NotificationChannel, *pluginhost.NotificationChannelClient, error) {
	channels, err := s.NotificationChannels(ctx)
	if err != nil {
		return nil, nil, err
	}
	idx := slices.IndexFunc(channels, func(c NotificationChannel) bool {
		return c.PluginID == pluginID && c.CapabilityID == capabilityID
	})
	if idx < 0 {
		return nil, nil, ErrInstallationNotFound
	}
	channel := channels[idx]
	type started struct {
		client *pluginhost.NotificationChannelClient
		err    error
	}
	done := make(chan started, 1)
	go func() {
		client, err := s.NotificationChannelClient(ctx, channel.InstallationID, capabilityID)
		done <- started{client, err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			return nil, nil, result.err
		}
		return &channel, result.client, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}
