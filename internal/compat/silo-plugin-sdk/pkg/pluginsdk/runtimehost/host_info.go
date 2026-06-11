package runtimehost

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type HostInfo struct {
	PublicBaseURL      string
	InternalBaseURL    string
	PluginProxyBaseURL string
}

func (c *Client) GetHostInfo(ctx context.Context) (*HostInfo, error) {
	resp, err := c.rpc.GetHostInfo(ctx, &pluginv1.GetHostInfoRequest{})
	if err != nil {
		return nil, err
	}
	return &HostInfo{
		PublicBaseURL:      resp.GetPublicBaseUrl(),
		InternalBaseURL:    resp.GetInternalBaseUrl(),
		PluginProxyBaseURL: resp.GetPluginProxyBaseUrl(),
	}, nil
}
