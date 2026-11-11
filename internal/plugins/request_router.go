package plugins

import (
	"context"
	"errors"
	"fmt"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
)

// RequestRouterDescriptor returns the request_router.v1 descriptor an
// installation's manifest declares for capabilityID. It reads the capability
// metadata stored at install, so it never launches the plugin. A capability
// without a descriptor, as in every plugin built before the SDK carried one,
// or one the installation does not declare, yields an empty descriptor: every
// optional request-router feature off.
func (s *Service) RequestRouterDescriptor(ctx context.Context, installationID int, capabilityID string) (*pluginv1.RequestRouterDescriptor, error) {
	descriptor, err := s.requestRouterCapability(ctx, installationID, capabilityID)
	if err != nil {
		return nil, err
	}
	if router := descriptor.GetRequestRouter(); router != nil {
		return router, nil
	}
	return &pluginv1.RequestRouterDescriptor{}, nil
}

// RequestRouterFieldLabels returns the label of each field in the admin forms
// of an installation's request router capability, keyed by config key. Like
// RequestRouterDescriptor it reads stored metadata only.
func (s *Service) RequestRouterFieldLabels(ctx context.Context, installationID int, capabilityID string) (map[string]string, error) {
	descriptor, err := s.requestRouterCapability(ctx, installationID, capabilityID)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for _, schema := range descriptor.GetConfigSchema() {
		for _, field := range schema.GetAdminForm().GetFields() {
			labels[field.GetKey()] = field.GetLabel()
		}
	}
	return labels, nil
}

// requestRouterCapability returns the stored descriptor of an installation's
// request router capability, or nil when the installation declares none.
func (s *Service) requestRouterCapability(ctx context.Context, installationID int, capabilityID string) (*pluginv1.CapabilityDescriptor, error) {
	if s == nil || s.installations == nil {
		return nil, errors.New("plugin installations are not configured")
	}
	records, err := s.installations.ListCapabilities(ctx, installationID)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record == nil || record.Type != capability.RequestRouter || record.ID != capabilityID {
			continue
		}
		descriptor, err := DecodeCapability(record)
		if err != nil {
			return nil, fmt.Errorf("decode request router capability %q of installation %d: %w", capabilityID, installationID, err)
		}
		return descriptor, nil
	}
	return nil, nil
}
