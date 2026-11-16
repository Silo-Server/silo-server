package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// hevcFatalErrorLine mirrors the prod stderr shape from an HEVC decoder
// refusing an invalid packet: the bitstream itself is undecodable.
func hevcFatalErrorLine() string {
	return `[vist#0:0/hevc @ 0x55d0] [dec:hevc @ 0x55d1] Error submitting packet to decoder: Invalid data found when processing input`
}

// hevcPOCErrorLine mirrors the prod stderr shape from an HEVC decoder
// priming its reference lists on a Dolby Vision profile 8 stream. It is a
// warning, not a source verdict: live sessions emitted a bounded burst and then
// produced valid, decodable segments.
func hevcPOCErrorLine() string {
	return `[hevc @ 0x55d0] Could not find ref with POC 16`
}

// TestDecodeErrorLineMatchesOnlyDecoderFailures proves which stderr lines count
// as a decoder rejecting the source and which are encoder/output/HLS/network
// chatter or tolerable reference-list warnings. The negative set mirrors
// TestNonDemuxErrorsDoNotCount's discipline.
func TestDecodeErrorLineMatchesOnlyDecoderFailures(t *testing.T) {
	positive := []string{
		`[vist#0:0/hevc @ 0x55d0] [dec:hevc @ 0x55d1] Error submitting packet to decoder: Invalid data found when processing input`,
		`[hevc @ 0x55d0] Invalid NAL unit size (1215484279 > 206).`,
		`[hevc @ 0x55d0] Error splitting the input into NAL units.`,
		`[h264 @ 0x55d0] Failed to decode picture`,
		`[hevc @ 0x55d0] decode_slice_header error`,
	}
	for _, line := range positive {
		if !decodeErrorLine(line) {
			t.Errorf("decodeErrorLine(%q) = false, want true", line)
		}
	}
	negative := []string{
		// Reference-list warnings: a bounded opening burst on a playable Dolby
		// Vision profile 8 source, verified to keep producing decodable segments.
		`[hevc @ 0x55d0] Could not find ref with POC 16`,
		`[hevc @ 0x55d0] Error constructing the frame RPS`,
		`[h264_qsv @ 0x55d0] Error writing trailer: Broken pipe`,
		`[hls @ 0x55d0] Error writing trailer`,
		`[https @ 0x55d0] HTTP error 503 Service Unavailable, reconnecting`,
		`[hevc @ 0x55d0] Reinit context to 1920x1080, pix_fmt yuv420p`,
		`frame=  120 fps=0.0 q=-0.0 size=   512kB time=00:00:05.00 bitrate= 838.9kbits/s`,
	}
	for _, line := range negative {
		if decodeErrorLine(line) {
			t.Errorf("decodeErrorLine(%q) = true, want false", line)
		}
	}
}

// TestReferenceListWarningsDoNotRejectSource proves the decoder stderr shape
// that wrongly fatalised playable Dolby Vision profile 8 content no longer
// records a source rejection, even well past the failure threshold and even on
// a hardware plan. A live session emitted 170 such lines and still produced
// decodable segments.
func TestReferenceListWarningsDoNotRejectSource(t *testing.T) {
	marked := make(chan struct{}, 4)
	opts := TranscodeOpts{
		MediaFileID:        91,
		CanonicalInputPath: "virtual://movie/tt1?result=poc",
		TargetCodecVideo:   "av1",
		OnSourceRejected: func(context.Context, int, string) error {
			marked <- struct{}{}
			return nil
		},
	}
	s := &TranscodeSession{opts: opts}
	ctx := context.Background()
	for i := 0; i < decodeErrorThreshold*20; i++ {
		s.logFFmpegLine(ctx, hevcPOCErrorLine())
		s.logFFmpegLine(ctx, `[hevc @ 0x55d0] Error constructing the frame RPS`)
	}
	if s.IsSourceRejected() {
		t.Fatal("reference-list warnings recorded a source rejection")
	}
	if s.IsDecodeFailed() {
		t.Fatal("reference-list warnings claimed the hardware decoder failed")
	}
	select {
	case <-marked:
		t.Fatal("reference-list warnings invoked the source-rejected marker")
	default:
	}
}

// TestDecodeFailureStampsAfterObservationWindow covers the lifecycle: the
// threshold enters suspicion but does not stamp, the stamp lands only on
// expiry, and it never reports twice.
func TestDecodeFailureStampsAfterObservationWindow(t *testing.T) {
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "h264"}}
	clock := attachDecodeClock(s, time.Unix(2000, 0))
	for i := 0; i < decodeErrorThreshold-1; i++ {
		if s.observeDecodeError(hevcFatalErrorLine()) {
			t.Fatalf("suspected after only %d errors", i+1)
		}
	}
	if s.IsDecodeFailed() {
		t.Fatal("session latched the hardware decoder before the threshold")
	}
	if !s.observeDecodeError(hevcFatalErrorLine()) {
		t.Fatal("not suspected when the threshold is reached")
	}
	// Suspicion latches the hardware decoder (licensing the reactive software
	// retry) but must not confirm the source before the observation window.
	if !s.IsDecodeFailed() {
		t.Fatal("session did not latch the hardware decoder at suspicion")
	}
	if s.IsSourceRejected() {
		t.Fatal("session confirmed the source at the threshold, before the observation window")
	}
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	if !s.IsSourceRejected() {
		t.Fatal("session not confirmed after the observation window")
	}
	// A further error and evaluation must not re-stamp.
	if s.observeDecodeError(hevcFatalErrorLine()) {
		t.Fatal("stamp reported more than once")
	}
	s.evaluateDecodeVerdict()
	if got := decodeStageOf(s); got != decodeStageConfirmed {
		t.Fatalf("confirmed generation reopened to stage %d", got)
	}
}

// TestDecodeFailureDecaysAfterQuietWindow proves a healthy stretch resets the
// counter so failures spread past the window never accumulate into suspicion.
func TestDecodeFailureDecaysAfterQuietWindow(t *testing.T) {
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "h264"}}
	clock := attachDecodeClock(s, time.Unix(3000, 0))
	for i := 0; i < decodeErrorThreshold-1; i++ {
		s.observeDecodeError(hevcFatalErrorLine())
	}
	// More than decodeErrorDecay after the previous failure: the counter resets,
	// so this is a fresh first strike and must not enter suspicion.
	clock.Advance(decodeErrorDecay + time.Second)
	if s.observeDecodeError(hevcFatalErrorLine()) {
		t.Fatal("decayed error counted toward the threshold")
	}
	if s.IsDecodeFailed() {
		t.Fatal("suspected after a decayed error")
	}
	if got := decodeStageOf(s); got != decodeStageObserving {
		t.Fatalf("stage after decayed error = %d, want observing", got)
	}
}

// TestDecodeFailureDoesNotStampSoftwareOrCopy proves a decoder error on a
// copy target is an output/remux anomaly, not evidence that hardware decode
// should be replaced, so it never enters the lifecycle. A software plan still
// confirms the source verdict (there is no further decoder) — covered by
// TestSoftwareDecodeConfirmsWithoutHardwareTransition.
func TestDecodeFailureDoesNotStampSoftwareOrCopy(t *testing.T) {
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "copy"}}
	clock := attachDecodeClock(s, time.Unix(4000, 0))
	for i := 0; i < decodeErrorThreshold*3; i++ {
		if s.observeDecodeError(hevcFatalErrorLine()) {
			t.Fatal("copy session observed a decoder failure")
		}
	}
	clock.Advance(decodeObservationWindow * 2)
	s.evaluateDecodeVerdict()
	if s.IsDecodeFailed() || s.IsSourceRejected() {
		t.Fatal("copy session recorded a decode verdict")
	}
}

// TestDecodeFailureRecordsSourceRejectionForEveryDecodeMode proves the source
// verdict is independent of the decode-mode retry: a software plan confirms the
// rejection (so the serve path can report it as permanent) without claiming the
// hardware decoder failed, and a copy target records neither.
func TestDecodeFailureRecordsSourceRejectionForEveryDecodeMode(t *testing.T) {
	software := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "h264", SoftwareVideoDecode: true}}
	softwareClock := attachDecodeClock(software, time.Unix(5000, 0))
	stormDecode(software)
	softwareClock.Advance(decodeObservationWindow)
	software.evaluateDecodeVerdict()
	if !software.IsSourceRejected() {
		t.Fatal("software plan did not record the source rejection")
	}
	if software.IsDecodeFailed() {
		t.Fatal("software plan claimed the hardware decoder failed")
	}

	hardware := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "h264"}}
	hardwareClock := attachDecodeClock(hardware, time.Unix(5100, 0))
	stormDecode(hardware)
	hardwareClock.Advance(decodeObservationWindow)
	hardware.evaluateDecodeVerdict()
	if !hardware.IsSourceRejected() || !hardware.IsDecodeFailed() {
		t.Fatalf("hardware plan rejected=%v decodeFailed=%v, want both true", hardware.IsSourceRejected(), hardware.IsDecodeFailed())
	}

	copyTarget := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "copy"}}
	copyClock := attachDecodeClock(copyTarget, time.Unix(6000, 0))
	for i := 0; i < decodeErrorThreshold*3; i++ {
		copyTarget.observeDecodeError(hevcFatalErrorLine())
	}
	copyClock.Advance(decodeObservationWindow * 2)
	copyTarget.evaluateDecodeVerdict()
	if copyTarget.IsSourceRejected() {
		t.Fatal("copy target recorded a source decode rejection")
	}
}

// TestDecodeFailureInvokesSourceRejectedMarkerOnce proves a decoder rejection
// is handed to the source-candidate failure callback exactly once, after the
// observation window and with the same identity the demux marker uses, so the
// candidate is stamped through the one existing failed_at mechanism. The
// callback is the mark path: it fires for a hardware plan and for a software
// plan (the source is bad, not the decoder mode), never for a copy target, and
// never before the window expires.
func TestDecodeFailureInvokesSourceRejectedMarkerOnce(t *testing.T) {
	type call struct {
		fileID    int
		canonical string
	}
	newSession := func(opts TranscodeOpts, calls chan call) *TranscodeSession {
		opts.OnSourceRejected = func(_ context.Context, fileID int, canonical string) error {
			calls <- call{fileID: fileID, canonical: canonical}
			return nil
		}
		return &TranscodeSession{opts: opts}
	}
	ctx := context.Background()

	hardwareCalls := make(chan call, 4)
	hardware := newSession(TranscodeOpts{
		MediaFileID:        77,
		CanonicalInputPath: "virtual://movie/tt1?result=bad",
		TargetCodecVideo:   "h264",
	}, hardwareCalls)
	hardwareClock := attachDecodeClock(hardware, time.Unix(21_000, 0))
	for i := 0; i < decodeErrorThreshold-1; i++ {
		hardware.logFFmpegLine(ctx, hevcFatalErrorLine())
	}
	select {
	case got := <-hardwareCalls:
		t.Fatalf("marker invoked before the threshold: %+v", got)
	default:
	}
	// Crossing the threshold enters suspicion; the marker still waits.
	hardware.logFFmpegLine(ctx, hevcFatalErrorLine())
	select {
	case got := <-hardwareCalls:
		t.Fatalf("marker invoked at the threshold, before the observation window: %+v", got)
	default:
	}
	hardwareClock.Advance(decodeObservationWindow)
	hardware.evaluateDecodeVerdict()
	select {
	case got := <-hardwareCalls:
		if got.fileID != 77 || got.canonical != "virtual://movie/tt1?result=bad" {
			t.Fatalf("marker call = %+v, want file 77 and the canonical virtual path", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source-rejected marker was not invoked after the observation window")
	}
	// The notified flag is set under the mutex before dispatch, so a later
	// error must not spawn a second marker call.
	hardware.logFFmpegLine(ctx, hevcFatalErrorLine())
	select {
	case extra := <-hardwareCalls:
		t.Fatalf("source-rejected marker invoked more than once: %+v", extra)
	default:
	}

	// A software plan records the source rejection too: the bitstream is bad,
	// not merely the hardware decoder.
	softwareCalls := make(chan call, 1)
	software := newSession(TranscodeOpts{
		MediaFileID:         78,
		CanonicalInputPath:  "virtual://movie/tt2?result=sw",
		TargetCodecVideo:    "h264",
		SoftwareVideoDecode: true,
	}, softwareCalls)
	softwareClock := attachDecodeClock(software, time.Unix(22_000, 0))
	stormDecode(software)
	softwareClock.Advance(decodeObservationWindow)
	software.evaluateDecodeVerdict()
	select {
	case got := <-softwareCalls:
		if got.fileID != 78 || got.canonical != "virtual://movie/tt2?result=sw" {
			t.Fatalf("software marker call = %+v, want file 78 and the canonical path", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("software source rejection did not invoke the marker")
	}
	if software.IsDecodeFailed() {
		t.Fatal("software plan claimed the hardware decoder failed")
	}

	// A copy target never reports a decode verdict, so it never marks.
	copyCalls := make(chan struct{}, 1)
	copyTarget := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo: "copy",
		OnSourceRejected: func(context.Context, int, string) error {
			copyCalls <- struct{}{}
			return nil
		},
	}}
	for i := 0; i < decodeErrorThreshold*3; i++ {
		copyTarget.logFFmpegLine(ctx, hevcFatalErrorLine())
	}
	select {
	case <-copyCalls:
		t.Fatal("copy target invoked the source-rejected marker")
	default:
	}
}

// TestLogFFmpegLineObservesDecodeFailure exercises the stderr plumbing the
// transcode process uses, including the diagnostic sample kept for the replan
// decision log.
func TestLogFFmpegLineObservesDecodeFailure(t *testing.T) {
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "h264"}}
	clock := attachDecodeClock(s, time.Unix(23_000, 0))
	ctx := context.Background()
	for i := 0; i < decodeErrorThreshold; i++ {
		s.logFFmpegLine(ctx, hevcFatalErrorLine())
	}
	if got := decodeStageOf(s); got != decodeStageSuspected {
		t.Fatalf("logFFmpegLine stage = %d, want suspected", got)
	}
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	if !s.IsDecodeFailed() {
		t.Fatal("logFFmpegLine did not confirm the decoder failure")
	}
	sample, count := s.DecodeFailureEvidence()
	if sample != hevcFatalErrorLine() || count != decodeErrorThreshold {
		t.Fatalf("evidence = (%q, %d), want (%q, %d)", sample, count, hevcFatalErrorLine(), decodeErrorThreshold)
	}
}

// TestDecodeFailureRequiresVideoStreamIdentity proves the source-rejection
// verdict is scoped to the video stream: a subtitle decoder refusing a corrupt
// subtitle packet, and a decoder line with no stream identity at all, must not
// reject the video source. A video codec/NAL failure still does.
func TestDecodeFailureRequiresVideoStreamIdentity(t *testing.T) {
	stamped := func(line string) (rejected, marked bool) {
		markedCh := make(chan struct{}, 1)
		s := &TranscodeSession{opts: TranscodeOpts{
			MediaFileID:        11,
			CanonicalInputPath: "virtual://movie/tt-id?result=x",
			TargetCodecVideo:   "h264",
			OnSourceRejected: func(context.Context, int, string) error {
				markedCh <- struct{}{}
				return nil
			},
		}}
		clock := attachDecodeClock(s, time.Unix(24_000, 0))
		ctx := context.Background()
		for i := 0; i < decodeErrorThreshold; i++ {
			s.logFFmpegLine(ctx, line)
		}
		clock.Advance(decodeObservationWindow)
		s.evaluateDecodeVerdict()
		rejected = s.IsSourceRejected()
		select {
		case <-markedCh:
			marked = true
		case <-time.After(time.Second):
		}
		return rejected, marked
	}

	ambiguous := `Error submitting packet to decoder: Invalid data found when processing input`
	if rejected, marked := stamped(ambiguous); rejected || marked {
		t.Fatal("a decoder failure with no stream identity rejected the video source")
	}
	subtitle := `[sist#0:2/subrip @ 0x55d0] [dec:subrip @ 0x55d1] Error submitting packet to decoder: Invalid data found when processing input`
	if rejected, marked := stamped(subtitle); rejected || marked {
		t.Fatal("a subtitle decoder failure rejected the video source")
	}
	videoNAL := `[hevc @ 0x55d0] Invalid NAL unit size (1215484279 > 206).`
	if rejected, marked := stamped(videoNAL); !rejected || !marked {
		t.Fatal("a video NAL failure no longer rejected the video source")
	}
}

// TestVideoStreamEvidenceRejectsAudioStreams pins the lexical rule directly: an
// audio decoder tag or explicit audio-stream wording never counts as video
// evidence, even when the line also carries a video-shaped word, while a
// video-tagged or video-codec line does.
func TestVideoStreamEvidenceRejectsAudioStreams(t *testing.T) {
	for _, line := range []string{
		`[aac @ 0x55d0] Error submitting packet to decoder`,
		`[ac3 @ 0x55d0] error decoding audio bitstream`,
		`[eac3 @ 0x55d0] error decoding audio bitstream`,
		`[truehd @ 0x55d0] error decoding audio bitstream`,
		`[dts @ 0x55d0] error decoding audio bitstream`,
		`[flac @ 0x55d0] error decoding audio bitstream`,
		`[opus @ 0x55d0] error decoding audio bitstream`,
		`[aist#0:1/aac @ 0x55d0] error decoding audio bitstream`,
		`Audio stream 0:1 failed to decode bitstream`,
	} {
		if videoStreamEvidenceV3(line) {
			t.Fatalf("audio stream line %q was treated as video evidence", line)
		}
	}
	if !videoStreamEvidenceV3(`[vist#0:0/h264 @ 0x55d0] Error during demuxing`) {
		t.Fatal("a vist# line must remain video evidence")
	}
	if !videoStreamEvidenceV3(`[hevc @ 0x55d0] Invalid NAL unit size (1215484279 > 206)`) {
		t.Fatal("a video codec line must remain video evidence")
	}
}

// TestDecodeFailureAudioStreamIdentityNeverStamps proves an audio decoder error
// can never stamp the video candidate or reject the source, even when the line
// carries a word ("bitstream") that also appears in video failures. A genuine
// video failure on the same line shape still indicts.
func TestDecodeFailureAudioStreamIdentityNeverStamps(t *testing.T) {
	stamped := func(line string) (rejected, marked bool) {
		markedCh := make(chan struct{}, 1)
		s := &TranscodeSession{opts: TranscodeOpts{
			MediaFileID:      11,
			TargetCodecVideo: "h264",
			OnSourceRejected: func(context.Context, int, string) error {
				markedCh <- struct{}{}
				return nil
			},
		}}
		clock := attachDecodeClock(s, time.Unix(25_000, 0))
		ctx := context.Background()
		for i := 0; i < decodeErrorThreshold; i++ {
			s.logFFmpegLine(ctx, line)
		}
		clock.Advance(decodeObservationWindow)
		s.evaluateDecodeVerdict()
		rejected = s.IsSourceRejected()
		select {
		case <-markedCh:
			marked = true
		case <-time.After(time.Second):
		}
		return rejected, marked
	}

	for _, line := range []string{
		`[aac @ 0x55d0] Error submitting packet to decoder: Invalid data found when processing input`,
		`[ac3 @ 0x55d0] Error submitting packet to decoder: Invalid data found when processing input`,
		`[eac3 @ 0x55d0] Error submitting packet to decoder: Invalid data found when processing input`,
		`[truehd @ 0x55d0] Error submitting packet to decoder: Invalid data found when processing input`,
		`[dca @ 0x55d0] Error submitting packet to decoder: Invalid data found when processing input`,
		`[flac @ 0x55d0] Failed to decode audio bitstream`,
		`[opus @ 0x55d0] Error splitting the input into NAL units`, // audio tag wins over the video-shaped phrase
		`[aist#0:1/aac @ 0x55d0] Error submitting packet to decoder: Invalid data found when processing input`,
		`Audio stream 0:1 Error submitting packet to decoder: Invalid data found when processing input`,
	} {
		if rejected, marked := stamped(line); rejected || marked {
			t.Fatalf("an audio decoder failure %q rejected the video source", line)
		}
	}
	// A video decoder failure is unchanged even though it mentions the same
	// "bitstream" word an audio failure can carry.
	videoBitstream := `[hevc @ 0x55d0] Invalid NAL unit size (1215484279 > 206) while parsing bitstream.`
	if rejected, marked := stamped(videoBitstream); !rejected || !marked {
		t.Fatal("a video bitstream failure no longer rejected the video source")
	}
}

// prodSPSMissingLine and prodPPSRangeLine are the exact stderr shapes from the
// production incident where a 4K HEVC provider stream carried no parameter
// sets: ffmpeg emitted them for every frame with zero segments produced, while
// the pre-split matcher counted nothing and the start decayed into a generic
// startup timeout instead of a candidate rotation.
func prodSPSMissingLine() string {
	return `[hevc @ 0x560d3bfc3280] SPS 0 does not exist.`
}

func prodPPSRangeLine() string {
	return `[hevc @ 0x560d3bfc82c0] PPS id out of range: 0`
}

// TestDecodeErrorLineMatchesParameterSetLoss proves the parameter-set-missing
// family counts as a decoder rejecting the source, while reference-list
// warnings and encoder/output chatter still do not.
func TestDecodeErrorLineMatchesParameterSetLoss(t *testing.T) {
	positive := []string{
		prodSPSMissingLine(),
		prodPPSRangeLine(),
		`[hevc @ 0x55d0] non-existing PPS 0 referenced`,
		`[h265 @ 0x55d0] SPS 1 does not exist.`,
	}
	for _, line := range positive {
		if !decodeErrorLine(line) {
			t.Errorf("decodeErrorLine(%q) = false, want true", line)
		}
		if !videoStreamEvidenceV3(line) {
			t.Errorf("videoStreamEvidenceV3(%q) = false, want true: the indictment path requires video identity", line)
		}
	}
	negative := []string{
		`[hevc @ 0x55d0] Could not find ref with POC 16`,
		`[hevc @ 0x55d0] Error constructing the frame RPS`,
		`[hevc_qsv @ 0x55d0] Error writing trailer: Broken pipe`,
		`[hevc @ 0x55d0] Reinit context to 1920x1080, pix_fmt yuv420p`,
	}
	for _, line := range negative {
		if decodeErrorLine(line) {
			t.Errorf("decodeErrorLine(%q) = true, want false", line)
		}
	}
}

// TestDecodeFailureStampsParameterSetLossStorm replays the production
// incident shape: a storm of SPS/PPS-missing lines with no output stamps the
// source rejection and invokes the candidate marker exactly once, so the serve
// path rotates instead of stalling into a startup timeout.
func TestDecodeFailureStampsParameterSetLossStorm(t *testing.T) {
	markedCh := make(chan struct{}, 1)
	s := &TranscodeSession{opts: TranscodeOpts{
		MediaFileID:        1433,
		CanonicalInputPath: "virtual://series/tt3006802/8/5?result=59959f2d428d742fb6d18d0c",
		TargetCodecVideo:   "hevc",
		OnSourceRejected: func(context.Context, int, string) error {
			markedCh <- struct{}{}
			return nil
		},
	}}
	clock := attachDecodeClock(s, time.Unix(26_000, 0))
	ctx := context.Background()
	lines := []string{prodSPSMissingLine(), prodPPSRangeLine()}
	for i := 0; i < decodeErrorThreshold; i++ {
		s.logFFmpegLine(ctx, lines[i%len(lines)])
	}
	if got := decodeStageOf(s); got != decodeStageSuspected {
		t.Fatalf("storm stage = %d, want suspected before the observation window", got)
	}
	clock.Advance(decodeObservationWindow)
	s.evaluateDecodeVerdict()
	if !s.IsSourceRejected() {
		t.Fatal("parameter-set-loss storm did not record the source rejection")
	}
	select {
	case <-markedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("parameter-set-loss storm did not invoke the source-rejected marker")
	}
	sample, count := s.DecodeFailureEvidence()
	if count != decodeErrorThreshold {
		t.Fatalf("evidence count = %d, want %d", count, decodeErrorThreshold)
	}
	if sample == "" {
		t.Fatal("evidence sample is empty")
	}
}

// TestSourceRejectionSuppressedAfterVideoProgress proves the verdict is about
// an undecodable source, not a noisy one: a decoder storm on a generation that
// muxed segments after suspicion neither confirms nor marks. A manifest alone,
// an empty segment, or segments older than the generation (a previous
// generation's leftovers) prove nothing and let the suspicion expire.
func TestSourceRejectionSuppressedAfterVideoProgress(t *testing.T) {
	writeFile := func(t *testing.T, dir, name string, size int, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		if !mtime.IsZero() {
			if err := os.Chtimes(filepath.Join(dir, name), mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
	}
	// stormInPast records a storm on the fake clock and returns the clock.
	stormInPast := func(t *testing.T, s *TranscodeSession, base time.Time, dir string) *decodeTestClock {
		t.Helper()
		clock := attachDecodeClock(s, base.Add(time.Second))
		s.mu.Lock()
		s.outputDir = dir
		s.generationStartedAt = base
		s.mu.Unlock()
		stormDecode(s)
		return clock
	}

	t.Run("recovery after the storm cancels", func(t *testing.T) {
		dir := t.TempDir()
		s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
		base := time.Unix(7000, 0)
		clock := stormInPast(t, s, base, dir)
		// A segment muxed after suspicion proves the decoder recovered. The
		// file mtime is real time, so anchor suspicion in the past too, and
		// report decoded frames: the frame counter is the half of the evidence
		// a muxed segment cannot supply.
		writeFile(t, dir, "seg_00000.ts", 188, base.Add(time.Minute))
		s.mu.Lock()
		s.videoFrames = 50
		s.mu.Unlock()
		clock.Advance(decodeObservationWindow)
		s.evaluateDecodeVerdict()
		if s.IsSourceRejected() {
			t.Fatal("video produced after the storm confirmed the verdict")
		}
	})

	t.Run("audio-only segments do not cancel", func(t *testing.T) {
		// The production shape this guards: an encode muxes a non-empty segment
		// for every audio packet while its decoder produces no picture at all.
		// Segment existence must not clear the suspicion the stderr evidence
		// raised, or the session would certify a stream no client can display.
		dir := t.TempDir()
		s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
		base := time.Unix(7050, 0)
		clock := stormInPast(t, s, base, dir)
		writeFile(t, dir, "seg_00000.ts", 188, base.Add(time.Minute))
		writeFile(t, dir, "seg_00001.ts", 188, base.Add(time.Minute))
		clock.Advance(decodeObservationWindow)
		s.evaluateDecodeVerdict()
		if !s.IsSourceRejected() {
			t.Fatal("segments muxed with zero decoded frames canceled the verdict")
		}
	})

	t.Run("manifest alone does not cancel", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "stream.m3u8", 64, time.Time{})
		s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
		base := time.Unix(7100, 0)
		clock := stormInPast(t, s, base, dir)
		clock.Advance(decodeObservationWindow)
		s.evaluateDecodeVerdict()
		if !s.IsSourceRejected() {
			t.Fatal("manifest without segments canceled the verdict")
		}
	})

	t.Run("empty segment does not cancel", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "seg_00000.ts", 0, time.Time{})
		s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
		base := time.Unix(7200, 0)
		clock := stormInPast(t, s, base, dir)
		clock.Advance(decodeObservationWindow)
		s.evaluateDecodeVerdict()
		if !s.IsSourceRejected() {
			t.Fatal("empty segment canceled the verdict")
		}
	})

	t.Run("previous generation leftovers do not cancel", func(t *testing.T) {
		dir := t.TempDir()
		base := time.Unix(7300, 0)
		writeFile(t, dir, "seg_00000.ts", 188, base.Add(-time.Hour))
		s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "hevc"}}
		clock := stormInPast(t, s, base, dir)
		clock.Advance(decodeObservationWindow)
		s.evaluateDecodeVerdict()
		if !s.IsSourceRejected() {
			t.Fatal("stale segments canceled the fresh generation's verdict")
		}
	})
}

// rejectedGenerationFixture builds a live-looking session with a stamped
// generation and an empty output directory: waiters block, and a decoder
// storm records the verdict under test control. No ffmpeg process runs, so
// revocation only latches verdict state (cancel is nil-safe).
func rejectedGenerationFixture(t *testing.T) *TranscodeSession {
	t.Helper()
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "virtual://movie/tt-test?result=x",
	}}
	base := time.Unix(30_000, 0)
	attachDecodeClock(s, base)
	s.mu.Lock()
	s.outputDir = t.TempDir()
	s.running = true
	s.generationStartedAt = base
	s.mu.Unlock()
	return s
}

// confirmRejectedGeneration drives the fixture through the observation window
// to a confirmed verdict on the real (uninjected) clock, without waiting a
// second: it anchors suspicion one window in the past and evaluates.
func confirmRejectedGeneration(t *testing.T, s *TranscodeSession) {
	t.Helper()
	for i := 0; i < decodeErrorThreshold; i++ {
		s.logFFmpegLine(context.Background(), prodSPSMissingLine())
	}
	// Anchor suspicion one observation window in the past on the session's
	// clock, then drive the evaluator. The evaluator's watcher goroutine may
	// own the in-flight probe, so wait on the observable verdict rather than
	// assuming this call applied it.
	s.mu.Lock()
	if s.decodeStage != decodeStageSuspected {
		stage := s.decodeStage
		s.mu.Unlock()
		t.Fatalf("fixture stage = %d, want suspected", stage)
	}
	if s.decodeNow != nil {
		s.decodeSuspectAt = s.decodeClock().Add(-decodeObservationWindow)
	} else {
		s.decodeSuspectAt = time.Now().Add(-decodeObservationWindow)
	}
	s.mu.Unlock()
	s.evaluateDecodeVerdict()
	waitSourceRejected(t, s)
}

// waitSourceRejected waits on the observable confirmed verdict.
func waitSourceRejected(t *testing.T, s *TranscodeSession) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if s.IsSourceRejected() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session never reached a confirmed verdict")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestRejectedGenerationWakesSegmentWaits proves a rejected generation
// terminates every waiter flavor on the typed verdict instead of the
// deadline: segment, opened-segment, and manifest waits must all return
// ErrSourceDecodeRejected promptly once the storm crosses the threshold.
// Waiters observe the verdict through their tick-bounded gated checks.
func TestRejectedGenerationWakesSegmentWaits(t *testing.T) {
	s := rejectedGenerationFixture(t)
	ctx := context.Background()

	type outcome struct {
		name string
		err  error
	}
	results := make(chan outcome, 3)
	go func() {
		_, err := s.WaitForSegment("seg_00009.ts", 30*time.Second)
		results <- outcome{"segment", err}
	}()
	go func() {
		_, err := s.WaitForOpenSegment("seg_00009.ts", 30*time.Second)
		results <- outcome{"open-segment", err}
	}()
	go func() {
		_, err := s.waitForManifest(ctx, 30*time.Second, true)
		results <- outcome{"manifest", err}
	}()
	// Let the waiters block before the rejection confirms.
	time.Sleep(200 * time.Millisecond)
	confirmRejectedGeneration(t, s)

	for i := 0; i < 3; i++ {
		select {
		case got := <-results:
			if !errors.Is(got.err, ErrSourceDecodeRejected) {
				t.Fatalf("%s wait err = %v, want ErrSourceDecodeRejected", got.name, got.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("revoked generation did not wake a waiter promptly")
		}
	}
}

// TestSimultaneousWaitsReceiveSameVerdict proves one rejection fans out to
// every waiter: concurrent segment requests on the same dead generation all
// observe the identical typed verdict.
func TestSimultaneousWaitsReceiveSameVerdict(t *testing.T) {
	s := rejectedGenerationFixture(t)

	const waiters = 5
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			_, err := s.WaitForSegment("seg_00007.ts", 30*time.Second)
			errs <- err
		}()
	}
	time.Sleep(200 * time.Millisecond)
	confirmRejectedGeneration(t, s)

	for i := 0; i < waiters; i++ {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrSourceDecodeRejected) {
				t.Fatalf("waiter %d err = %v, want ErrSourceDecodeRejected", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("waiter %d was not woken by the revocation", i)
		}
	}
}

// TestManifestWaitIgnoresRevocationForLocalInput proves the startup manifest
// wait only short-circuits on the verdict for virtual inputs. A local file
// commits and recovers through the replan software path, which requires the
// committed session the verdict must not preempt — so the wait runs to its
// deadline (here shrunk) instead of returning the typed verdict, even though
// IsSourceRejected is true.
func TestManifestWaitIgnoresRevocationForLocalInput(t *testing.T) {
	s := &TranscodeSession{opts: TranscodeOpts{
		TargetCodecVideo:   "hevc",
		CanonicalInputPath: "/media/movies/local.mkv",
	}}
	s.mu.Lock()
	s.outputDir = t.TempDir()
	s.running = true
	s.generationStartedAt = time.Now()
	s.mu.Unlock()

	ctx := context.Background()
	confirmRejectedGeneration(t, s)
	start := time.Now()
	_, err := s.waitForManifest(ctx, 300*time.Millisecond, true)
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("local manifest wait returned after %v, want the full deadline", elapsed)
	}
	if err == nil || errors.Is(err, ErrSourceDecodeRejected) {
		t.Fatalf("local manifest wait err = %v, want the deadline error, never the typed verdict", err)
	}
}

// TestRestartRefusedAfterRejection proves a revoked generation cannot be
// rebuilt: restarting the same undecodable bytes is refused with the typed
// verdict so the caller rotates instead. A healthy session keeps its restart.
func TestRestartRefusedAfterRejection(t *testing.T) {
	ctx := context.Background()
	rejected := rejectedGenerationFixture(t)
	confirmRejectedGeneration(t, rejected)
	if err := rejected.Restart(ctx, 0, 0); !errors.Is(err, ErrSourceDecodeRejected) {
		t.Fatalf("Restart err = %v, want ErrSourceDecodeRejected", err)
	}
	if _, _, err := rejected.RestartSegment(ctx, 9); !errors.Is(err, ErrSourceDecodeRejected) {
		t.Fatalf("RestartSegment err = %v, want ErrSourceDecodeRejected", err)
	}

	healthy := rejectedGenerationFixture(t)
	if healthy.IsSourceRejected() {
		t.Fatal("fresh generation reports a verdict it never earned")
	}
}

// TestNotificationDoesNotMisrouteRecoveryButConfirmationReaps pins D's
// narrower rule: a mere suspicion (and even a storm at the threshold) must not
// synchronously fire the marker or kill the process, so a recovering generation
// is never misrouted; confirmation, however, eventually reaps it. The marker
// latch is checked under the mutex.
func TestNotificationDoesNotMisrouteRecoveryButConfirmationReaps(t *testing.T) {
	s := rejectedGenerationFixture(t)
	killed := make(chan struct{})
	s.mu.Lock()
	_, innerCancel := context.WithCancel(context.Background())
	s.cancel = func() {
		innerCancel()
		close(killed)
	}
	s.mu.Unlock()

	// At the threshold the generation is only suspected: no marker, no kill.
	stormDecode(s)
	if s.IsSourceRejected() {
		t.Fatal("suspicion was reported as a confirmation")
	}
	s.mu.Lock()
	notified := s.sourceRejectNotified
	s.mu.Unlock()
	if notified {
		t.Fatal("marker latch set before confirmation")
	}
	select {
	case <-killed:
		t.Fatal("suspicion killed the process; reaping is confirmation-only")
	case <-time.After(200 * time.Millisecond):
	}

	// Confirmation reaps and latches the marker.
	s.mu.Lock()
	s.decodeSuspectAt = s.decodeClock().Add(-decodeObservationWindow)
	s.mu.Unlock()
	s.evaluateDecodeVerdict()
	waitSourceRejected(t, s)
	s.mu.Lock()
	notified = s.sourceRejectNotified
	s.mu.Unlock()
	if !notified {
		t.Fatal("marker latch not set after confirmation")
	}
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("confirmed rejection did not eventually reap the process")
	}
}

// TestRevocationScopedToGeneration proves a dead generation's verdict cannot
// indict a replacement: after a rejection plus a generation reset, waiters on
// the fresh generation block on the deadline instead of inheriting the old
// verdict.
func TestRevocationScopedToGeneration(t *testing.T) {
	s := rejectedGenerationFixture(t)
	confirmRejectedGeneration(t, s)

	s.mu.Lock()
	s.generationStartedAt = time.Now()
	s.resetDecodeVerdictLocked()
	s.running = true
	s.mu.Unlock()
	if s.IsSourceRejected() {
		t.Fatal("fresh generation inherits the dead generation's verdict")
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.WaitForSegment("seg_00009.ts", 400*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if errors.Is(err, ErrSourceDecodeRejected) {
			t.Fatal("fresh generation waiter inherited the stale verdict")
		}
		if !errors.Is(err, ErrSegmentNotFound) {
			t.Fatalf("fresh waiter err = %v, want deadline ErrSegmentNotFound", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fresh generation waiter did not terminate")
	}
}

// TestDecodeWindowResetsOnNewGeneration proves a restart never inherits its
// predecessor's strikes: nine errors, a generation reset, and one more error
// must not enter suspicion, while ten fresh errors after the reset must. The
// generation counter itself must advance so stale observations are fenced.
func TestDecodeWindowResetsOnNewGeneration(t *testing.T) {
	s := &TranscodeSession{opts: TranscodeOpts{TargetCodecVideo: "h264"}}
	base := time.Unix(9000, 0)
	clock := attachDecodeClock(s, base)
	for i := 0; i < decodeErrorThreshold-1; i++ {
		if s.observeDecodeError(hevcFatalErrorLine()) {
			t.Fatalf("suspected after only %d errors", i+1)
		}
	}
	s.mu.Lock()
	before := s.decodeGeneration
	s.generationStartedAt = clock.Now()
	s.resetDecodeVerdictLocked()
	after := s.decodeGeneration
	s.mu.Unlock()
	if after <= before {
		t.Fatalf("reset did not advance the verdict generation: %d -> %d", before, after)
	}
	if s.observeDecodeError(hevcFatalErrorLine()) {
		t.Fatal("fresh generation suspected on the dead generation's count")
	}
	s.mu.Lock()
	count := s.decodeErrorCount
	stage := s.decodeStage
	s.mu.Unlock()
	if count != 1 || stage != decodeStageObserving {
		t.Fatalf("window did not reopen at the fresh error: count=%d stage=%d", count, stage)
	}
	for i := 1; i < decodeErrorThreshold; i++ {
		s.observeDecodeError(hevcFatalErrorLine())
	}
	awaitDecodeStage(t, s, decodeStageSuspected)
	if s.IsSourceRejected() {
		t.Fatal("fresh generation was confirmed at the threshold, before the observation window")
	}
}
