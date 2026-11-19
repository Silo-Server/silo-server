package nodepool

import (
	"context"
	"encoding/json"
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
