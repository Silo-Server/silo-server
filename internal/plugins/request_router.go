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
		if router := descriptor.GetRequestRouter(); router != nil {
			return router, nil
		}
		break
	}
	return &pluginv1.RequestRouterDescriptor{}, nil
}
