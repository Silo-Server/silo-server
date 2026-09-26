package apiv2

import (
	"context"
	"net/http"
	"slices"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type AdminHardwareAccelerationService interface {
	ReadHardwareAcceleration(http.ResponseWriter, *http.Request) handlers.HWAccelInventory
}

type AdminHardwareToneMapCapability tonemap.Capability

type AdminHardwareAcceleration struct {
	Resolved                  string                           `json:"resolved"`
	RenderDevices             []string                         `json:"render_devices"`
	RenderDeviceDetails       []playback.RenderDeviceInfo      `json:"render_device_details"`
	IntelDetected             bool                             `json:"intel_detected"`
	DetectedBackends          []playback.DetectedBackend       `json:"detected_backends,omitempty"`
	Source                    string                           `json:"source"`
	NodeURL                   string                           `json:"node_url,omitempty"`
	Transformations           []playback.TransformationV3      `json:"transformations,omitempty"`
	TransportFeatures         []string                         `json:"transport_features,omitempty"`
	ToneMapCapabilities       []AdminHardwareToneMapCapability `json:"tone_map_capabilities,omitempty"`
	BootID                    string                           `json:"boot_id,omitempty"`
	NVIDIAGPUUUIDs            []string                         `json:"nvidia_gpu_uuids,omitempty"`
	CapabilityHash            string                           `json:"capability_hash,omitempty"`
	ProbeRequestTimeoutMillis int64                            `json:"probe_request_timeout_ms,omitempty"`
	Nodes                     []NodeHWAccel                    `json:"nodes,omitempty"`
}
type AdminHardwareAccelerationOutput struct{ Body AdminHardwareAcceleration }
type AdminHardwareAccelerationInput struct {
	requestCapture
}

func registerAdminHardwareAcceleration(reg *Registry) {
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/system/hw-accel", "getAdminHardwareAcceleration", "admin-settings", "Read synchronous hardware inventory with existing probe budgets and local fallback; a configured backend does not imply verified support."), Class: ClassActingAdmin, ServiceBacked: true}, func(_ context.Context, in *AdminHardwareAccelerationInput) (*AdminHardwareAccelerationOutput, error) {
		if reg.deps.AdminHardwareAcceleration == nil {
			return nil, unavailable("hardware acceleration inventory")
		}
		inventory := reg.deps.AdminHardwareAcceleration.ReadHardwareAcceleration(in.writer, in.request)
		inventory.Nodes = slices.Clone(inventory.Nodes)
		if inventory.NodeURL != "" {
			inventory.NodeURL = logredact.SanitizeURL(inventory.NodeURL)
		}
		for i := range inventory.Nodes {
			inventory.Nodes[i].NodeURL = logredact.SanitizeURL(inventory.Nodes[i].NodeURL)
			if inventory.Nodes[i].Error != "" {
				inventory.Nodes[i].Error = "Node capability probe failed."
			}
		}
		nodes := make([]NodeHWAccel, len(inventory.Nodes))
		for i, node := range inventory.Nodes {
			nodes[i] = NodeHWAccel(node)
		}
		v := inventory.HWAccelInfo
		body := AdminHardwareAcceleration{Resolved: v.Resolved, RenderDevices: append([]string{}, v.RenderDevices...), RenderDeviceDetails: append([]playback.RenderDeviceInfo{}, v.RenderDeviceDetails...), IntelDetected: v.IntelDetected, DetectedBackends: v.DetectedBackends, Source: v.Source, NodeURL: v.NodeURL, Transformations: v.Transformations, TransportFeatures: v.TransportFeatures, BootID: v.BootID, NVIDIAGPUUUIDs: v.NVIDIAGPUUUIDs, CapabilityHash: v.CapabilityHash, ProbeRequestTimeoutMillis: v.ProbeRequestTimeoutMillis, Nodes: nodes}
		for _, capability := range v.ToneMapCapabilities {
			body.ToneMapCapabilities = append(body.ToneMapCapabilities, AdminHardwareToneMapCapability(capability))
		}
		return &AdminHardwareAccelerationOutput{Body: body}, nil
	})
}

// NodeHWAccel is the native transport projection, independent of handler views.
type NodeHWAccel struct {
	NodeURL             string                      `json:"node_url"`
	NodeName            string                      `json:"node_name,omitempty"`
	Resolved            string                      `json:"resolved,omitempty"`
	RenderDevices       []string                    `json:"render_devices,omitempty"`
	RenderDeviceDetails []playback.RenderDeviceInfo `json:"render_device_details,omitempty"`
	Error               string                      `json:"error,omitempty"`
}
