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
		CapabilitiesHash: stringPtr("sha256:old"),
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
		CapabilitiesHash: stringPtr("sha256:degraded"),
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
		CapabilitiesHash: stringPtr("sha256:degraded"),
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
		CapabilitiesHash: stringPtr("sha256:degraded"),
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

// Clearing the note requires evidence that hardware came back, and a report in
// which nothing was probed carries none.
//
// The two shapes that produce no passing probe are the two ways hardware goes
// away: a device the node can no longer open reports the backend `skipped`, and
// a card that is gone entirely leaves no candidate backend to report at all.
// Both used to read as clean — the first because a skipped backend is not a
// failure, the second because a loop over an empty list finds none — so a
// standing regression was erased by the next unrelated hash change (a reboot
// moving boot_id is enough), telling an operator a still-broken node had
// recovered.
//
// A proxy pointed at a cluster-wide hw_device does not get stuck behind this:
// it never verified those backends in the first place, so computeCapabilityDrift
// never gives it a note to hold open.
func TestResolveDriftNoteKeepsNoteWhenNoProbePassed(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name: "every candidate device is inaccessible",
			payload: `{"resolved":"none","render_devices":[],` +
				`"detected_backends":[{"backend":"vaapi","verified":false,"skipped":true}]}`,
		},
		{
			name:    "the gpu is gone, so nothing was a candidate",
			payload: `{"resolved":"none","render_devices":[],"detected_backends":[]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			standing := "verified hardware backends lost: vaapi"
			payload := []byte(test.payload)
			// Both sides degraded: the delta finds nothing newly lost, which is
			// exactly the state in which clearing has to be refused.
			drift, parsed := computeCapabilityDrift(payload, payload)
			got, _ := resolveDriftNote(&standing, nil, drift, parsed, payload)
			if got == nil {
				t.Fatal("capability_drift was cleared by a report in which no probe passed")
			}
			if *got != standing {
				t.Fatalf("capability_drift = %q, want the standing note %q", *got, standing)
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

// Growth is not repair. A node standing on a lost GPU that gains an unrelated
// one has a bigger, perfectly clean inventory and still has not got its card
// back — which is why the note keeps what it is waiting for rather than
// re-deriving it from the stored report.
func TestResolveDriftNoteKeepsNoteWhenAnUnrelatedGPUIsAdded(t *testing.T) {
	const gained = `{"resolved":"vaapi","render_devices":["/dev/dri/renderD130"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD130","pci_address":"0000:09:00.0"}],` +
		`"detected_backends":[{"backend":"vaapi","verified":true}]}`

	standing := "render devices gone: /dev/dri/renderD128"
	// The lost card, by every identity it answered to.
	baseline := []byte(`{"devices":[{"aliases":["0000:03:00.0","/dev/dri/renderD128"]}]}`)
	payload := []byte(gained)
	drift, parsed := computeCapabilityDrift(payload, payload)

	got, gotBaseline := resolveDriftNote(&standing, baseline, drift, parsed, payload)
	if got == nil {
		t.Fatal("capability_drift cleared because an unrelated GPU appeared")
	}
	if *got != standing {
		t.Fatalf("capability_drift = %q, want the standing note %q", *got, standing)
	}
	if len(gotBaseline) == 0 {
		t.Fatal("the baseline was dropped while the note still stands")
	}
}

// The lost card coming back under a renumbered render node still counts: the
// baseline keeps every identity it answered to, and any one of them matching
// identifies it.
func TestResolveDriftNoteClearsWhenTheLostCardReturnsRenumbered(t *testing.T) {
	const back = `{"resolved":"vaapi","render_devices":["/dev/dri/renderD129"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD129","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"vaapi","verified":true}]}`

	standing := "render devices gone: /dev/dri/renderD128"
	baseline := []byte(`{"devices":[{"aliases":["0000:03:00.0","/dev/dri/renderD128"]}]}`)
	payload := []byte(back)
	drift, parsed := computeCapabilityDrift(payload, payload)

	if got, _ := resolveDriftNote(&standing, baseline, drift, parsed, payload); got != nil {
		t.Fatalf("capability_drift = %q, want the card at the same slot to clear it", *got)
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

// A multi-GPU node that lost one card keeps probing the survivor cleanly, and
// once the degraded report is stored the delta finds nothing lost ever again.
// Clearing on that generic success told an operator the node recovered while
// one of its cards was still missing.
func TestResolveDriftNoteKeepsNoteWhileASiblingGPUIsStillMissing(t *testing.T) {
	// Two cards before the loss, one after — and the survivor verifies, so
	// every probe that ran passes.
	const degraded = `{"resolved":"vaapi","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"vaapi","verified":true}]}`

	standing := "render devices gone: /dev/dri/renderD129"
	baseline := []byte(`{"devices":[{"aliases":["0000:04:00.0","/dev/dri/renderD129"]}]}`)
	payload := []byte(degraded)
	// The next refetch is degraded-to-degraded: nothing newly lost, and every
	// probe that ran passed, because the survivor is fine.
	drift, parsed := computeCapabilityDrift(payload, payload)
	if !hardwareProbesClean(payload) {
		t.Fatal("the surviving card should probe cleanly; that is the point")
	}
	got, _ := resolveDriftNote(&standing, baseline, drift, parsed, payload)
	if got == nil {
		t.Fatal("capability_drift cleared while the lost card was still missing")
	}
	if *got != standing {
		t.Fatalf("capability_drift = %q, want the standing note %q", *got, standing)
	}
}

// A node's very first report has nothing to compare against, so it must not be
// flagged.
func TestHealthCheckerStoresNoDriftOnFirstReport(t *testing.T) {
	url := newHealthNode(t, "sha256:new")
	fetcher := &fakeCapabilityFetcher{payload: []byte(testCapabilityPayload), hash: "sha256:new"}
	fixture := newCapabilityCheckerFixture(t, &Node{
		ID: 1, Name: "gpu-1", Type: NodeTypeTranscode, URL: url, Enabled: true,
	}, fetcher)

	fixture.sweep()

	if stored := fixture.storedNode(t); stored.CapabilityDrift != nil {
		t.Fatalf("capability_drift = %q on a first report, want none", *stored.CapabilityDrift)
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
	drift := capabilityDrift{}
	for range 400 {
		drift.lostDevices = append(drift.lostDevices, "/dev/dri/renderD128")
	}
	note := drift.persistedNote()
	if note == nil {
		t.Fatal("a regression produced no note")
	}
	if len(*note) > maxCapabilityDriftNoteBytes+3 {
		t.Fatalf("note is %d bytes, want it bounded at %d", len(*note), maxCapabilityDriftNoteBytes)
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
		drift := capabilityDrift{}
		for range 40 {
			drift.lostDevices = append(drift.lostDevices,
				"/dev/dri/"+strings.Repeat("x", pad)+strings.Repeat("é", 12))
		}
		note := drift.persistedNote()
		if note == nil {
			t.Fatalf("pad %d: a regression produced no note", pad)
		}
		if len(*note) <= maxCapabilityDriftNoteBytes {
			t.Fatalf("pad %d: note is %d bytes, the fixture must exceed the bound", pad, len(*note))
		}
		if !utf8.ValidString(*note) {
			t.Fatalf("pad %d: truncated note is not valid UTF-8: %q", pad, *note)
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
		CapabilitiesHash: stringPtr("sha256:old"),
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

// DRM is free to hand the same card a different renderD number across a reboot.
// Comparing enumeration paths alone then reports a GPU as gone, and because the
// reboot moves boot_id it also triggers the refetch that persists the note — so
// an operator sees a hardware regression for a card that never moved.
func TestComputeCapabilityDriftMatchesRenumberedRenderDevices(t *testing.T) {
	const before = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	const after = `{"resolved":"qsv","render_devices":["/dev/dri/renderD129"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD129","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("both reports should parse")
	}
	if drift.regressed() {
		t.Fatalf("drift = %+v, want a renumbered path at the same PCI slot to be no regression", drift)
	}
}

// A card that genuinely goes away has neither its path nor its slot in the new
// report, and must still be caught.
func TestComputeCapabilityDriftStillCatchesARemovedDevice(t *testing.T) {
	const before = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128","/dev/dri/renderD129"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"},` +
		`{"path":"/dev/dri/renderD129","pci_address":"0000:04:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	const after = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("both reports should parse")
	}
	if len(drift.lostDevices) != 1 || drift.lostDevices[0] != "/dev/dri/renderD129" {
		t.Fatalf("lostDevices = %v, want the card at 0000:04:00.0 reported gone", drift.lostDevices)
	}
}

// An NVIDIA uuid outranks the slot, so a card moved between slots is still the
// same card.
func TestComputeCapabilityDriftMatchesAMovedCardByUUID(t *testing.T) {
	const before = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-abc"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`
	const after = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD130"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD130","pci_address":"0000:07:00.0","gpu_uuid":"GPU-abc"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed || drift.regressed() {
		t.Fatalf("drift = %+v (parsed=%v), want the same uuid to be the same card", drift, parsed)
	}
}

// A node that predates render_device_details reports paths only, and must still
// be comparable.
func TestComputeCapabilityDriftFallsBackToPathsWithoutDetails(t *testing.T) {
	const before = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	const after = `{"resolved":"none","render_devices":[],` +
		`"detected_backends":[{"backend":"qsv","verified":false}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("both reports should parse")
	}
	if len(drift.lostDevices) != 1 || drift.lostDevices[0] != "/dev/dri/renderD128" {
		t.Fatalf("lostDevices = %v, want the path-only device reported gone", drift.lostDevices)
	}
}

// nvidia-smi is queried behind a circuit breaker, so the same NVIDIA card
// publishes a uuid on one pass and only its PCI address on another. Keeping
// just the strongest identity made those two reports describe different
// devices, persisting a "render device gone" note for a card that never moved.
func TestComputeCapabilityDriftMatchesAcrossIdentityStrength(t *testing.T) {
	const withUUID = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-abc"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`
	const withoutUUID = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`

	for _, test := range []struct{ name, before, after string }{
		{"uuid disappears", withUUID, withoutUUID},
		{"uuid appears", withoutUUID, withUUID},
	} {
		t.Run(test.name, func(t *testing.T) {
			drift, parsed := computeCapabilityDrift([]byte(test.before), []byte(test.after))
			if !parsed {
				t.Fatal("both reports should parse")
			}
			if drift.regressed() {
				t.Fatalf("drift = %+v, want a shared PCI alias to identify the same card", drift)
			}
		})
	}
}

// A replacement card in the same slot keeps the slot's PCI address and usually
// the render path too, so matching on any shared alias would hide the old card's
// disappearance entirely. Two permanent uuids that disagree settle it.
func TestComputeCapabilityDriftReportsAReplacedCardInTheSameSlot(t *testing.T) {
	const before = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-old"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`
	const after = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-new"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("both reports should parse")
	}
	if len(drift.lostDevices) != 1 || drift.lostDevices[0] != "/dev/dri/renderD128" {
		t.Fatalf("lostDevices = %v, want the replaced card reported gone", drift.lostDevices)
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

// A backend that fails its probe outright is still a loss — the distinction is
// "could not try" versus "tried and the driver said no".
func TestComputeCapabilityDriftStillCatchesAFailedBackend(t *testing.T) {
	const before = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	const after = `{"resolved":"none","render_devices":["/dev/dri/renderD128"],` +
		`"detected_backends":[{"backend":"qsv","verified":false,"reason":"h264_qsv smoke encode failed"}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("both reports should parse")
	}
	if len(drift.lostBackends) != 1 || drift.lostBackends[0] != "qsv" {
		t.Fatalf("lostBackends = %v, want the failing backend reported", drift.lostBackends)
	}
}

// A GPU that actually disappears is caught by the device comparison, which reads
// the host's own inventory and owes nothing to the configuration. The backend
// going unreported alongside it is a consequence, not separate evidence.
func TestComputeCapabilityDriftCatchesAVanishedGPUAsADeviceLoss(t *testing.T) {
	const before = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	const after = `{"resolved":"none","render_devices":[],"detected_backends":[]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("both reports should parse")
	}
	if !drift.regressed() {
		t.Fatalf("drift = %+v, want a vanished GPU recorded", drift)
	}
	if len(drift.lostDevices) != 1 || drift.lostDevices[0] != "/dev/dri/renderD128" {
		t.Fatalf("lostDevices = %v, want the vanished card reported", drift.lostDevices)
	}
}

// Detection only probes the backends the configured hw_device gives it
// candidates for, so repointing a node from a QSV render path to an NVENC index
// legitimately stops QSV being reported. Treating that as a disappeared backend
// latched a warning demanding QSV verify again on a node deliberately configured
// for NVENC — which nothing could ever satisfy, so the false incident could
// never clear.
func TestComputeCapabilityDriftIgnoresABackendThePolicyStoppedProbing(t *testing.T) {
	// The host's inventory is unchanged; only what was probed moved.
	const before = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true}]}`
	const after = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("both reports should parse")
	}
	if drift.regressed() {
		t.Fatalf("drift = %+v, want a policy change not recorded as hardware loss", drift)
	}
}

// A note written before the baseline column existed has nothing recorded to wait
// for. Holding it forever would strand it on an upgraded deployment, so a clean
// report clears it — the best evidence available for a note whose subject was
// never captured.
func TestResolveDriftNoteClearsALegacyNoteWithNoBaseline(t *testing.T) {
	const clean = `{"resolved":"vaapi","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"vaapi","verified":true}]}`

	standing := "verified hardware backends lost: vaapi"
	payload := []byte(clean)
	drift, parsed := computeCapabilityDrift(payload, payload)

	if got, _ := resolveDriftNote(&standing, nil, drift, parsed, payload); got != nil {
		t.Fatalf("capability_drift = %q, want a baseline-less note cleared by a clean report", *got)
	}
}

// A replacement card in the same slot inherits the PCI address and usually the
// render path. Clearing on that would report the lost card as returned when a
// different one arrived — the same false match the loss comparison already
// refuses, applied to the recovery side.
func TestResolveDriftNoteKeepsNoteWhenAReplacementCardTakesTheSlot(t *testing.T) {
	const replaced = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0","gpu_uuid":"GPU-new"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`

	standing := "render devices gone: /dev/dri/renderD128"
	baseline := []byte(`{"devices":[{"uuid":"GPU-old","aliases":["GPU-old","0000:03:00.0","/dev/dri/renderD128"]}]}`)
	payload := []byte(replaced)
	drift, parsed := computeCapabilityDrift(payload, payload)

	got, _ := resolveDriftNote(&standing, baseline, drift, parsed, payload)
	if got == nil {
		t.Fatal("capability_drift cleared because a different card took the slot")
	}
	if *got != standing {
		t.Fatalf("capability_drift = %q, want the standing note %q", *got, standing)
	}
}

// The original card returning does clear it, even under a renumbered path.
func TestResolveDriftNoteClearsWhenTheSameCardReturnsByUUID(t *testing.T) {
	const back = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD131"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD131","pci_address":"0000:09:00.0","gpu_uuid":"GPU-old"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`

	standing := "render devices gone: /dev/dri/renderD128"
	baseline := []byte(`{"devices":[{"uuid":"GPU-old","aliases":["GPU-old","0000:03:00.0","/dev/dri/renderD128"]}]}`)
	payload := []byte(back)
	drift, parsed := computeCapabilityDrift(payload, payload)

	if got, _ := resolveDriftNote(&standing, baseline, drift, parsed, payload); got != nil {
		t.Fatalf("capability_drift = %q, want the same uuid in a new slot to clear it", *got)
	}
}

// A mixed host can carry a backend that has never worked — VAAPI failing beside
// a working QSV is ordinary. Requiring every backend to verify before clearing
// meant a note about a lost render device latched forever, long after that
// device came back and the backend that used it verified again.
func TestResolveDriftNoteClearsDespiteAnUnrelatedFailingBackend(t *testing.T) {
	const recovered = `{"resolved":"qsv","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":true},{"backend":"vaapi","verified":false,"reason":"no driver"}]}`

	standing := "render devices gone: /dev/dri/renderD128"
	baseline := []byte(`{"backends":["qsv"],"devices":[{"aliases":["0000:03:00.0","/dev/dri/renderD128"]}]}`)
	payload := []byte(recovered)
	drift, parsed := computeCapabilityDrift(payload, payload)

	if got, _ := resolveDriftNote(&standing, baseline, drift, parsed, payload); got != nil {
		t.Fatalf("capability_drift = %q, want it cleared: the lost device is back and qsv verifies", *got)
	}
}

// The baseline still decides. A backend the note is waiting on that has not come
// back keeps it latched, however healthy the rest of the report looks.
func TestResolveDriftNoteKeepsNoteWhenTheBaselineBackendIsStillFailing(t *testing.T) {
	const stillBroken = `{"resolved":"vaapi","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],` +
		`"detected_backends":[{"backend":"qsv","verified":false,"reason":"no driver"},{"backend":"vaapi","verified":true}]}`

	standing := "backends no longer verifying: qsv"
	baseline := []byte(`{"backends":["qsv"]}`)
	payload := []byte(stillBroken)
	drift, parsed := computeCapabilityDrift(payload, payload)

	got, _ := resolveDriftNote(&standing, baseline, drift, parsed, payload)
	if got == nil {
		t.Fatal("capability_drift cleared while the backend it names is still failing")
	}
}

// A report with nothing probed is not evidence of anything. Every backend
// skipped means the node could not open its configured devices, which says
// nothing about the hardware the note is waiting on.
func TestResolveDriftNoteKeepsNoteWhenEveryBackendWasSkipped(t *testing.T) {
	const allSkipped = `{"resolved":"none","render_devices":[],` +
		`"detected_backends":[{"backend":"qsv","skipped":true},{"backend":"vaapi","skipped":true}]}`

	standing := "render devices gone: /dev/dri/renderD128"
	baseline := []byte(`{"devices":[{"aliases":["/dev/dri/renderD128"]}]}`)
	payload := []byte(allSkipped)
	drift, parsed := computeCapabilityDrift(payload, payload)

	if got, _ := resolveDriftNote(&standing, baseline, drift, parsed, payload); got == nil {
		t.Fatal("capability_drift cleared on a report where nothing was probed")
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

// The ordinary NVENC container has /dev/nvidia* and the toolkit and no /dev/dri,
// so its cards exist only in nvidia_gpu_uuids. Losing one moves nothing in
// render_devices, and the backend comparison does not cover it either: NVENC
// stops being a candidate the moment the device nodes go away, and an absent
// backend is deliberately not a lost one.
func TestCapabilityDriftNotesAnNVIDIAOnlyCardGoingAway(t *testing.T) {
	const before = `{"resolved":"nvenc","nvidia_gpu_uuids":["GPU-aaa","GPU-bbb"],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`
	// One card left. NVENC still verifies on it, so nothing else in the report
	// says anything was lost.
	const after = `{"resolved":"nvenc","nvidia_gpu_uuids":["GPU-bbb"],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("drift did not parse")
	}
	if !drift.regressed() {
		t.Fatal("a card that disappeared produced no drift")
	}
	if got := drift.lostDevices; len(got) != 1 || got[0] != "GPU-aaa" {
		t.Fatalf("lost devices = %v, want the missing GPU-aaa", got)
	}
	if len(drift.lostBackends) != 0 {
		t.Fatalf("lost backends = %v, want none — NVENC still verifies on the remaining card", drift.lostBackends)
	}
}

// nvidia-smi sits behind a circuit breaker and can be missing from an image
// outright, so an empty uuid list is not evidence a card is gone. NVENC is only
// probed where /dev/nvidia* opens, so a report still carrying it describes a
// node whose cards are present and whose query tool is not — latching drift
// there would demand a uuid come back that nothing on the node can produce.
func TestCapabilityDriftIgnoresNVIDIAUUIDsLostWithTheQueryTool(t *testing.T) {
	const before = `{"resolved":"nvenc","nvidia_gpu_uuids":["GPU-aaa"],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`
	const after = `{"resolved":"nvenc","detected_backends":[{"backend":"nvenc","verified":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("drift did not parse")
	}
	if drift.regressed() {
		t.Fatalf("nvidia-smi going quiet was read as hardware loss: %+v", drift)
	}
}

// A card with both a render node and an nvidia-smi entry is one card. Counting
// it twice would report a device gone whenever the two sources disagree about
// which of them can currently see it.
func TestCapabilityDriftCountsOneCardOnceAcrossBothIdentitySources(t *testing.T) {
	const before = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128","gpu_uuid":"GPU-aaa"}],` +
		`"nvidia_gpu_uuids":["GPU-aaa"],"detected_backends":[{"backend":"nvenc","verified":true}]}`
	// nvidia-smi went quiet; the render node still reports the same card.
	const after = `{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"],` +
		`"render_device_details":[{"path":"/dev/dri/renderD128"}],` +
		`"detected_backends":[{"backend":"nvenc","verified":true}]}`

	drift, parsed := computeCapabilityDrift([]byte(before), []byte(after))
	if !parsed {
		t.Fatal("drift did not parse")
	}
	if drift.regressed() {
		t.Fatalf("one card read as two: %+v", drift)
	}
}
