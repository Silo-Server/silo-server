package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
	publicconfig "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/config"
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
// starting any plugin (notificationChannelsOf).
func (s *Service) NotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	installations, err := s.installations.ListEnabledWithCapabilityTypes(ctx, []string{capability.NotificationChannel})
	if err != nil {
		return nil, fmt.Errorf("list notification channel plugins: %w", err)
	}
	var candidates []notificationChannelCandidate
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
			if record != nil && record.Type == capability.NotificationChannel {
				candidates = append(candidates, notificationChannelCandidate{installation.ID, installation.PluginID, record})
			}
		}
	}
	return notificationChannelsOf(ctx, candidates), nil
}

// ListNotificationChannels is NotificationChannels read through q, which may
// be a transaction, so a caller holding one needs no second connection.
func ListNotificationChannels(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
},
) ([]NotificationChannel, error) {
	rows, err := q.Query(ctx, `SELECT i.id, i.plugin_id, c.`+strings.ReplaceAll(capabilityColumns, ", ", ", c.")+`
		FROM plugin_capabilities c
		JOIN plugin_installations i ON i.id = c.plugin_installation_id
		WHERE c.capability_type = $1 AND i.enabled AND i.kind <> $2
		ORDER BY i.id, c.capability_type, c.capability_id`, capability.NotificationChannel, KindBuiltin)
	if err != nil {
		return nil, fmt.Errorf("list notification channel plugins: %w", err)
	}
	defer rows.Close()
	var candidates []notificationChannelCandidate
	for rows.Next() {
		var c notificationChannelCandidate
		c.record = &Capability{Metadata: map[string]any{}}
		var metadata []byte
		if err := rows.Scan(&c.installationID, &c.pluginID, &c.record.InstallationID, &c.record.Type, &c.record.ID,
			&metadata, &c.record.CreatedAt, &c.record.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan notification channel capability: %w", err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &c.record.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshaling plugin capability metadata: %w", err)
			}
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notification channel plugins: %w", err)
	}
	return notificationChannelsOf(ctx, candidates), nil
}

// notificationChannelCandidate is a notification_channel.v1 record of an
// enabled, non-builtin installation.
type notificationChannelCandidate struct {
	installationID int
	pluginID       string
	record         *Capability
}

// notificationChannelsOf is the one rule for which channels exist, shared by
// listing a channel, connecting to it and queueing a message for it.
// candidates come oldest installation first.
//
// A capability whose record doesn't decode is skipped, and so is one whose
// settings form isn't the flat form the SDK requires or whose JSON Schema
// doesn't compile: install and upgrade refuse such a form, so only a record
// stored before that rule can have one, and the channel then counts as not
// provided.
//
// A channel is named by plugin and capability ID, and direct archive
// installs can leave two enabled installations of one plugin. Each channel
// is listed once, from the oldest enabled installation that provides it;
// NotificationChannelClientFor calls the same one, so a destination's
// settings are checked against the schema of the plugin that receives them.
func notificationChannelsOf(ctx context.Context, candidates []notificationChannelCandidate) []NotificationChannel {
	var out []NotificationChannel
	listed := make(map[[2]string]struct{})
	for _, c := range candidates {
		descriptor, err := DecodeCapability(c.record)
		if err == nil && descriptor.GetNotificationChannel() == nil {
			err = errors.New("not a notification channel descriptor")
		}
		if err == nil {
			err = usableDestinationSchema(descriptor.GetNotificationChannel().GetDestinationSchema(), c.record.ID)
		}
		if err != nil {
			slog.WarnContext(ctx, "skipping a notification channel this server can't offer", "component", "plugins",
				"plugin_id", c.pluginID, "capability_id", c.record.ID, "error", err)
			continue
		}
		key := [2]string{c.pluginID, c.record.ID}
		if _, dup := listed[key]; dup {
			continue
		}
		listed[key] = struct{}{}
		out = append(out, NotificationChannel{
			InstallationID: c.installationID,
			PluginID:       c.pluginID,
			CapabilityID:   c.record.ID,
			Descriptor:     descriptor.GetNotificationChannel(),
		})
	}
	return out
}

// usableDestinationSchema reports why a destination form can't be offered:
// it isn't the flat form the SDK requires, or its JSON Schema doesn't
// compile, so no value could ever be checked against it.
func usableDestinationSchema(schema *pluginv1.ConfigSchema, capabilityID string) error {
	if err := publicmanifest.ValidateDestinationSchema(schema); err != nil {
		return err
	}
	err := publicconfig.ValidateValue(schema, "destination", capabilityID, map[string]any{})
	if _, invalidValue := errors.AsType[*jsonschema.ValidationError](err); err != nil && !invalidValue {
		return fmt.Errorf("destination schema doesn't compile: %w", err)
	}
	return nil
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
