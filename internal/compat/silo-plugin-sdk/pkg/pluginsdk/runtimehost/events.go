package runtimehost

import (
	"context"
	"fmt"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func (c *Client) PublishEventToInstallation(ctx context.Context, targetInstallationID int, name string, payload map[string]any) error {
	if targetInstallationID <= 0 {
		return fmt.Errorf("runtimehost: target installation id is required")
	}
	if name == "" {
		return fmt.Errorf("runtimehost: event name is required")
	}
	pb, err := structFromMap("payload", payload)
	if err != nil {
		return err
	}
	_, err = c.rpc.PublishEventToInstallation(ctx, &pluginv1.PublishEventToInstallationRequest{
		TargetInstallationId: int64(targetInstallationID),
		EventName:            name,
		Payload:              pb,
	})
	return err
}
