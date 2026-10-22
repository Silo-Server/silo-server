package nodepool

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const degradedCapabilityPayload = `{"resolved":"none","render_devices":[],` +
	`"detected_backends":[{"backend":"nvenc","verified":false}],"capability_hash":"sha256:degraded"}`

// The drift log line only reaches an operator who is reading logs. Persisting
// the same finding is what puts it on the node list, so the pool copy has to
// carry it the moment the report is applied.
func TestHealthCheckerCarriesCapabilityDriftIntoThePool(t *testing.T) {
	url := newHealthNode(t, "sha256:degraded")
	fetcher := &fakeCapabilityFetcher{payload: []byte(degradedCapabilityPayload), hash: "sha256:degraded"}
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", Type: NodeTypeTranscode, URL: url, Enabled: true,
		Capabilities:     json.RawMessage(testCapabilityPayload),
		CapabilitiesHash: strPtr("sha256:old"),
	}, fetcher)

	fixture.sweep()

	stored := fixture.storedNode(t)
	if stored.CapabilityDrift == nil {
		t.Fatal("pool copy carries no capability_drift after a regression")
	}
	note := *stored.CapabilityDrift
	for _, want := range []string{"nvenc", "/dev/dri/renderD128", "none"} {
		if !strings.Contains(note, want) {
			t.Fatalf("drift note %q does not name %q", note, want)
		}
	}
}

// A repaired node must stop being flagged. The note describes the last
// comparison, not a latched incident, so a clean report clears it — otherwise a
// one-off driver hiccup would mark a node broken forever.
func TestHealthCheckerClearsCapabilityDriftOnRecovery(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	previousNote := "verified hardware backends lost: nvenc"
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", Type: NodeTypeTranscode, URL: url, Enabled: true,
		Capabilities:     json.RawMessage(degradedCapabilityPayload),
		CapabilitiesHash: strPtr("sha256:degraded"),
		CapabilityDrift:  &previousNote,
	}, fetcher)

	fixture.sweep()

	if stored := fixture.storedNode(t); stored.CapabilityDrift != nil {
		t.Fatalf("capability_drift = %q after recovery, want it cleared", *stored.CapabilityDrift)
	}
}

// A still-degraded report is not a recovery. Drift is a delta, so the refetch
// after a regression finds nothing *newly* lost — and clearing the note there
// would tell an operator the node is fine while its backend is still failing its
// probe. Anything that moves the hash reaches this path: a reboot moves boot_id,
// a reworded FFmpeg failure moves the probe reason.
func TestHealthCheckerKeepsCapabilityDriftWhileTheReportIsStillDegraded(t *testing.T) {
	const rebootedPayload = `{"resolved":"none","render_devices":[],` +
		`"detected_backends":[{"backend":"nvenc","verified":false,"reason":"no such device"}],` +
		`"boot_id":"after-reboot","capability_hash":"sha256:rebooted"}`
	url := newHealthNode(t, "sha256:rebooted")
	fetcher := &fakeCapabilityFetcher{payload: []byte(rebootedPayload), hash: "sha256:rebooted"}
	previousNote := "verified hardware backends lost: nvenc"
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", Type: NodeTypeTranscode, URL: url, Enabled: true,
		Capabilities:     json.RawMessage(degradedCapabilityPayload),
		CapabilitiesHash: strPtr("sha256:degraded"),
		CapabilityDrift:  &previousNote,
	}, fetcher)

	fixture.sweep()

	stored := fixture.storedNode(t)
	if stored.CapabilityDrift == nil {
		t.Fatal("capability_drift cleared by a refetch that found the node still degraded")
	}
	if *stored.CapabilityDrift != previousNote {
		t.Fatalf("capability_drift = %q, want the standing note %q", *stored.CapabilityDrift, previousNote)
	}
}

// The operator-triggered re-probe refetches unconditionally, with no hash gate.
// It is the action the docs and the UI tooltip prescribe for checking whether a
// drift note is still true, so it must be able to answer "yes" — clearing the
// badge on the first click regardless of what the probe found would make the
// only tool for confirming the note the tool that destroys it.
func TestRefreshNodeCapabilitiesKeepsDriftWhenNothingRecovered(t *testing.T) {
	url := newHealthNode(t, "sha256:degraded")
	fetcher := &fakeCapabilityFetcher{payload: []byte(degradedCapabilityPayload), hash: "sha256:degraded"}
	previousNote := "verified hardware backends lost: nvenc"
	node := &Node{
		ID: 1, Name: "gpu-1", Type: NodeTypeTranscode, URL: url, Enabled: true,
		Capabilities:     json.RawMessage(degradedCapabilityPayload),
		CapabilitiesHash: strPtr("sha256:degraded"),
		CapabilityDrift:  &previousNote,
	}
	fixture := newCapabilityCheckerFixture(t, node, fetcher)

	if err := fixture.checker.RefreshNodeCapabilities(context.Background(), node); err != nil {
		t.Fatalf("RefreshNodeCapabilities: %v", err)
	}

	if stored := fixture.storedNode(t); stored.CapabilityDrift == nil {
		t.Fatal("a re-probe that found the same failing probe cleared the drift note")
	}
}

const withUUID = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
	`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-abc"}],` +
	`"detected_backends":[{"backend":"nvenc","verified":true}]}`
const withoutUUID = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
	`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
	`"detected_backends":[{"backend":"nvenc","verified":true}]}`

var computeCapabilityDriftCases = []struct {
	name          string
	before, after string
	lostDevices   []string
	lostBackends  []string
}{
	{
		// DRM is free to hand the same card a different renderD number
		// across a reboot. Comparing enumeration paths alone reports a GPU
		// as gone for a card that never moved.
		name: "a renumbered path at the same PCI slot is no regression",
		before: `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"qsv","verified":true}]}`,
		after: `{"resolved":"qsv","render_devices":["/dev/dri/renderD129"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD129","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"qsv","verified":true}]}`,
	},
	{
		// A card that genuinely goes away has neither its path nor its slot
		// in the new report.
		name: "a removed device is still caught",
		before: `{"resolved":"qsv","render_devices":["/dev/dri/renderD128","/dev/dri/renderD129"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"},` +
			`{"path":"/dev/dri/renderD129","pci_address":"0000:04:00.0"}],` +
			`"detected_backends":[{"backend":"qsv","verified":true}]}`,
		after: `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"qsv","verified":true}]}`,
		lostDevices: []string{"/dev/dri/renderD129"},
	},
	{
		// An NVIDIA uuid outranks the slot.
		name: "a card moved between slots is matched by uuid",
		before: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-abc"}],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		after: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD130"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD130","pci_address":"0000:07:00.0","gpu_uuid":"GPU-abc"}],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
	},
	// nvidia-smi is queried behind a circuit breaker, so the same card
	// publishes a uuid on one pass and only its PCI address on another; a
	// shared alias identifies it.
	{name: "uuid disappears", before: withUUID, after: withoutUUID},
	{name: "uuid appears", before: withoutUUID, after: withUUID},
	{
		// A replacement card in the same slot keeps the slot's PCI address
		// and render path; two permanent uuids that disagree settle it.
		name: "a replaced card in the same slot is reported gone",
		before: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-old"}],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		after: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-new"}],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		lostDevices: []string{"/dev/dri/renderD128"},
	},
	{
		// "Could not try" (skipped) is not a loss; "tried and the driver
		// said no" is.
		name: "a backend failing its probe is still a loss",
		before: `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
			`"detected_backends":[{"backend":"qsv","verified":true}]}`,
		after: `{"resolved":"none","render_devices":["/dev/dri/renderD128"],` +
			`"detected_backends":[{"backend":"qsv","verified":false,"reason":"h264_qsv smoke encode failed"}]}`,
		lostBackends: []string{"qsv"},
	},
	{
		// The device comparison reads the host's own inventory; the backend
		// going unreported alongside it is a consequence, not evidence.
		name: "a vanished GPU is a device loss",
		before: `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
			`"detected_backends":[{"backend":"qsv","verified":true}]}`,
		after:       `{"resolved":"none","render_devices":[],"detected_backends":[]}`,
		lostDevices: []string{"/dev/dri/renderD128"},
	},
	{
		// Detection only probes backends the configured hw_device gives it
		// candidates for. Repointing a node from QSV to NVENC must not latch
		// a warning demanding QSV verify again, which nothing could satisfy.
		name: "a backend the policy stopped probing is no regression",
		before: `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"qsv","verified":true}]}`,
		after: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
	},
	{
		// The ordinary NVENC container has no /dev/dri, so its cards exist
		// only in nvidia_gpu_uuids, and NVENC still verifies on the one left.
		name: "an NVIDIA-only card going away is noted",
		before: `{"resolved":"nvenc","nvidia_gpu_uuids":["GPU-aaa","GPU-bbb"],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		after: `{"resolved":"nvenc","nvidia_gpu_uuids":["GPU-bbb"],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		lostDevices:  []string{"GPU-aaa"},
		lostBackends: []string{},
	},
	{
		// nvidia-smi can be missing from an image outright; NVENC is only
		// probed where /dev/nvidia* opens, so the cards are still present.
		name: "NVIDIA uuids lost with the query tool are no regression",
		before: `{"resolved":"nvenc","nvidia_gpu_uuids":["GPU-aaa"],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		after: `{"resolved":"nvenc","detected_backends":[{"backend":"nvenc","verified":true}]}`,
	},
	{
		// A card with both a render node and an nvidia-smi entry is one card.
		name: "one card counts once across both identity sources",
		before: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","gpu_uuid":"GPU-aaa"}],` +
			`"nvidia_gpu_uuids":["GPU-aaa"],"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		after: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128"}],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
	},
}

// computeCapabilityDrift compares the stored report with a new one. A row
// with neither lostDevices nor lostBackends wants no regression; otherwise
// each non-nil list must match exactly.
func TestComputeCapabilityDrift(t *testing.T) {
	for _, test := range computeCapabilityDriftCases {
		t.Run(test.name, func(t *testing.T) {
			drift, parsed := computeCapabilityDrift([]byte(test.before), []byte(test.after))
			if !parsed {
				t.Fatal("both reports should parse")
			}
			if test.lostDevices == nil && test.lostBackends == nil {
				if drift.regressed() {
					t.Fatalf("drift = %+v, want no regression", drift)
				}
				return
			}
			if test.lostDevices != nil && !slices.Equal(drift.lostDevices, test.lostDevices) {
				t.Fatalf("lostDevices = %v, want %v", drift.lostDevices, test.lostDevices)
			}
			if test.lostBackends != nil && !slices.Equal(drift.lostBackends, test.lostBackends) {
				t.Fatalf("lostBackends = %v, want %v", drift.lostBackends, test.lostBackends)
			}
		})
	}
}

const (
	lostVAAPI   = "verified hardware backends lost: vaapi"
	lostD128    = "render devices gone: /dev/dri/renderD128"
	card0300    = `{"devices":[{"aliases":["0000:03:00.0","/dev/dri/renderD128"]}]}`
	oldCardSlot = `{"devices":[{"uuid":"GPU-old","aliases":["GPU-old","0000:03:00.0","/dev/dri/renderD128"]}]}`
)

var resolveDriftNoteOnARepeatedReportCases = []struct {
	name          string
	standing      string
	baseline      string
	payload       string
	clears        bool
	exact         bool
	keepsBaseline bool
	probesClean   bool
}{
	// A device the node can no longer open reports the backend skipped, and
	// a card that is gone leaves no candidate backend at all. Neither is a
	// passing probe, so neither is evidence of recovery.
	{
		name:     "every candidate device is inaccessible",
		standing: lostVAAPI,
		payload: `{"resolved":"none","render_devices":[],` +
			`"detected_backends":[{"backend":"vaapi","verified":false,"skipped":true}]}`,
		exact: true,
	},
	{
		name:     "the gpu is gone, so nothing was a candidate",
		standing: lostVAAPI,
		payload:  `{"resolved":"none","render_devices":[],"detected_backends":[]}`,
		exact:    true,
	},
	{
		// Growth is not repair: the note keeps what it is waiting for.
		name:     "an unrelated GPU is added",
		standing: lostD128,
		baseline: card0300,
		payload: `{"resolved":"vaapi","render_devices":["/dev/dri/renderD130"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD130","pci_address":"0000:09:00.0"}],` +
			`"detected_backends":[{"backend":"vaapi","verified":true}]}`,
		exact: true, keepsBaseline: true,
	},
	{
		// The baseline keeps every identity the lost card answered to.
		name:     "the lost card returns renumbered",
		standing: lostD128,
		baseline: card0300,
		payload: `{"resolved":"vaapi","render_devices":["/dev/dri/renderD129"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD129","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"vaapi","verified":true}]}`,
		clears: true,
	},
	{
		// A multi-GPU node that lost one card probes the survivor cleanly.
		name:     "a sibling GPU is still missing",
		standing: "render devices gone: /dev/dri/renderD129",
		baseline: `{"devices":[{"aliases":["0000:04:00.0","/dev/dri/renderD129"]}]}`,
		payload: `{"resolved":"vaapi","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"vaapi","verified":true}]}`,
		exact: true, probesClean: true,
	},
	{
		// A replacement card inherits the PCI address and render path.
		name:     "a replacement card takes the slot",
		standing: lostD128,
		baseline: oldCardSlot,
		payload: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-new"}],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		exact: true,
	},
	{
		name:     "the same card returns by uuid in a new slot",
		standing: lostD128,
		baseline: oldCardSlot,
		payload: `{"resolved":"nvenc","render_devices":["/dev/dri/renderD131"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD131","pci_address":"0000:09:00.0","gpu_uuid":"GPU-old"}],` +
			`"detected_backends":[{"backend":"nvenc","verified":true}]}`,
		clears: true,
	},
	{
		// VAAPI failing beside a working QSV is ordinary on a mixed host.
		name:     "an unrelated backend is failing",
		standing: lostD128,
		baseline: `{"backends":["qsv"],"devices":[{"aliases":["0000:03:00.0","/dev/dri/renderD128"]}]}`,
		payload: `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"qsv","verified":true},{"backend":"vaapi","verified":false,"reason":"no driver"}]}`,
		clears: true,
	},
	{
		name:     "the baseline backend is still failing",
		standing: "backends no longer verifying: qsv",
		baseline: `{"backends":["qsv"]}`,
		payload: `{"resolved":"vaapi","render_devices":["/dev/dri/renderD128"],` +
			`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
			`"detected_backends":[{"backend":"qsv","verified":false,"reason":"no driver"},{"backend":"vaapi","verified":true}]}`,
	},
	{
		name:     "every backend was skipped",
		standing: lostD128,
		baseline: `{"devices":[{"aliases":["/dev/dri/renderD128"]}]}`,
		payload: `{"resolved":"none","render_devices":[],` +
			`"detected_backends":[{"backend":"qsv","skipped":true},{"backend":"vaapi","skipped":true}]}`,
	},
}

// A refetch that finds nothing newly lost (the report compared with itself)
// may clear a standing note only on evidence that what the baseline waits for
// came back. exact also requires the note unchanged; keepsBaseline requires the
// baseline kept; probesClean pins the fixture's premise.
func TestResolveDriftNoteOnARepeatedReport(t *testing.T) {
	for _, test := range resolveDriftNoteOnARepeatedReportCases {
		t.Run(test.name, func(t *testing.T) {
			standing := test.standing
			var baseline []byte
			if test.baseline != "" {
				baseline = []byte(test.baseline)
			}
			payload := []byte(test.payload)
			drift, parsed := computeCapabilityDrift(payload, payload)
			if test.probesClean && !hardwareProbesClean(payload) {
				t.Fatal("the surviving card should probe cleanly; that is the point")
			}
			got, gotBaseline := resolveDriftNote(&standing, baseline, drift, parsed, payload)
			if test.clears {
				if got != nil {
					t.Fatalf("capability_drift = %q, want it cleared", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("capability_drift was cleared")
			}
			if test.exact && *got != standing {
				t.Fatalf("capability_drift = %q, want the standing note %q", *got, standing)
			}
			if test.keepsBaseline && len(gotBaseline) == 0 {
				t.Fatal("the baseline was dropped while the note still stands")
			}
		})
	}
}

// The complement: a report that gains back what the stored one lacked is the
// evidence recovery needs, and clears the note.
func TestResolveDriftNoteClearsWhenHardwareComesBack(t *testing.T) {
	const degraded = `{"resolved":"none","render_devices":[],` +
		`"detected_backends":[{"backend":"vaapi","verified":false,"reason":"no such device"}]}`
	const recovered = `{"resolved":"vaapi","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"vaapi","verified":true},` +
		`{"backend":"qsv","verified":false,"skipped":true}]}`

	standing := "verified hardware backends lost: vaapi"
	// The baseline the loss recorded: vaapi has to verify again.
	baseline := []byte(`{"backends":["vaapi"]}`)
	drift, parsed := computeCapabilityDrift([]byte(degraded), []byte(recovered))
	got, gotBaseline := resolveDriftNote(&standing, baseline, drift, parsed, []byte(recovered))
	if got != nil {
		t.Fatalf("capability_drift = %q, want the recovered backend to clear it", *got)
	}
	if gotBaseline != nil {
		t.Fatalf("baseline = %s, want it cleared with the note", gotBaseline)
	}
}

// Two cards going one at a time must both have to return: the second loss
// extends the baseline rather than replacing it.
func TestResolveDriftNoteAccumulatesSuccessiveLosses(t *testing.T) {
	const twoCards = `{"resolved":"vaapi","render_devices":["/dev/dri/renderD128","/dev/dri/renderD129"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"},` +
		`{"path":"/dev/dri/renderD129","pci_address":"0000:04:00.0"}],` +
		`"detected_backends":[{"backend":"vaapi","verified":true}]}`
	const oneCard = `{"resolved":"vaapi","render_devices":["/dev/dri/renderD129"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD129","pci_address":"0000:04:00.0"}],` +
		`"detected_backends":[{"backend":"vaapi","verified":true}]}`
	const noCards = `{"resolved":"none","render_devices":[],"detected_backends":[]}`

	firstLoss, parsed := computeCapabilityDrift([]byte(twoCards), []byte(oneCard))
	note, baseline := resolveDriftNote(nil, nil, firstLoss, parsed, []byte(oneCard))
	if note == nil || len(baseline) == 0 {
		t.Fatalf("first loss produced note=%v baseline=%s", note, baseline)
	}

	secondLoss, parsed := computeCapabilityDrift([]byte(oneCard), []byte(noCards))
	note, baseline = resolveDriftNote(note, baseline, secondLoss, parsed, []byte(noCards))
	if note == nil || len(baseline) == 0 {
		t.Fatal("second loss dropped the standing note or its baseline")
	}

	// Only the first card returns; the note must stand for the second.
	if got, _ := resolveDriftNote(note, baseline, capabilityDrift{}, true, []byte(oneCard)); got == nil {
		t.Fatal("capability_drift cleared with one of two lost cards still missing")
	}
	// Both back clears it.
	if got, _ := resolveDriftNote(note, baseline, capabilityDrift{}, true, []byte(twoCards)); got != nil {
		t.Fatalf("capability_drift = %q, want both cards returning to clear it", *got)
	}
}

// An unreadable payload means the drift is unknown, not that the node recovered.
func TestComputeCapabilityDriftReportsUnparseablePayloads(t *testing.T) {
	if _, parsed := computeCapabilityDrift([]byte(testCapabilityPayload), []byte(`not json`)); parsed {
		t.Fatal("an unreadable new report parsed")
	}
	if _, parsed := computeCapabilityDrift([]byte(`not json`), []byte(testCapabilityPayload)); parsed {
		t.Fatal("an unreadable stored report parsed")
	}
	drift, parsed := computeCapabilityDrift(nil, []byte(testCapabilityPayload))
	if !parsed || !drift.first || drift.regressed() {
		t.Fatalf("first report drift = %+v, parsed = %v", drift, parsed)
	}
}

// The note is echoed to every admin listing nodes, and its inputs come from a
// worker that may run on remote hardware.
func TestCapabilityDriftNoteIsBounded(t *testing.T) {
	baseline := driftBaseline{}
	for range 400 {
		baseline.Devices = append(baseline.Devices, driftBaselineDevice{Aliases: []string{"/dev/dri/renderD128"}})
	}
	note := baseline.note(capabilityDrift{})
	if note == "" {
		t.Fatal("a regression produced no note")
	}
	if len(note) > maxCapabilityDriftNoteBytes+3 {
		t.Fatalf("note is %d bytes, want it bounded at %d", len(note), maxCapabilityDriftNoteBytes)
	}
}

// capability_drift is a Postgres text column, which rejects invalid UTF-8
// outright — and the rejected UPDATE takes capabilities and capabilities_hash
// with it, so the stored hash never advances and every later sweep refetches and
// fails again. Device names come from a worker that may run elsewhere, so the
// bound has to cut on a rune boundary at every alignment, not just the lucky
// ones.
func TestCapabilityDriftNoteStaysValidUTF8AtEveryTruncationOffset(t *testing.T) {
	for pad := range 8 {
		baseline := driftBaseline{}
		for range 40 {
			baseline.Devices = append(baseline.Devices, driftBaselineDevice{Aliases: []string{
				"/dev/dri/" + strings.Repeat("x", pad) + strings.Repeat("é", 12)}})
		}
		note := baseline.note(capabilityDrift{})
		if note == "" {
			t.Fatalf("pad %d: a regression produced no note", pad)
		}
		if len(note) <= maxCapabilityDriftNoteBytes {
			t.Fatalf("pad %d: note is %d bytes, the fixture must exceed the bound", pad, len(note))
		}
		if !utf8.ValidString(note) {
			t.Fatalf("pad %d: truncated note is not valid UTF-8: %q", pad, note)
		}
	}
}

// The operator-triggered re-probe stores the node's new report immediately, and
// must go through the same fetch, drift, and publish path the sweep uses rather
// than a second implementation.
func TestRefreshNodeCapabilitiesStoresImmediately(t *testing.T) {
	url := newHealthNode(t, "sha256:degraded")
	fetcher := &fakeCapabilityFetcher{payload: []byte(degradedCapabilityPayload), hash: "sha256:degraded"}
	node := &Node{
		ID: 1, Name: "gpu-1", Type: NodeTypeTranscode, URL: url, Enabled: true,
		Capabilities:     json.RawMessage(testCapabilityPayload),
		CapabilitiesHash: strPtr("sha256:old"),
	}
	fixture := newCapabilityCheckerFixture(t, node, fetcher)

	if err := fixture.checker.RefreshNodeCapabilities(context.Background(), node); err != nil {
		t.Fatalf("RefreshNodeCapabilities: %v", err)
	}

	stored := fixture.storedNode(t)
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:degraded" {
		t.Fatalf("stored hash = %v, want the refetched report", stored.CapabilitiesHash)
	}
	if stored.CapabilityDrift == nil {
		t.Fatal("an immediate refresh did not persist the drift the sweep would have")
	}
	// The capability cache must be told, exactly as on a sweep refresh.
	if notifications := fixture.notifications(); len(notifications) != 1 || notifications[0] != url {
		t.Fatalf("notifications = %v, want one for %s", notifications, url)
	}
	if got := fetcher.callCount(); got != 1 {
		t.Fatalf("fetch calls = %d, want exactly one", got)
	}
}

// A refresh already running is at least as fresh as the one being asked for, so
// the second caller is told rather than starting a duplicate fetch.
func TestRefreshNodeCapabilitiesRefusesWhenAlreadyInFlight(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	node := &Node{ID: 1, Name: "gpu-1", Type: NodeTypeTranscode, URL: url, Enabled: true}
	fixture := newCapabilityCheckerFixture(t, node, fetcher)

	fixture.checker.capabilityRefreshInFlight.Store(node.ID, struct{}{})
	defer fixture.checker.capabilityRefreshInFlight.Delete(node.ID)

	if err := fixture.checker.RefreshNodeCapabilities(context.Background(), node); err == nil {
		t.Fatal("a duplicate refresh was allowed to start")
	}
	if got := fetcher.callCount(); got != 0 {
		t.Fatalf("fetch calls = %d, want none while a refresh is in flight", got)
	}
}

// Skipped means no probe ran, because the node cannot open the backend's
// configured devices — a statement about access, not about hardware. Counting
// it as a loss also contradicted hardwareProbesClean, which treats a skipped
// backend as clean: the note would be set by one rule and cleared by the other
// on the next hash change, flapping with nothing having changed.
func TestComputeCapabilityDriftDoesNotTreatASkippedBackendAsLost(t *testing.T) {
	const before = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	const after = `{"resolved":"none","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":false,"skipped":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("both reports should parse")
	}
	if len(drift.lostBackends) != 0 {
		t.Fatalf("lostBackends = %v, want a skipped backend not counted as lost", drift.lostBackends)
	}
	// And the pair round-trips: what does not set the note must not be held
	// open by it either.
	standing := "verified hardware backends lost: qsv"
	if got, _ := resolveDriftNote(&standing, nil, drift, parsed, []byte(after)); got == nil || *got != standing {
		t.Fatalf("resolveDriftNote = %v, want a skipped report to leave a standing note alone", got)
	}
}

// Two GPUs going one at a time: the note and the latch have to name the same
// thing. Built from the delta alone, the second loss replaced the first in the
// text while the baseline still waited for both — so after the visible GPU came
// back the warning stayed, naming hardware the operator could no longer see.
func TestResolveDriftNoteNamesEveryOutstandingLoss(t *testing.T) {
	const both = `{"resolved":"qsv","render_devices":["/dev/dri/renderD130"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD130","pci_address":"0000:05:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`

	standing := "render devices gone: /dev/dri/renderD128"
	firstLoss := []byte(`{"devices":[{"aliases":["0000:03:00.0","/dev/dri/renderD128"]}]}`)
	// A second card goes while the first is still missing.
	const before = `{"resolved":"qsv","render_devices":["/dev/dri/renderD129","/dev/dri/renderD130"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD129","pci_address":"0000:04:00.0"},` +
		`{"path":"/dev/dri/renderD130","pci_address":"0000:05:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	drift, parsed := computeCapabilityDrift([]byte(before), []byte(both))
	if !parsed {
		t.Fatal("drift not parsed")
	}

	note, baseline := resolveDriftNote(&standing, firstLoss, drift, parsed, []byte(both))
	if note == nil {
		t.Fatal("capability_drift cleared while two cards are missing")
	}
	for _, want := range []string{"/dev/dri/renderD128", "/dev/dri/renderD129"} {
		if !strings.Contains(*note, want) {
			t.Fatalf("capability_drift = %q, want it to name %s — the baseline is still waiting on it", *note, want)
		}
	}

	// And the note keeps agreeing with the latch: the first card returning is
	// not enough, and what remains is still named.
	const firstBack = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128","/dev/dri/renderD130"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"},` +
		`{"path":"/dev/dri/renderD130","pci_address":"0000:05:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	drift, parsed = computeCapabilityDrift([]byte(firstBack), []byte(firstBack))
	still, _ := resolveDriftNote(note, baseline, drift, parsed, []byte(firstBack))
	if still == nil {
		t.Fatal("capability_drift cleared with the second card still missing")
	}
	if !strings.Contains(*still, "/dev/dri/renderD129") {
		t.Fatalf("capability_drift = %q, want the still-missing card named", *still)
	}
}
