package nodepool

import (
	"encoding/json"
	"slices"
)

// gpuIdentityView is the minimal projection this package parses out of an
// otherwise opaque capability payload, in the same spirit as
// capabilityDriftView: nodepool must not depend on playback, and identifying a
// GPU only needs the host's boot id and each render device's own identity.
type gpuIdentityView struct {
	BootID              string `json:"boot_id"`
	RenderDeviceDetails []struct {
		PCIAddress string `json:"pci_address"`
		GPUUUID    string `json:"gpu_uuid"`
	} `json:"render_device_details"`
	// NVIDIAGPUUUIDs covers cards with no readable DRM node, which is the
	// ordinary shape of an NVIDIA container: /dev/nvidia* and the toolkit, no
	// /dev/dri. NVENC works there and render_device_details is empty, so
	// without this the host contributes no identity at all and two containers
	// on one card read as two GPUs.
	NVIDIAGPUUUIDs []string `json:"nvidia_gpu_uuids"`
}

// physicalGPUKeys derives one stable key per GPU a node can see, deduplicated
// and sorted so two nodes' key sets can be compared directly.
//
// An NVIDIA uuid is preferred because it follows the card between slots and
// hosts; the PCI address falls back to it, scoped by boot id because a device
// path and a slot only mean the same hardware within one boot of one kernel. A
// device with neither is unidentifiable and contributes no key rather than a
// fake one — a synthetic key would claim two nodes share hardware (or do not)
// on no evidence, and both directions of that claim change how work is placed.
//
// The fallback needs a boot id as much as it needs a slot. Boot id detection is
// best-effort (it reads /proc, which a hardened or sandboxed host may hide even
// while sysfs stays readable), and an empty one scopes the key to nothing: two
// unrelated hosts whose iGPU sits at the near-universal 0000:00:02.0 would both
// derive "|0000:00:02.0" and be routed as one card. So an unscoped slot is
// treated like no identity at all — nodes on such a host are accounted
// independently, which is only what they got before GPU grouping existed.
//
// A payload that cannot be parsed yields no keys: an unreadable report is not
// evidence about hardware.
func physicalGPUKeys(capabilities []byte) []string {
	if len(capabilities) == 0 {
		return nil
	}
	var identity gpuIdentityView
	if err := json.Unmarshal(capabilities, &identity); err != nil {
		return nil
	}
	total := len(identity.RenderDeviceDetails) + len(identity.NVIDIAGPUUUIDs)
	seen := make(map[string]struct{}, total)
	keys := make([]string, 0, total)
	add := func(key string) {
		if key == "" {
			return
		}
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for _, device := range identity.RenderDeviceDetails {
		key := device.GPUUUID
		if key == "" {
			if device.PCIAddress == "" || identity.BootID == "" {
				continue
			}
			key = identity.BootID + "|" + device.PCIAddress
		}
		add(key)
	}
	// A uuid is host-independent, so a card reported only through nvidia-smi
	// keys the same way whether or not it also has a render node — which is
	// what lets a container with /dev/dri and one without recognize the same
	// physical GPU.
	for _, uuid := range identity.NVIDIAGPUUUIDs {
		add(uuid)
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)
	return keys
}

// applyPhysicalGPUKeys refreshes a node's derived GPU identities from the
// capability payload it currently carries. Every place a Node is built from
// stored bytes calls it, so the field is never a stale answer to an older
// payload: the row scanner, the pools' load path, and the capability writer.
func applyPhysicalGPUKeys(n *Node) {
	if n == nil {
		return
	}
	n.PhysicalGPUKeys = physicalGPUKeys(n.Capabilities)
}
