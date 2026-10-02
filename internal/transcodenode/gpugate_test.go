package transcodenode

import "testing"

// The bug this gate exists for: a node idle at a point-in-time check accepts a
// transcode milliseconds later, and the re-probe's smoke encode — minutes long
// — then races a live encoder session and publishes working hardware as failed.
// Admitted work has to keep the re-probe out even before it is visible as an
// active job.
func TestGPUGateRefusesReprobeWhileWorkIsAdmitted(t *testing.T) {
	var gate gpuGate

	if !gate.beginWork() {
		t.Fatal("beginWork on an idle gate was refused")
	}
	// activeJobs is still 0 here: the counter only moves once ffmpeg is running,
	// which is exactly the window a point-in-time check missed.
	if busy, ok := gate.beginReprobe(otherWork(0)); ok {
		t.Fatal("re-probe admitted while a transcode was starting")
	} else if busy != 1 {
		t.Fatalf("busy = %d, want the admitted work counted", busy)
	}

	gate.endWork()
	if _, ok := gate.beginReprobe(otherWork(0)); !ok {
		t.Fatal("re-probe refused after the work finished")
	}
}

// The node's own running-session count is read under the same lock, so "no
// admitted work" and "no active jobs" cannot be true at two different instants.
func TestGPUGateRefusesReprobeWhileJobsAreActive(t *testing.T) {
	var gate gpuGate

	busy, ok := gate.beginReprobe(otherWork(2))
	if ok {
		t.Fatal("re-probe admitted on a node running transcodes")
	}
	if busy != 2 {
		t.Fatalf("busy = %d, want 2", busy)
	}
}

// A re-probe holds the encoder for the whole rebuild, so work arriving mid-probe
// is refused rather than queued: a viewer pressing play must not wait minutes
// for a probe, and the API retries elsewhere.
func TestGPUGateRefusesWorkWhileReprobing(t *testing.T) {
	var gate gpuGate

	if _, ok := gate.beginReprobe(otherWork(0)); !ok {
		t.Fatal("re-probe refused on an idle gate")
	}
	if gate.beginWork() {
		t.Fatal("GPU work admitted while a re-probe held the encoder")
	}
	if _, ok := gate.beginReprobe(otherWork(0)); ok {
		t.Fatal("a second concurrent re-probe was admitted")
	}

	gate.endReprobe()
	if !gate.beginWork() {
		t.Fatal("GPU work refused after the re-probe released the encoder")
	}
	gate.endWork()
}

// endWork must not drive the counter negative, or one unbalanced release would
// let a re-probe run beside real transcodes forever.
func TestGPUGateEndWorkDoesNotUnderflow(t *testing.T) {
	var gate gpuGate

	gate.endWork()
	gate.endWork()
	if !gate.beginWork() {
		t.Fatal("beginWork refused after unbalanced releases")
	}
	if _, ok := gate.beginReprobe(otherWork(0)); ok {
		t.Fatal("re-probe admitted while one unit of work was outstanding")
	}
}

// Teardown is GPU work too, and it cannot be refused: a stop must always
// proceed. TranscodeSession.Close waits for ffmpeg to exit, so the encoder holds
// its GPU session for the whole call while activeJobs has already dropped —
// counted by neither unless the gate holds it.
func TestGPUGateHoldWorkIsNeverRefusedAndKeepsReprobesOut(t *testing.T) {
	var gate gpuGate

	gate.holdWork()
	if busy, ok := gate.beginReprobe(otherWork(0)); ok {
		t.Fatal("re-probe admitted while a session was still closing")
	} else if busy != 1 {
		t.Fatalf("busy = %d, want the closing session counted", busy)
	}

	gate.endWork()
	if _, ok := gate.beginReprobe(otherWork(0)); !ok {
		t.Fatal("re-probe refused after the teardown finished")
	}

	// A re-probe already holding the encoder must not turn a stop into a hang or
	// a refusal; the teardown is registered regardless.
	gate.holdWork()
	gate.endWork()
}

// A hardware probe runs on a background context so an abandoned caller cannot
// kill work another request is waiting on. The capability build therefore
// releases its gate claim while ffmpeg may still be encoding, and a re-probe
// that counted only this node's own bookkeeping would claim an encoder that is
// not free — its smoke matrix racing the one already running, publishing the
// false hardware verdict this gate exists to prevent.
func TestGPUGateRefusesReprobeWhileADetachedProbeRuns(t *testing.T) {
	var gate gpuGate

	busy, ok := gate.beginReprobe(otherWork(1))
	if ok {
		t.Fatal("re-probe admitted while a detached smoke encode was still running")
	}
	if busy != 1 {
		t.Fatalf("busy = %d, want the detached probe counted", busy)
	}

	if _, ok := gate.beginReprobe(otherWork(0)); !ok {
		t.Fatal("re-probe refused once no probe was in flight")
	}
}

// Tone-map probes detach from their caller exactly as hardware probes do, and
// they run their own FFmpeg smoke encodes, so a re-probe that counted only the
// hardware ones would start a second matrix beside a running first.
func TestGPUGateCountsEveryDetachedProbeSource(t *testing.T) {
	var gate gpuGate

	busy, ok := gate.beginReprobe(otherWork(2))
	if ok {
		t.Fatal("re-probe admitted while detached smoke encodes were still running")
	}
	if busy != 2 {
		t.Fatalf("busy = %d, want both detached probe sources counted", busy)
	}
}

// otherWork builds the callback beginReprobe reads under its own lock, for
// tests that want a fixed count.
func otherWork(count int) func() int { return func() int { return count } }

// The count has to be read under the lock that grants the claim, not sampled
// before it. A request descheduled between reading zero and acquiring the lock
// would otherwise claim an encoder that a capability build has since started
// and abandoned, leaving that build's background probe running.
func TestGPUGateReadsOtherWorkUnderTheClaimLock(t *testing.T) {
	var gate gpuGate

	// Whatever this reports at the moment of the claim is what decides it: a
	// probe that starts between the caller's own check and the lock is seen.
	appeared := false
	busy, ok := gate.beginReprobe(func() int {
		appeared = true
		return 1
	})
	if !appeared {
		t.Fatal("beginReprobe did not consult the count while holding the lock")
	}
	if ok {
		t.Fatal("re-probe admitted despite work appearing at claim time")
	}
	if busy != 1 {
		t.Fatalf("busy = %d, want the work counted at claim time", busy)
	}

	if _, ok := gate.beginReprobe(nil); !ok {
		t.Fatal("re-probe refused with nothing else to count")
	}
}
