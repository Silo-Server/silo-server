package main

import (
	"context"
	"fmt"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
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
		schema := channel.Descriptor.GetDestinationSchema()
		_, secrets := plugins.ConfigSchemaFieldSets(schema)
		out = append(out, notifications.PluginChannel{
			PluginChannelRef: notifications.PluginChannelRef{PluginID: channel.PluginID, CapabilityID: channel.CapabilityID},
			PluginName:       channel.PluginID,
			DisplayName:      channel.Descriptor.GetDisplayName(),
			Description:      channel.Descriptor.GetDescription(),
			Schema:           schema,
			SecretFields:     secrets,
		})
	}
	return out, nil
}

func (h pluginChannelHost) client(ctx context.Context, ref notifications.PluginChannelRef) (*pluginhost.NotificationChannelClient, error) {
	client, err := h.plugins.NotificationChannelClientFor(ctx, ref.PluginID, ref.CapabilityID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", notifications.ErrPluginChannelUnavailable, err)
	}
	return client, nil
}

func (h pluginChannelHost) ValidateDestination(ctx context.Context, ref notifications.PluginChannelRef, req *pluginv1.ValidateNotificationDestinationRequest) (*pluginv1.ValidateNotificationDestinationResponse, error) {
	client, err := h.client(ctx, ref)
	if err != nil {
		return nil, err
	}
	return client.ValidateDestination(ctx, req)
}

func (h pluginChannelHost) SendNotification(ctx context.Context, ref notifications.PluginChannelRef, req *pluginv1.SendNotificationRequest) (*pluginv1.SendNotificationResponse, error) {
	client, err := h.client(ctx, ref)
	if err != nil {
		return nil, err
	}
	return client.SendNotification(ctx, req)
}
