package playback

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func remuxVideoBitstreamFilterArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-bsf:v" {
			return args[i+1]
		}
	}
	return ""
}

func TestBuildRemuxArgsDropsLeadingPicturesOnlyForSeekedVideo(t *testing.T) {
	for _, test := range []struct {
		name      string
		seek      float64
		audioOnly bool
		drop      bool
		dvProfile int
		want      string
	}{
		{name: "seeked copy", seek: 1_234.5, drop: true, want: ResumeLeadingPictureDropBitstreamFilter},
		{name: "start at zero has no leading pictures", seek: 0, drop: true, want: ""},
		{name: "not requested", seek: 1_234.5, drop: false, want: ""},
		{name: "audio only", seek: 1_234.5, drop: true, audioOnly: true, want: ""},
		{name: "chained ahead of the DV7 strip", seek: 1_234.5, drop: true, dvProfile: 7, want: ResumeLeadingPictureDropBitstreamFilter + "," + DV7ToHDR10BitstreamFilter},
		{name: "DV7 strip alone at zero", seek: 0, drop: true, dvProfile: 7, want: DV7ToHDR10BitstreamFilter},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := buildRemuxArgsWithLeadingPictureDropV3("/x.mkv", "mp4", test.seek, false, -1, test.dvProfile, false, test.audioOnly, 0, 0, 0, test.drop, "", nil)
			if got := remuxVideoBitstreamFilterArg(args); got != test.want {
				t.Fatalf("-bsf:v = %q, want %q (args=%s)", got, test.want, strings.Join(args, " "))
			}
		})
	}
}

func TestBuildRemuxArgsTakesPrimedVideoFromConcatList(t *testing.T) {
	primer := &remuxIDRPrimer{firstPTS: 0, anchorPTS: 86.67, anchorSeconds: 86.67}
	for _, test := range []struct {
		name   string
		primer *remuxIDRPrimer
		inputs []string
		maps   []string
		drop   string
	}{
		{name: "plain seeked copy", inputs: []string{"/x.mkv"}, maps: []string{"0:V:0", "0:a:0?"}, drop: ResumeLeadingPictureDropBitstreamFilter},
		{name: "primed copy", primer: primer, inputs: []string{"pipe:0", "/x.mkv"}, maps: []string{"0:V:0", "1:a:0?"}, drop: primedResumeLeadingPictureDropBitstreamFilter},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := buildRemuxArgsWithLeadingPictureDropV3("/x.mkv", "mp4", 88.213, true, -1, 0, false, false, 0, 0, 0, true, "", test.primer)
			var inputs, maps []string
			for i := 0; i+1 < len(args); i++ {
				switch args[i] {
				case "-i":
					inputs = append(inputs, args[i+1])
				case "-map":
					maps = append(maps, args[i+1])
				}
			}
			if strings.Join(inputs, " ") != strings.Join(test.inputs, " ") || strings.Join(maps, " ") != strings.Join(test.maps, " ") {
				t.Fatalf("inputs %v maps %v, want %v %v (args=%s)", inputs, maps, test.inputs, test.maps, strings.Join(args, " "))
			}
			if got := remuxVideoBitstreamFilterArg(args); got != test.drop {
				t.Fatalf("-bsf:v = %q, want %q", got, test.drop)
			}
			joined := strings.Join(args, " ")
			if test.primer != nil && !strings.Contains(joined, "-f concat -safe 0 -protocol_whitelist file,pipe -i pipe:0") {
				t.Fatalf("concat input missing: %s", joined)
			}
			// The seeked input starts at the request, 1.543 s past the keyframe the concat clock starts on.
			if hasOffset := strings.Contains(joined, "-itsoffset 1.543000 -i /x.mkv"); hasOffset != (test.primer != nil) {
				t.Fatalf("-itsoffset present = %v, want %v: %s", hasOffset, test.primer != nil, joined)
			}
		})
	}
}

// hevcSliceNALTypes lists the NAL unit types of the slices in an Annex B
// HEVC stream, in order.
func hevcSliceNALTypes(annexB []byte) []int {
	var types []int
	for rest := annexB; ; {
		i := bytes.Index(rest, []byte{0, 0, 1})
		if i < 0 || i+3 >= len(rest) {
			return types
		}
		if nalType := int(rest[i+3]>>1) & 0x3f; nalType < 32 {
			types = append(types, nalType)
		}
		rest = rest[i+3:]
	}
}

// primerTestSources are the files TestPrimedRemuxStartsOnTheSourceIDRThenTheSeekKeyframe
// remuxes: x265 encodes with B-pyramid reordering, and copies of the open-GOP
// one in containers without Matroska Cues.
type primerTestSources struct {
	openGOP, closedGOP, earlyKeyframe, transportStream, mp4 string
}

func encodePrimerTestSources(t *testing.T, run func(*testing.T, ...string) []byte, dir string) primerTestSources {
	t.Helper()
	encode := func(name, video, x265Params string) string {
		path := filepath.Join(dir, name)
		run(t, "-v", "error", "-f", "lavfi", "-i", video,
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "10",
			"-c:v", "libx265", "-preset", "ultrafast",
			"-x265-params", "log-level=error:keyint=48:bframes=8:b-pyramid=1:b-adapt=0:rc-lookahead=20:"+x265Params,
			"-c:a", "ac3", path)
		return path
	}
	remux := func(source, name string) string {
		path := filepath.Join(dir, name)
		run(t, "-v", "error", "-i", source, "-map", "0", "-c", "copy", path)
		return path
	}
	const testsrc = "testsrc2=size=128x72:rate=24"
	src := primerTestSources{
		openGOP:   encode("open-gop.mkv", testsrc, "min-keyint=48:scenecut=0:open-gop=1"),
		closedGOP: encode("closed-gop.mkv", testsrc, "min-keyint=48:scenecut=0:open-gop=0"),
		// Two black frames, then a scene cut that x265 makes a CRA decoded
		// right after the IDR, inside the primer's outpoint.
		earlyKeyframe: encode("early-keyframe.mkv",
			"color=black:size=128x72:rate=24:duration=0.08[b];"+testsrc+":duration=10[t];[b][t]concat=n=2:v=1:a=0",
			"min-keyint=1:scenecut=40:open-gop=1"),
	}
	src.transportStream = remux(src.openGOP, "open-gop.ts")
	src.mp4 = remux(src.openGOP, "open-gop.mp4")
	return src
}

// macOS Firefox shows nothing from a stream that starts on a CRA. A primed
// resume of an open-GOP encode must start on the source's IDR, go straight to
// the seek's CRA and decode cleanly; B-pyramid reordering is what lets extra
// frames, a keyframe among them, past the concat outpoint. The primer starts
// on the plan's origin keyframe, read from the Matroska Cues. A seek into the
// first GOP, a closed-GOP encode, a URL without the origin, and sources
// without Cues (MP4, MPEG-TS) get the plain copy.
func TestPrimedRemuxStartsOnTheSourceIDRThenTheSeekKeyframe(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	run := func(t *testing.T, args ...string) []byte {
		t.Helper()
		output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, output)
		}
		return output
	}
	if !strings.Contains(string(run(t, "-hide_banner", "-encoders")), "libx265") {
		t.Skip("libx265 is unavailable")
	}
	if !supportsLeadingPictureDropFilter(ffmpeg) {
		t.Skip("ffmpeg's noise filter has no drop expression")
	}
	dir := t.TempDir()
	src := encodePrimerTestSources(t, run, dir)
	openGOP, earlyKeyframe := src.openGOP, src.earlyKeyframe

	const idrNLP, cra = 20, 21
	for _, test := range []struct {
		name     string
		source   string
		seek     float64
		noOrigin bool
		want     []int // the first two slices' NAL unit types
	}{
		{name: "past the first GOP", source: openGOP, seek: 5, want: []int{idrNLP, cra}},
		// Matroska answers an exact keyframe seek with the keyframe before it (#839).
		{name: "requested on a keyframe", source: openGOP, seek: 6, want: []int{idrNLP, cra}},
		{name: "inside the first GOP", source: openGOP, seek: 1, want: []int{idrNLP, 1}},
		{name: "a keyframe right after the first", source: earlyKeyframe, seek: 5, want: []int{idrNLP, cra}},
		{name: "without the plan's origin", source: openGOP, seek: 5, noOrigin: true, want: []int{cra, 1}},
		{name: "MP4", source: src.mp4, seek: 5, want: []int{cra, 1}},
		{name: "closed GOP", source: src.closedGOP, seek: 5, want: []int{idrNLP, 1}},
		{name: "MPEG-TS", source: src.transportStream, seek: 5, want: []int{cra, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.source == earlyKeyframe {
				packets := string(run(t, "-v", "error", "-i", test.source, "-map", "0:V:0", "-c", "copy", "-frames:v", "3", "-f", "framecrc", "-"))
				if keyframes := strings.Count(packets, "\n0,") - strings.Count(packets, "F=0x"); keyframes < 2 {
					t.Skipf("this x265 put no keyframe right after the first:\n%s", packets)
				}
			}
			// The plan resolves the origin its timeline starts at.
			origin, _, err := ResolveCopySeekAnchor(ctx, ffmpeg, test.source, test.seek, 0)
			if err != nil {
				t.Fatal(err)
			}
			streamOrigin := &origin
			if test.noOrigin {
				streamOrigin = nil
			}
			session, err := startRemuxWithOptions(ctx, test.source, "mp4", test.seek, true, -1, 0, RemuxDVRejectP7V3, ffmpeg, false, 2, 2, 128, true, "", streamOrigin)
			if err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, strings.ReplaceAll(test.name, " ", "-")+".mp4")
			media, err := io.ReadAll(session)
			_ = session.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(output, media, 0o600); err != nil {
				t.Fatal(err)
			}
			annexB := run(t, "-v", "error", "-i", output, "-map", "0:v", "-c", "copy", "-frames:v", "2", "-f", "hevc", "-")
			slices := hevcSliceNALTypes(annexB)
			if len(slices) < 2 || slices[0] != test.want[0] || slices[1] != test.want[1] {
				t.Fatalf("first slice NAL types = %v, want %v", slices, test.want)
			}
			if decode := run(t, "-v", "error", "-i", output, "-f", "null", "-"); bytes.Contains(decode, []byte("[hevc @")) {
				t.Fatalf("decoding the primed stream failed:\n%s", decode)
			}
			if test.want[1] != cra {
				return
			}
			// The picture after the primer is the keyframe the plan's timeline starts at.
			got := run(t, "-v", "error", "-i", output, "-map", "0:v", "-vf", `select=eq(n\,1)`, "-frames:v", "1", "-f", "md5", "-")
			want := run(t, "-v", "error", "-ss", strconv.FormatFloat(origin, 'f', 6, 64), "-i", test.source, "-map", "0:V:0", "-frames:v", "1", "-f", "md5", "-")
			if !bytes.Equal(got, want) {
				t.Fatalf("frame after the primer = %s, want the source frame at the %gs origin = %s", got, origin, want)
			}
		})
	}
}

// The primer lookup reads the source, so stalled storage must never hold a
// remux start for longer than its budget, nor queue starts behind lookups
// that are already waiting.
func TestRemuxIDRPrimerLookupNeverHoldsTheStart(t *testing.T) {
	release := make(chan struct{})
	var lookups atomic.Int32
	original := lookupRemuxIDRPrimer
	lookupRemuxIDRPrimer = func(string, float64) *remuxIDRPrimer {
		lookups.Add(1)
		<-release
		return &remuxIDRPrimer{}
	}
	t.Cleanup(func() {
		lookupRemuxIDRPrimer = original
		close(release)
		// Taking every slot waits for the stalled lookup to give its back.
		for range maxRemuxIDRPrimerLookups {
			remuxIDRPrimerLookups <- struct{}{}
		}
		for range maxRemuxIDRPrimerLookups {
			<-remuxIDRPrimerLookups
		}
	})

	// The budget is 300 ms; the bound leaves room for a loaded host, and still
	// fails a start that waits on the stalled lookup itself.
	const maxWait = 5 * time.Second
	begin := time.Now()
	if primer := resolveRemuxIDRPrimer(t.Context(), "/x.mkv", 12); primer != nil {
		t.Fatal("a stalled lookup returned a primer")
	}
	if waited := time.Since(begin); waited > maxWait {
		t.Fatalf("a stalled lookup held the start for %v", waited)
	}

	// The stalled lookup still holds its slot; take the others too.
	for range maxRemuxIDRPrimerLookups - 1 {
		remuxIDRPrimerLookups <- struct{}{}
	}
	primer := resolveRemuxIDRPrimer(t.Context(), "/x.mkv", 12)
	for range maxRemuxIDRPrimerLookups - 1 {
		<-remuxIDRPrimerLookups
	}
	if primer != nil || lookups.Load() != 1 {
		t.Fatalf("with every lookup slot taken, a start got primer %v and %d lookups ran; want none and one", primer, lookups.Load())
	}
}

func TestPlanPlaybackV3RequestsLeadingPictureDropOnlyForMacOSFirefoxHEVCProgressive(t *testing.T) {
	const macFirefox = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0"
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: "1", Available: true, ValidatedClaims: []string{ClaimAudioDecodeV3}},
	})
	plan := func(userAgent, videoCodec string, start float64) PlannerResultV3 {
		file := detailedFixtureFileV3()
		file.VideoTracks[0].Codec = videoCodec
		file.CodecVideo = videoCodec
		file.VideoTracks[0].Profile = "Main"
		file.VideoTracks[0].BitDepth = 8
		file.VideoTracks[0].VideoRange = "SDR"
		file.VideoTracks[0].VideoRangeType = "SDR"
		request := validStartRequestV3()
		request.ClientPlaybackContext.FormFactor = "desktop"
		request.ClientPlaybackContext.Device = DeviceContextV3{Platform: "web", PlatformDetails: map[string]string{"user_agent": userAgent}}
		request.Capabilities.VideoEvidence = EvidenceDeclaredV3
		request.Capabilities.AudioEvidence = EvidenceDeclaredV3
		request.Capabilities.Containers = []string{"mp4"}
		request.ClientPlaybackContext.Deliveries = map[string]DeliveryCapabilityV3{
			DeliveryClassProgressiveV3: {Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"}, VideoCodecs: []string{videoCodec}, AudioDecodeCodecs: []string{"aac"}},
		}
		request.StartPosition = floatPointerV3(start)
		return PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: registry})
	}

	for _, start := range []float64{0, 1_234.5} {
		result := plan(macFirefox, "hevc", start)
		if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
			t.Fatalf("start %v: %s", start, ExplainPlannerResultV3(result))
		}
		// The flag is frozen for the whole session: a later seek in the same
		// remux needs it even when playback started at zero.
		if !result.RemuxResumeLeadingPictureDrop {
			t.Fatalf("start %v: leading-picture drop not requested for macOS Firefox HEVC", start)
		}
		// Best effort means the plan never names it as a required server
		// transformation, so it cannot narrow the executor pool.
		for _, transformation := range result.Plan.Transformations {
			if strings.Contains(transformation.Name, "leading") {
				t.Fatalf("start %v: leading-picture drop leaked into plan transformations: %#v", start, result.Plan.Transformations)
			}
		}
	}

	for _, test := range []struct {
		name, userAgent, codec string
	}{
		{name: "Windows Firefox", userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:153.0) Gecko/20100101 Firefox/153.0", codec: "hevc"},
		{name: "macOS Safari", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15", codec: "hevc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := plan(test.userAgent, test.codec, 1_234.5)
			if test.codec == "hevc" && (result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3) {
				t.Fatalf("%s", ExplainPlannerResultV3(result))
			}
			if result.RemuxResumeLeadingPictureDrop {
				t.Fatalf("leading-picture drop requested outside macOS Firefox HEVC: %s", ExplainPlannerResultV3(result))
			}
		})
	}
}

func TestLeadingPictureDropScopeIsHEVCOnly(t *testing.T) {
	request := validStartRequestV3()
	request.ClientPlaybackContext.Device = DeviceContextV3{Platform: "web", PlatformDetails: map[string]string{
		"user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0",
	}}
	for codec, want := range map[string]bool{"hevc": true, "h265": true, "h264": false, "av1": false} {
		if got := firefoxMacOSHEVCResumeLeadingPictureDropV3(SourceDescriptorV3{VideoCodec: codec}, request); got != want {
			t.Errorf("codec %s: drop = %v, want %v", codec, got, want)
		}
	}
}

// Pin the exact argv token: Quick104 validated this string against real
// open-GOP HEVC media, including the escaped comma when it is chained with
// DV7ToHDR10BitstreamFilter.
func TestResumeLeadingPictureDropFilterString(t *testing.T) {
	if ResumeLeadingPictureDropBitstreamFilter != `noise=drop=lt(pts\,startpts)*not(key)` {
		t.Fatalf("filter = %q", ResumeLeadingPictureDropBitstreamFilter)
	}
}

func TestRemuxLeadingPictureDropSurvivesTokenAndRecipeRoundTrips(t *testing.T) {
	card := NewRemuxRecipeCard("session-1", 42, "profile-1", 77, false, 0)
	card.RemuxResumeLeadingPictureDrop = true
	claims := card.ToClaims()
	if !claims.RemuxResumeLeadingPictureDrop {
		t.Fatal("recipe card did not carry the leading-picture drop into stream claims")
	}
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"rlpd":true`) {
		t.Fatalf("claims JSON = %s, want rlpd", encoded)
	}
	if restored := RecipeCardFromClaims(&claims); !restored.RemuxResumeLeadingPictureDrop {
		t.Fatal("stream claims did not restore the leading-picture drop")
	}

	plan := &PlanV3{PlanID: "plan:remux", Delivery: DeliveryRemuxProgressiveV3}
	recipe := FreezeExecutableRecipeV3(PlannerResultV3{Plan: plan, PlayMethod: PlayRemux, RemuxResumeLeadingPictureDrop: true})
	if !recipe.Valid() {
		t.Fatalf("frozen remux recipe is invalid: %#v", recipe)
	}
	if !recipe.PlannerResult(plan).RemuxResumeLeadingPictureDrop {
		t.Fatal("a seek reanchor from the frozen recipe lost the leading-picture drop")
	}
}
