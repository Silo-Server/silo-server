package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// pluginChannelHost lets the notification system reach notification_channel.v1
// plugins through the plugin service, without either package importing the
// other.
type pluginChannelHost struct {
	plugins *plugins.Service
}

func (h pluginChannelHost) Channels(ctx context.Context) ([]notifications.PluginChannel, error) {
	channels, err := h.plugins.NotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]notifications.PluginChannel, 0, len(channels))
	for _, channel := range channels {
		out = append(out, pluginChannelOf(channel))
	}
	return out, nil
}

func pluginChannelOf(channel plugins.NotificationChannel) notifications.PluginChannel {
	schema := channel.Descriptor.GetDestinationSchema()
	public, secrets := plugins.ConfigSchemaFieldSets(schema)
	return notifications.PluginChannel{
		PluginChannelRef: notifications.PluginChannelRef{PluginID: channel.PluginID, CapabilityID: channel.CapabilityID},
		DisplayName:      channel.Descriptor.GetDisplayName(),
		Description:      channel.Descriptor.GetDescription(),
		Schema:           schema,
		PublicFields:     public,
		SecretFields:     secrets,
	}
}

func (h pluginChannelHost) AvailableChannels(ctx context.Context, tx pgx.Tx) ([]notifications.PluginChannelRef, error) {
	channels, err := plugins.ListNotificationChannels(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]notifications.PluginChannelRef, 0, len(channels))
	for _, channel := range channels {
		out = append(out, notifications.PluginChannelRef{PluginID: channel.PluginID, CapabilityID: channel.CapabilityID})
	}
	return out, nil
}

func (h pluginChannelHost) Connect(ctx context.Context, ref notifications.PluginChannelRef) (*notifications.PluginChannel, notifications.PluginChannelClient, error) {
	channel, client, err := h.plugins.NotificationChannelClientFor(ctx, ref.PluginID, ref.CapabilityID)
	if errors.Is(err, plugins.ErrInstallationNotFound) {
		return nil, nil, fmt.Errorf("%w: %w: %w", notifications.ErrPluginChannelUnavailable, notifications.ErrPluginChannelNotProvided, err)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", notifications.ErrPluginChannelUnavailable, err)
	}
	out := pluginChannelOf(*channel)
	return &out, client, nil
}
