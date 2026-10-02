package nodepool

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// An NVIDIA uuid identifies a card wherever it is plugged in; a PCI address
// only identifies a slot, and only within one boot of one kernel. Deriving the
// key this way is what lets the planner — and an admin — see that two nodes are
// sharing one GPU.
func TestPhysicalGPUKeys(t *testing.T) {
	tests := []struct {
		name         string
		capabilities string
		want         []string
	}{
		{
			name: "prefers gpu uuid over slot identity",
			capabilities: `{"boot_id":"boot-1","render_device_details":[
				{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-aaa"}]}`,
			want: []string{"GPU-aaa"},
		},
		{
			name: "falls back to boot-scoped pci address",
			capabilities: `{"boot_id":"boot-1","render_device_details":[
				{"path":"/dev/dri/renderD129","pci_address":"0000:04:00.0"}]}`,
			want: []string{"boot-1|0000:04:00.0"},
		},
		{
			name: "mixed devices are deduped and sorted",
			capabilities: `{"boot_id":"boot-1","render_device_details":[
				{"path":"/dev/dri/renderD130","pci_address":"0000:05:00.0"},
				{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-bbb"},
				{"path":"/dev/dri/renderD129","pci_address":"0000:03:00.0","gpu_uuid":"GPU-bbb"}]}`,
			want: []string{"GPU-bbb", "boot-1|0000:05:00.0"},
		},
		{
			name: "device with no identity contributes no key",
			capabilities: `{"boot_id":"boot-1","render_device_details":[
				{"path":"/dev/dri/renderD128"},{"path":"/dev/dri/renderD129","gpu_uuid":"GPU-ccc"}]}`,
			want: []string{"GPU-ccc"},
		},
		{
			// Boot id detection is best-effort. Without one the slot is scoped
			// to nothing, and "|0000:00:02.0" — where every Intel iGPU lives —
			// would merge unrelated hosts into one GPU group.
			name:         "a slot with no boot id contributes no key",
			capabilities: `{"render_device_details":[{"pci_address":"0000:00:02.0"}]}`,
			want:         nil,
		},
		{
			// A uuid is host-independent by construction, so it survives the
			// missing boot id that disqualifies its slot.
			name:         "a uuid still identifies a device on a host with no boot id",
			capabilities: `{"render_device_details":[{"pci_address":"0000:03:00.0","gpu_uuid":"GPU-ddd"}]}`,
			want:         []string{"GPU-ddd"},
		},
		{
			// The ordinary NVIDIA container: /dev/nvidia* and the toolkit, no
			// /dev/dri at all. NVENC works, render_device_details is empty, and
			// without the uuid list the whole host contributes no identity — so
			// two containers on one card read as two independent GPUs and the
			// shared-GPU tie-break keeps piling work onto the same hardware.
			name:         "cuda-only host keys by uuid with no render device",
			capabilities: `{"boot_id":"boot-1","render_device_details":[],"nvidia_gpu_uuids":["GPU-eee","GPU-fff"]}`,
			want:         []string{"GPU-eee", "GPU-fff"},
		},
		{
			// A uuid is host-independent, so the card a container reaches only
			// through nvidia-smi keys identically to the one its neighbor also
			// sees through a render node. That identity is the whole point.
			name: "a card reported both ways contributes one key",
			capabilities: `{"boot_id":"boot-1","render_device_details":[
				{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-ggg"}],
				"nvidia_gpu_uuids":["GPU-ggg"]}`,
			want: []string{"GPU-ggg"},
		},
		{
			name:         "unparseable uuid list yields no keys",
			capabilities: `{"nvidia_gpu_uuids":"nope"}`,
			want:         nil,
		},
		{name: "no capabilities stored", capabilities: "", want: nil},
		{name: "unparseable payload", capabilities: `not json`, want: nil},
		{name: "payload of the wrong shape", capabilities: `{"render_device_details":"nope"}`, want: nil},
		{name: "no render devices", capabilities: `{"boot_id":"boot-1","render_device_details":[]}`, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := physicalGPUKeys([]byte(tt.capabilities))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("physicalGPUKeys() = %v, want %v", got, tt.want)
			}
		})
	}
}

const gpuAAACapabilities = `{"boot_id":"boot-1","render_device_details":[` +
	`{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-aaa"}]}`

// Keys must exist from the moment a stored row reaches a pool, not only after
// the next capability refetch: after an API restart the planner routes on the
// inventory the database already holds.
func TestPoolsDeriveGPUKeysOnLoad(t *testing.T) {
	transcodes := NewTranscodePool()
	transcodes.SetNodes([]*Node{{
		ID: 1, URL: "http://tc-1/", Enabled: true, Healthy: true,
		Capabilities: json.RawMessage(gpuAAACapabilities),
	}})
	if got := transcodes.Nodes()[0].PhysicalGPUKeys; !slices.Equal(got, []string{"GPU-aaa"}) {
		t.Fatalf("transcode pool load derived %v, want [GPU-aaa]", got)
	}
	// The URL normalization the same loop performs must still happen.
	if got := transcodes.Nodes()[0].URL; got != "http://tc-1" {
		t.Fatalf("transcode pool load left URL %q unnormalized", got)
	}

	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{
		{ID: 2, URL: "http://proxy-1", Enabled: true, Healthy: true, Capabilities: json.RawMessage(gpuAAACapabilities)},
		{ID: 3, URL: "http://proxy-2", Enabled: true, Healthy: true},
	})
	if got := proxies.Nodes()[0].PhysicalGPUKeys; !slices.Equal(got, []string{"GPU-aaa"}) {
		t.Fatalf("proxy pool load derived %v, want [GPU-aaa]", got)
	}
	if got := proxies.Nodes()[1].PhysicalGPUKeys; got != nil {
		t.Fatalf("node without a stored report derived %v, want none", got)
	}
}

// A refetched report replaces the identities it describes; carrying the
// previous ones over would claim a GPU that the node no longer reports.
func TestApplyCapabilitiesDerivesGPUKeys(t *testing.T) {
	refreshedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	transcodes := NewTranscodePool()
	transcodes.SetNodes([]*Node{{ID: 1, URL: "http://tc-1", Enabled: true, Healthy: true}})
	transcodes.ApplyCapabilities(1, "http://tc-1", []byte(gpuAAACapabilities), "sha256:aaa", refreshedAt, nil, nil)
	if got := transcodes.Nodes()[0].PhysicalGPUKeys; !slices.Equal(got, []string{"GPU-aaa"}) {
		t.Fatalf("transcode ApplyCapabilities derived %v, want [GPU-aaa]", got)
	}

	// The card was passed through to another host: the node now reports a
	// device it cannot identify, and must stop claiming the old key.
	transcodes.ApplyCapabilities(1, "http://tc-1", []byte(`{"boot_id":"boot-2","render_device_details":[{"path":"/dev/dri/renderD128"}]}`),
		"sha256:bbb", refreshedAt, nil, nil)
	if got := transcodes.Nodes()[0].PhysicalGPUKeys; got != nil {
		t.Fatalf("stale identities survived a new report: %v", got)
	}

	proxies := NewProxyPool()
	proxies.SetNodes([]*Node{{ID: 2, URL: "http://proxy-1", Enabled: true, Healthy: true}})
	proxies.ApplyCapabilities(2, "http://proxy-1", []byte(gpuAAACapabilities), "sha256:aaa", refreshedAt, nil, nil)
	if got := proxies.Nodes()[0].PhysicalGPUKeys; !slices.Equal(got, []string{"GPU-aaa"}) {
		t.Fatalf("proxy ApplyCapabilities derived %v, want [GPU-aaa]", got)
	}
}
