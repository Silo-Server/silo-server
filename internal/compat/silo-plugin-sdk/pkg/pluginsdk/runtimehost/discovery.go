package runtimehost

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func (c *Client) ListInstalledPluginsByCapability(ctx context.Context, capabilityType string) ([]*pluginv1.InstalledPlugin, error) {
	plugins, err := c.ListInstalledPlugins(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*pluginv1.InstalledPlugin, 0, len(plugins))
	for _, plugin := range plugins {
		if HasCapability(plugin, capabilityType) {
			out = append(out, plugin)
		}
	}
	return out, nil
}

func HasCapability(plugin *pluginv1.InstalledPlugin, capabilityType string) bool {
	return Capability(plugin, capabilityType) != nil
}

func Capability(plugin *pluginv1.InstalledPlugin, capabilityType string) *pluginv1.CapabilityDescriptor {
	if plugin == nil || capabilityType == "" {
		return nil
	}
	for _, cap := range plugin.GetCapabilities() {
		if cap.GetType() == capabilityType {
			return cap
		}
	}
	return nil
}

func CapabilityMetadata(cap *pluginv1.CapabilityDescriptor) map[string]any {
	if cap == nil || cap.GetMetadata() == nil {
		return map[string]any{}
	}
	return cap.GetMetadata().AsMap()
}

func CapabilityMetadataString(cap *pluginv1.CapabilityDescriptor, key string) string {
	value, ok := CapabilityMetadata(cap)[key].(string)
	if !ok {
		return ""
	}
	return value
}

func CapabilityMetadataStrings(cap *pluginv1.CapabilityDescriptor, key string) []string {
	value, ok := CapabilityMetadata(cap)[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(value))
	for _, item := range value {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
