package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/prometheus/client_golang/prometheus"
)

func TestDV7FragmentConversionPreservesSampleTimingAndBaseLayer(t *testing.T) {
	initData, fragmentData := dvFragmentTestMedia(t)
	tool := filepath.Join(t.TempDir(), "dovi_tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf '\\000\\000\\000\\001\\174\\001\\201' > \"$8\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	base := []byte{0x26, 1, 0x11, 0x22}
	convertedInit, convertedFragment, err := ConvertDV7Fragment(t.Context(), tool, initData, fragmentData)
	if err != nil {
		t.Fatal(err)
	}
	decodedInit, err := mp4.DecodeFile(bytes.NewReader(convertedInit))
	if err != nil {
		t.Fatal(err)
	}
	config := decodedInit.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.HvcX.DoViConfig
	if config == nil || config.DVProfile != 8 || config.DVBLSignalCompatibilityID != 1 || config.ELPresentFlag || !config.RPUPresentFlag {
		t.Fatalf("converted configuration = %+v", config)
	}
	decoded, err := mp4.DecodeFile(bytes.NewReader(convertedFragment))
	if err != nil {
		t.Fatal(err)
	}
	samples, err := decoded.Segments[0].Fragments[0].GetFullSamples(decodedInit.Init.Moov.Mvex.Trex)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("sample count = %d", len(samples))
	}
	sample := samples[0]
	if sample.DecodeTime != 900000 || sample.CompositionTimeOffset != -1001 || sample.Dur != 3003 || sample.Flags != 0x02000000 {
		t.Fatalf("sample timing changed: %+v", sample.Sample)
	}
	if !bytes.Equal(sample.Data, dvFragmentTestSample(base, []byte{0x7c, 1, 0x81})) {
		t.Fatalf("base layer or RPU replacement is wrong: %x", sample.Data)
	}
}

func dvFragmentTestSample(nalus ...[]byte) []byte {
	var data bytes.Buffer
	for _, nalu := range nalus {
		if err := binary.Write(&data, binary.BigEndian, uint32(len(nalu))); err != nil {
			panic(err)
		}
		data.Write(nalu)
	}
	return data.Bytes()
}

func TestDV7HLSRecipeSurvivesReconstruction(t *testing.T) {
	opts := TranscodeOpts{SessionID: "dv7-fixture", TargetCodecVideo: "copy", SeparateAudioHLS: true, DV7ToDV81: true}
	card := NewRecipeCard(1, "profile", 42, "", opts)
	restored := RecipeCardFromClaims(new(card.ToClaims())).TranscodeOpts(t.TempDir(), "ffmpeg", nil)
	if !restored.DV7ToDV81 {
		t.Fatal("reconstructed transport lost DV conversion")
	}
	if card.CopyFMP4RecipeVersion != "4" {
		t.Fatal("older separate-audio worker can accept the new byte recipe")
	}
}

func TestDV7HLSRejectsAnUnvalidatedToolBeforeOpeningMedia(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dovi_tool"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	opts := TranscodeOpts{OutputDir: t.TempDir(), FFmpegPath: "/unavailable/ffmpeg", TargetCodecVideo: "copy", SourceVideoCodec: "hevc", SeparateAudioHLS: true, DV7ToDV81: true, VideoSampleEntry: VideoSampleEntryDVH1}
	_, err := StartTranscode(t.Context(), opts)
	if err == nil || !strings.Contains(err.Error(), "conversion tool") {
		t.Fatalf("invalid converter reached media execution: %v", err)
	}
}

func TestDV7FragmentConversionHonorsCancellationBeforeReadingMedia(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := ConvertDV7Fragment(ctx, "unavailable", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled conversion = %v", err)
	}
}

func TestLengthPrefixedHEVCNALsRejectsMalformedLengths(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "truncated prefix", data: []byte{0, 0, 0}},
		{name: "missing header", data: []byte{0, 0, 0, 1, 0x26}},
		{name: "truncated payload", data: []byte{0, 0, 0, 3, 0x26, 1}},
		{name: "maximum unsigned length", data: []byte{255, 255, 255, 255, 0x26, 1}},
		{name: "signed integer overflow", data: []byte{128, 0, 0, 0, 0x26, 1}},
		{name: "trailing partial prefix", data: []byte{0, 0, 0, 2, 0x26, 1, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := lengthPrefixedHEVCNALs(test.data); err == nil {
				t.Fatal("malformed HEVC NAL length was accepted")
			}
		})
	}
	valid := []byte{0, 0, 0, 2, 0x26, 1, 0, 0, 0, 3, 0x7c, 1, 7}
	nalus, err := lengthPrefixedHEVCNALs(valid)
	if err != nil || len(nalus) != 2 || !bytes.Equal(nalus[0], []byte{0x26, 1}) || !bytes.Equal(nalus[1], []byte{0x7c, 1, 7}) {
		t.Fatalf("valid bounded NALs = %x, %v", nalus, err)
	}
}

func dvFragmentTestMedia(t *testing.T) ([]byte, []byte) {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(90000, "video", "und")
	entry := mp4.CreateVisualSampleEntryBox("dvh1", 1920, 1080, &mp4.HvcCBox{DecConfRec: hevc.DecConfRec{ConfigurationVersion: 1, LengthSizeMinusOne: 3}})
	entry.AddChild(mp4.CreateDoViConfigurationBox(1, 0, 7, 6, true, true, true, 6))
	init.Moov.Trak.Mdia.Minf.Stbl.Stsd.AddChild(entry)
	var initData bytes.Buffer
	if err := init.Encode(&initData); err != nil {
		t.Fatal(err)
	}
	base := []byte{0x26, 1, 0x11, 0x22}
	oldRPU := []byte{0x7c, 1, 7}
	el := []byte{0x7e, 1, 0x33}
	data := dvFragmentTestSample(base, oldRPU, el)
	original := mp4.FullSample{Sample: mp4.NewSample(0x02000000, 3003, uint32(len(data)), -1001), DecodeTime: 900000, Data: data}
	frag, err := mp4.CreateFragment(7, 1)
	if err != nil {
		t.Fatal(err)
	}
	frag.AddFullSample(original)
	var fragmentData bytes.Buffer
	if err := frag.Encode(&fragmentData); err != nil {
		t.Fatal(err)
	}
	return initData.Bytes(), fragmentData.Bytes()
}

func TestDV7FragmentConversionLimitsConcurrentWorkAndCancelsWaiters(t *testing.T) {
	initData, fragmentData := dvFragmentTestMedia(t)
	dir := t.TempDir()
	tool := filepath.Join(dir, "converter")
	script := "#!/bin/sh\nmktemp '" + dir + "/started.XXXXXX' >/dev/null\nwhile [ ! -f '" + dir + "/release' ]; do sleep 0.01; done\nprintf '\\000\\000\\000\\001\\174\\001\\201' > \"$8\"\n"
	if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for range 2 {
		go func() { _, _, err := ConvertDV7Fragment(ctx, tool, initData, fragmentData); done <- err }()
	}
	for {
		files, _ := filepath.Glob(filepath.Join(dir, "started.*"))
		if len(files) == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("two conversions did not start")
		case <-time.After(time.Millisecond):
		}
	}
	waiterCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	_, _, err := ConvertDV7Fragment(waiterCtx, tool, initData, fragmentData)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting conversion = %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "started.*"))
	if len(files) != 2 {
		t.Errorf("started %d converters while two were active", len(files))
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := ConvertDV7Fragment(ctx, tool, initData, fragmentData); err != nil {
		t.Fatalf("conversion after release: %v", err)
	}
}

func dvConversionExits(t *testing.T, outcome string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "silo_subprocess_exits_total" {
			continue
		}
		for _, metric := range family.Metric {
			workload, result := "", ""
			for _, label := range metric.Label {
				switch label.GetName() {
				case "workload":
					workload = label.GetValue()
				case "outcome":
					result = label.GetValue()
				}
			}
			if workload == "transcode" && result == outcome {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestDV7FragmentConversionAccountsForProcessesAndReleasesFailedWork(t *testing.T) {
	initData, fragmentData := dvFragmentTestMedia(t)
	for _, outcome := range []string{"start_error", "error", "canceled", "success"} {
		t.Run(outcome, func(t *testing.T) {
			tool := filepath.Join(t.TempDir(), "converter")
			script := "#!/bin/sh\nexit 1\n"
			if outcome == "canceled" {
				script = "#!/bin/sh\nexec sleep 10\n"
			}
			if outcome == "success" {
				script = "#!/bin/sh\nprintf '\\000\\000\\000\\001\\174\\001\\201' > \"$8\"\n"
			}
			if outcome != "start_error" {
				if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := dvConversionExits(t, outcome)
			// More requests than the shared limit expose slots leaked on any exit path.
			for range 3 {
				timeout := time.Second
				if outcome == "canceled" {
					timeout = 50 * time.Millisecond
				}
				ctx, cancel := context.WithTimeout(t.Context(), timeout)
				_, _, err := ConvertDV7Fragment(ctx, tool, initData, fragmentData)
				cancel()
				if outcome == "success" && err != nil {
					t.Fatal(err)
				}
				if outcome != "success" && err == nil {
					t.Fatal("failed converter succeeded")
				}
				if outcome == "canceled" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("canceled converter = %v", err)
				}
			}
			if got := dvConversionExits(t, outcome) - before; got != 3 {
				t.Fatalf("%s process completions = %v, want 3", outcome, got)
			}
		})
	}
}

func TestLiveDVConversionHonorsTranscodingToggle(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, dv7SourceDirectory)
	if err := os.MkdirAll(raw, 0700); err != nil {
		t.Fatal(err)
	}
	init, fragment := dvFragmentTestMedia(t)
	for name, data := range map[string][]byte{hlsInitFilename: init, "seg_00000.m4s": fragment, "seg_00001.m4s": fragment} {
		if err := os.WriteFile(filepath.Join(raw, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(dir, "conversions")
	t.Setenv("SILO_DV_LIVE_MARKER", marker)
	tool := filepath.Join(dir, "converter")
	script := "#!/bin/sh\necho run >> \"$SILO_DV_LIVE_MARKER\"\nprintf '\\000\\000\\000\\001\\174\\001\\201' > \"$8\"\n"
	if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	enabled := true
	session := &TranscodeSession{outputDir: dir, dvContext: t.Context(), dvToolPath: tool, segmentGeneration: 1, opts: TranscodeOpts{DV7ToDV81: true, SeparateAudioHLS: true, TargetCodecVideo: "copy", SourceVideoCodec: "hevc", VideoSampleEntry: VideoSampleEntryDVH1, TranscodePolicy: &TranscodePolicy{Enabled: func() bool { return enabled }}}}
	if _, err := session.GetSegment("seg_00000.m4s"); err != nil {
		t.Fatal(err)
	}
	enabled = false
	if _, err := session.GetSegment("seg_00000.m4s"); err != nil {
		t.Fatalf("cached segment: %v", err)
	}
	if _, err := session.GetSegment("seg_00001.m4s"); err == nil || !strings.Contains(err.Error(), "transcoding is disabled") {
		t.Fatalf("disabled conversion: %v", err)
	}
	if err := session.Restart(t.Context(), 0, 0); err == nil || !strings.Contains(err.Error(), "transcoding is disabled") {
		t.Fatalf("disabled restart: %v", err)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "run\n" {
		t.Fatalf("disabled work launched converter: %q, %v", calls, err)
	}
	enabled = true
	if _, err := session.GetSegment("seg_00001.m4s"); err != nil {
		t.Fatalf("re-enabled conversion: %v", err)
	}
	calls, err = os.ReadFile(marker)
	if err != nil || string(calls) != "run\nrun\n" {
		t.Fatalf("re-enabled converter calls: %q, %v", calls, err)
	}
}

func TestDV7SessionWaitsForCapacityBeforeReadingSource(t *testing.T) {
	for range cap(dvConversionSlots) {
		dvConversionSlots <- struct{}{}
	}
	defer func() {
		for range cap(dvConversionSlots) {
			<-dvConversionSlots
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	// Missing source files must not be inspected while another session owns capacity.
	session := &TranscodeSession{outputDir: t.TempDir(), dvContext: ctx, segmentGeneration: 1, opts: TranscodeOpts{DV7ToDV81: true}}
	if _, err := session.GetSegment("seg_00000.m4s"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued conversion = %v, want caller deadline", err)
	}
}
