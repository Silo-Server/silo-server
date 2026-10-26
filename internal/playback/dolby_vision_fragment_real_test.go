package playback

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

func TestDV7FragmentConversionRealTool(t *testing.T) {
	dir := os.Getenv("SILO_DV7_QA_FIXTURE_DIR")
	tool := os.Getenv("SILO_DV7_QA_TOOL")
	if dir == "" || tool == "" {
		t.Skip("optional dovi_tool and interleaved HEVC fixture are not configured")
	}
	initData, err := os.ReadFile(filepath.Join(dir, "init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := mp4.DecodeFile(bytes.NewReader(initData))
	if err != nil {
		t.Fatal(err)
	}
	// The public two-track test vector was combined into one Annex B stream.
	// Restore its known source record, which raw HEVC cannot carry.
	entry := source.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.HvcX
	entry.AddChild(mp4.CreateDoViConfigurationBox(1, 0, 7, 6, true, true, true, 6))
	var init bytes.Buffer
	if err := source.Init.Encode(&init); err != nil {
		t.Fatal(err)
	}
	fragment, err := os.ReadFile(filepath.Join(dir, "seg_00000.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	outInit, outFragment, err := ConvertDV7Fragment(t.Context(), tool, init.Bytes(), fragment)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "converted.mp4")
	if err := os.WriteFile(output, append(outInit, outFragment...), 0600); err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is unavailable")
	}
	result, err := exec.CommandContext(t.Context(), ffprobe, "-v", "error", "-show_streams", "-of", "json", output).CombinedOutput()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !bytes.Contains(result, []byte(`"dv_profile": 8`)) || !bytes.Contains(result, []byte(`"dv_bl_signal_compatibility_id": 1`)) || !bytes.Contains(result, []byte(`"el_present_flag": 0`)) {
		t.Fatalf("converted stream does not report DV8.1: %s", result)
	}
	if destination := os.Getenv("SILO_DV7_QA_OUTPUT_DIR"); destination != "" {
		if err := os.MkdirAll(destination, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"source-p7.mp4": append(init.Bytes(), fragment...), "converted.mp4": append(outInit, outFragment...), "init.mp4": outInit, "seg_00000.m4s": outFragment} {
			if err := os.WriteFile(filepath.Join(destination, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestDV7HLSRealToolServesConvertedInitialization(t *testing.T) {
	source := os.Getenv("SILO_DV7_QA_SOURCE")
	tool := os.Getenv("SILO_DV7_QA_TOOL")
	if source == "" || tool == "" {
		t.Skip("optional DV7 stream fixture and tool are not configured")
	}
	t.Setenv("PATH", filepath.Dir(tool)+string(os.PathListSeparator)+os.Getenv("PATH"))
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	opts := TranscodeOpts{InputPath: source, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, SourceVideoCodec: "hevc", TargetCodecVideo: "copy", TargetCodecAudio: "copy", VideoSampleEntry: VideoSampleEntryDVH1, SeparateAudioHLS: true, SegmentDuration: 2, TotalDuration: 8}
	if err := json.Unmarshal([]byte(`{"dv7_to_dv81":true}`), &opts); err != nil {
		t.Fatal(err)
	}
	session, err := StartTranscode(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.WaitForGenerationManifestContext(ctx, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	path, err := session.GetSegment("init.mp4")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	init, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	config := init.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.HvcX.DoViConfig
	if config == nil || config.DVProfile != 8 || config.ELPresentFlag || config.DVBLSignalCompatibilityID != 1 {
		t.Fatalf("served Dolby Vision configuration = %+v", config)
	}
	t.Run("restart", func(t *testing.T) {
		if err := session.Restart(ctx, 4, 2); err != nil {
			t.Fatal(err)
		}
		if _, err := session.WaitForGenerationManifestContext(ctx, time.Second); err != nil {
			t.Fatalf("converted restart did not become ready: %v", err)
		}
		if _, err := session.GetSegment("init.mp4"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDV7ToolchainProbeRealTool(t *testing.T) {
	tool := os.Getenv("SILO_DV7_QA_TOOL")
	if tool == "" {
		t.Skip("optional dovi_tool is not configured")
	}
	t.Setenv("PATH", filepath.Dir(tool)+string(os.PathListSeparator)+os.Getenv("PATH"))
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg is unavailable")
	}
	registry, err := ProbeTransformationRegistryWithPlaybackPolicyV3Result(t.Context(), ffmpeg, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.Available(TransformationServerDV7DV81V3) {
		t.Fatal("working DV7 converter is not advertised")
	}
}
