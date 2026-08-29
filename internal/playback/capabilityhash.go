package playback

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// ComputeCapabilityHash summarizes what a host has and can do into one
// comparable token, so a reader (the node health sweep, an operator) detects
// change without diffing a whole report or re-probing.
//
// Only hardware identity and capability are hashed. Fields that vary with who
// asked rather than with the host — Source, NodeURL, and the hash itself — are
// excluded, because a report of unchanged hardware must keep its hash no matter
// who asked for it. IntelDetected is excluded as well: it is derived from the
// render devices already covered.
//
// ProbeRequestTimeoutMillis is included, though it describes the report rather
// than the hardware. It is the node's own statement of how long its answer may
// take, and the control plane sizes real deadlines from the stored copy. A node
// upgraded to a build that needs longer changes nothing else about itself, so
// leaving it out of the change signal meant the sweep never refetched and the
// API kept canceling that node's re-probes against a budget it had outgrown.
// It is stable for a given build and configuration, so including it costs one
// refetch at upgrade and nothing after.
//
// Every slice is ordered here rather than trusted from the input: probe and
// filesystem enumeration order is incidental, so two reports of the same host
// hash identically regardless of it.
func ComputeCapabilityHash(info HWAccelInfo) string {
	payload, err := json.Marshal(canonicalCapabilities(info))
	if err != nil {
		// Unreachable: every field below is a string, bool, or slice of them.
		return ""
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// canonicalCapability is the hashed projection of an HWAccelInfo. It is a
// distinct type rather than a reordered copy of the input so that adding a
// field to HWAccelInfo never silently changes every node's hash.
type canonicalCapability struct {
	Resolved            string                     `json:"resolved"`
	BootID              string                     `json:"boot_id"`
	RenderDevices       []string                   `json:"render_devices"`
	RenderDeviceDetails []canonicalRenderDevice    `json:"render_device_details"`
	NVIDIAGPUUUIDs      []string                   `json:"nvidia_gpu_uuids"`
	ProbeRequestTimeout int64                      `json:"probe_request_timeout_ms"`
	DetectedBackends    []canonicalDetectedBackend `json:"detected_backends"`
	Transformations     []canonicalTransformation  `json:"transformations"`
	ToneMapCapabilities []canonicalToneMap         `json:"tone_map_capabilities"`
}

// canonicalRenderDevice serializes a device strongest identity first: the GPU
// uuid identifies the card anywhere, the PCI address identifies a slot, and the
// path only identifies an enumeration position.
type canonicalRenderDevice struct {
	GPUUUID     string `json:"gpu_uuid"`
	PCIAddress  string `json:"pci_address"`
	Path        string `json:"path"`
	Description string `json:"description"`
}

type canonicalDetectedBackend struct {
	Backend  string   `json:"backend"`
	Verified bool     `json:"verified"`
	Devices  []string `json:"devices"`
	Device   string   `json:"device"`
	Reason   string   `json:"reason"`
	Skipped  bool     `json:"skipped"`
}

type canonicalTransformation struct {
	Name            string   `json:"name"`
	Executor        string   `json:"executor"`
	RecipeVersion   string   `json:"recipe_version"`
	ValidatedClaims []string `json:"validated_claims"`
}

type canonicalToneMap struct {
	Mode        string   `json:"mode"`
	Backend     string   `json:"backend"`
	Filter      string   `json:"filter"`
	SourceKinds []string `json:"source_kinds"`
}

func canonicalCapabilities(info HWAccelInfo) canonicalCapability {
	return canonicalCapability{
		Resolved:            info.Resolved,
		BootID:              info.BootID,
		RenderDevices:       sortedStrings(info.RenderDevices),
		RenderDeviceDetails: canonicalRenderDevices(info.RenderDeviceDetails),
		NVIDIAGPUUUIDs:      sortedStrings(info.NVIDIAGPUUUIDs),
		ProbeRequestTimeout: info.ProbeRequestTimeoutMillis,
		DetectedBackends:    canonicalDetectedBackends(info.DetectedBackends),
		Transformations:     canonicalTransformations(info.Transformations),
		ToneMapCapabilities: canonicalToneMaps(info.ToneMapCapabilities),
	}
}

func canonicalRenderDevices(details []RenderDeviceInfo) []canonicalRenderDevice {
	out := make([]canonicalRenderDevice, 0, len(details))
	for _, detail := range details {
		out = append(out, canonicalRenderDevice{
			GPUUUID:     detail.GPUUUID,
			PCIAddress:  detail.PCIAddress,
			Path:        detail.Path,
			Description: detail.Description,
		})
	}
	slices.SortFunc(out, func(a, b canonicalRenderDevice) int {
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

func canonicalDetectedBackends(backends []DetectedBackend) []canonicalDetectedBackend {
	out := make([]canonicalDetectedBackend, 0, len(backends))
	for _, backend := range backends {
		out = append(out, canonicalDetectedBackend{
			Backend:  backend.Backend,
			Verified: backend.Verified,
			Devices:  sortedStrings(backend.Devices),
			Device:   backend.Device,
			Reason:   backend.Reason,
			Skipped:  backend.Skipped,
		})
	}
	slices.SortFunc(out, func(a, b canonicalDetectedBackend) int {
		return strings.Compare(a.Backend, b.Backend)
	})
	return out
}

func canonicalTransformations(transformations []TransformationV3) []canonicalTransformation {
	out := make([]canonicalTransformation, 0, len(transformations))
	for _, transformation := range transformations {
		out = append(out, canonicalTransformation{
			Name:            transformation.Name,
			Executor:        transformation.Executor,
			RecipeVersion:   transformation.RecipeVersion,
			ValidatedClaims: sortedStrings(transformation.ValidatedClaims),
		})
	}
	slices.SortFunc(out, func(a, b canonicalTransformation) int {
		if byName := strings.Compare(a.Name, b.Name); byName != 0 {
			return byName
		}
		return strings.Compare(a.RecipeVersion, b.RecipeVersion)
	})
	return out
}

func canonicalToneMaps(capabilities tonemap.Capabilities) []canonicalToneMap {
	out := make([]canonicalToneMap, 0, len(capabilities))
	for _, capability := range capabilities {
		kinds := make([]string, 0, len(capability.SourceKinds))
		for _, kind := range capability.SourceKinds {
			kinds = append(kinds, string(kind))
		}
		out = append(out, canonicalToneMap{
			Mode:        string(capability.Mode),
			Backend:     capability.Backend,
			Filter:      capability.Filter,
			SourceKinds: sortedStrings(kinds),
		})
	}
	slices.SortFunc(out, func(a, b canonicalToneMap) int {
		if byMode := strings.Compare(a.Mode, b.Mode); byMode != 0 {
			return byMode
		}
		if byBackend := strings.Compare(a.Backend, b.Backend); byBackend != 0 {
			return byBackend
		}
		return strings.Compare(a.Filter, b.Filter)
	})
	return out
}

// sortedStrings returns an ordered copy, leaving the caller's slice alone: the
// input is a live report another goroutine may still be serving.
func sortedStrings(values []string) []string {
	out := make([]string, 0, len(values))
	out = append(out, values...)
	slices.Sort(out)
	return out
}
