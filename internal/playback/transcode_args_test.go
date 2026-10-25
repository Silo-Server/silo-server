package playback

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestActivityOutputContainerMatchesFFmpeg(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts TranscodeOpts
		want string
	}{
		{"hevc-copy", TranscodeOpts{TargetCodecVideo: "copy", SourceVideoCodec: "hevc"}, "fmp4"},
		{"forced-ts", TranscodeOpts{TargetCodecVideo: "copy", SourceVideoCodec: "hevc", CopyVideoMPEGTS: true}, "mpegts"},
		{"mpeg2-copy", TranscodeOpts{TargetCodecVideo: "copy", SourceVideoCodec: "mpeg2video"}, "mpegts"},
		{"video-encode", TranscodeOpts{TargetCodecVideo: "h264", SourceVideoCodec: "hevc"}, "mpegts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HLSOutputContainer(tc.opts); got != tc.want {
				t.Fatalf("output container = %q, want %q", got, tc.want)
			}
			args := strings.Join(buildFFmpegArgs(tc.opts), " ")
			if !strings.Contains(args, "-hls_segment_type "+tc.want) {
				t.Fatal("activity format does not match the FFmpeg muxer")
			}
			// Segments are served by extension. Hosts with shared-mime-info
			// would otherwise label .ts as Qt Linguist text.
			wantMIME := map[string]string{OutputContainerFMP4: "video/mp4", OutputContainerMPEGTS: mimeVideoMPEGTS}[tc.want]
			if got := mime.TypeByExtension(hlsSegmentExtension(tc.opts)); got != wantMIME {
				t.Fatalf("segment content type = %q, want %q", got, wantMIME)
			}
		})
	}
}

// TestToneMapFFmpegGraphsCoverSupportedExecutors verifies each executor emits its required graph.
func TestToneMapFFmpegGraphsCoverSupportedExecutors(t *testing.T) {
	tests := []struct {
		name       string
		mode       tonemap.Mode
		hwAccel    string
		filter     string
		sourceKind tonemap.SourceKind
		want       []string
	}{
		{name: "software PQ", mode: tonemap.ModeSoftware, hwAccel: "none", filter: "tonemapx", sourceKind: tonemap.SourcePQ, want: []string{"tonemapx=tonemap=bt2390", "color_trc=smpte2084", "libx264"}},
		{name: "software HLG fallback", mode: tonemap.ModeSoftware, hwAccel: "none", filter: "tonemap", sourceKind: tonemap.SourceHLG, want: []string{"tonemap=hable", "color_trc=arib-std-b67", "libx264"}},
		{name: "QSV", mode: tonemap.ModeHardware, hwAccel: "qsv", filter: "tonemap_opencl", sourceKind: tonemap.SourcePQ, want: []string{"-init_hw_device opencl=ocl@va", "tonemap_opencl", "hwmap=derive_device=qsv:mode=read+write", "h264_qsv"}},
		{name: "VAAPI", mode: tonemap.ModeHardware, hwAccel: "vaapi", filter: "tonemap_vaapi", sourceKind: tonemap.SourceHLG, want: []string{"tonemap_vaapi", "scale_vaapi", "h264_vaapi"}},
		{name: "NVENC", mode: tonemap.ModeHardware, hwAccel: "nvenc", filter: "tonemap_cuda", sourceKind: tonemap.SourcePQ, want: []string{"color_trc=smpte2084", "tonemap_cuda", "scale_cuda", "h264_nvenc"}},
		{name: "VideoToolbox", mode: tonemap.ModeHardware, hwAccel: "videotoolbox", filter: "scale_vt", sourceKind: tonemap.SourcePQ, want: []string{"-hwaccel videotoolbox", "-hwaccel_output_format videotoolbox_vld", "scale_vt=w=-2:h=1080", "hwdownload,format=p010le,format=nv12", "h264_videotoolbox"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildFFmpegArgs(TranscodeOpts{
				InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
				FFmpegPath:       videoToolboxTestFFmpegFor(t, tt.hwAccel),
				SourceVideoCodec: "hevc", SourceVideoProfile: "Main 10", SourceVideoBitDepth: 10,
				TargetResolution: "1080p", HWAccel: tt.hwAccel, ToneMapPolicy: tonemap.PolicyHardwareThenSoftware,
				ToneMapMode: tt.mode, ToneMapSourceKind: tt.sourceKind, ToneMapFilter: tt.filter,
				ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			})
			joined := strings.Join(args, " ")
			for _, token := range append(tt.want, "-color_range tv", "-color_primaries bt709", "-color_trc bt709") {
				if !strings.Contains(joined, token) {
					t.Fatalf("args missing %q: %s", token, joined)
				}
			}
			if tt.mode == tonemap.ModeSoftware && !strings.Contains(joined, "-colorspace bt709") {
				t.Fatalf("software args omit explicit output matrix: %s", joined)
			}
			if tt.mode == tonemap.ModeHardware && tt.hwAccel != transcodeHWVideoToolbox && strings.Contains(joined, "-colorspace bt709") {
				t.Fatalf("hardware args request an incompatible software colorspace conversion: %s", joined)
			}
			if tt.hwAccel == transcodeHWVideoToolbox && !strings.Contains(joined, "-colorspace bt709") {
				t.Fatalf("VideoToolbox args omit the required output matrix: %s", joined)
			}
			if tt.mode == tonemap.ModeHardware && strings.Contains(joined, "-pix_fmt yuv420p") {
				t.Fatalf("hardware graph requested a software pixel format conversion: %s", joined)
			}
		})
	}
}

func TestBuildFFmpegArgs_VideoToolboxToneMapUnprobedSourceUsesSoftwareDecodeUpload(t *testing.T) {
	for _, tt := range []struct {
		name     string
		codec    string
		profile  string
		bitDepth int
	}{
		{name: "AV1", codec: "av1", profile: "Main", bitDepth: 10},
		{name: "HEVC Main 12", codec: "hevc", profile: "Main 12", bitDepth: 12},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := buildFFmpegArgs(TranscodeOpts{
				InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
				FFmpegPath: videoToolboxTestFFmpeg(t), SourceVideoCodec: tt.codec, SourceVideoProfile: tt.profile, SourceVideoBitDepth: tt.bitDepth,
				TargetResolution: "1080p", HWAccel: transcodeHWVideoToolbox, ToneMapPolicy: tonemap.PolicyHardwareThenSoftware,
				ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: tonemap.HardwareFilterVideoToolbox,
				ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			})
			joined := strings.Join(args, " ")
			for _, forbidden := range []string{"-hwaccel videotoolbox", "-hwaccel_output_format videotoolbox_vld"} {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("unprobed source shape must not use VideoToolbox decoding, found %q: %s", forbidden, joined)
				}
			}
			for _, required := range []string{
				"-init_hw_device videotoolbox=vt", "-filter_hw_device vt",
				"setparams=range=tv:color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,format=p010le,hwupload",
				"scale_vt=w=-2:h=1080", "-c:v h264_videotoolbox",
			} {
				if !strings.Contains(joined, required) {
					t.Fatalf("software-decode VideoToolbox tone map missing %q: %s", required, joined)
				}
			}
		})
	}
}

func TestResolveVideoToolboxToneMapDecodeUsesOnlyProbedSourceShape(t *testing.T) {
	tests := []struct {
		name     string
		codec    string
		profile  string
		bitDepth int
		wantCPU  bool
	}{
		{name: "HEVC Main 10", codec: "hevc", profile: "Main 10", bitDepth: 10},
		{name: "HEVC Main 12", codec: "hevc", profile: "Main 12", bitDepth: 12, wantCPU: true},
		{name: "HEVC range extensions", codec: "hevc", profile: "Rext", bitDepth: 10, wantCPU: true},
		{name: "HEVC unknown bit depth", codec: "hevc", profile: "Main 10", wantCPU: true},
		{name: "HEVC mismatched bit depth", codec: "hevc", profile: "Main 10", bitDepth: 12, wantCPU: true},
		{name: "AV1", codec: "av1", profile: "Main", bitDepth: 10, wantCPU: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := resolveVideoToolboxToneMapDecode(TranscodeOpts{
				HWAccel: transcodeHWVideoToolbox, ToneMapMode: tonemap.ModeHardware,
				SourceVideoCodec: tt.codec, SourceVideoProfile: tt.profile, SourceVideoBitDepth: tt.bitDepth,
			})
			if opts.SoftwareVideoDecode != tt.wantCPU {
				t.Fatalf("SoftwareVideoDecode = %v, want %v", opts.SoftwareVideoDecode, tt.wantCPU)
			}
		})
	}
}

// TestHardwareToneMapRemovesMetadataAfterHardwareFormatConversion verifies output metadata is ordered safely.
func TestHardwareToneMapRemovesMetadataAfterHardwareFormatConversion(t *testing.T) {
	tests := []struct {
		name    string
		hwAccel string
		filter  string
		before  string
	}{
		{name: "QSV", hwAccel: "qsv", filter: tonemap.HardwareFilterOpenCL, before: "hwmap=derive_device=qsv"},
		{name: "VAAPI", hwAccel: "vaapi", filter: tonemap.HardwareFilterVAAPI, before: "scale_vaapi"},
		{name: "VAAPI OpenCL", hwAccel: "vaapi", filter: tonemap.HardwareFilterOpenCL, before: "reverse=1"},
		{name: "NVENC", hwAccel: "nvenc", filter: tonemap.HardwareFilterCUDA, before: "scale_cuda"},
		{name: "VideoToolbox", hwAccel: "videotoolbox", filter: tonemap.HardwareFilterVideoToolbox, before: "scale_vt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := strings.Join(buildFFmpegArgs(TranscodeOpts{
				InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
				FFmpegPath:       videoToolboxTestFFmpegFor(t, tt.hwAccel),
				TargetResolution: "1080p", HWAccel: tt.hwAccel, ToneMapPolicy: tonemap.PolicyHardwareOnly,
				ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: tt.filter,
				ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			}), " ")
			conversion := strings.Index(graph, tt.before)
			metadata := strings.Index(graph, "sidedata=mode=delete")
			if conversion < 0 || metadata <= conversion {
				t.Fatalf("metadata removal precedes hardware format conversion: %s", graph)
			}
		})
	}
}

// TestQSVToneMapScalesBeforeOpenCL keeps every VAAPI resize ahead of the
// OpenCL tone mapper on the subtitle-free graph, for hardware and software
// decode. Only the passthrough interop may follow the tone mapper; a
// scale_vaapi that reads the tone-mapped surface corrupts frames.
func TestQSVToneMapScalesBeforeOpenCL(t *testing.T) {
	for _, kind := range []tonemap.SourceKind{tonemap.SourcePQ, tonemap.SourceHLG, tonemap.SourceDolbyVision} {
		for _, softwareDecode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s software decode %t", kind, softwareDecode), func(t *testing.T) {
				graph := strings.Join(buildFFmpegArgs(TranscodeOpts{
					InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
					TargetResolution: "1080p", HWAccel: transcodeHWQSV, SoftwareVideoDecode: softwareDecode, SourceVideoBitDepth: 10,
					ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: kind,
					ToneMapFilter: tonemap.HardwareFilterOpenCL, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
				}), " ")
				resize := strings.Index(graph, "scale_vaapi=w=-2:h=1080:format=p010")
				toneMap := strings.Index(graph, tonemap.HardwareFilterOpenCL)
				if resize < 0 || toneMap < resize {
					t.Fatalf("QSV graph does not resize before tone mapping: %s", graph)
				}
				tail := "reverse=1:extra_hw_frames=16,format=vaapi," + tonemap.QSVInteropFilter() + ",sidedata="
				if !strings.Contains(graph[toneMap:], tail) || strings.Contains(graph[toneMap:], "scale_vaapi=w=") {
					t.Fatalf("QSV graph processes the tone-mapped surface before the encoder: %s", graph)
				}
			})
		}
	}
}

// TestQSVToneMapBitmapSubtitlesCompositeOnOpenCL keeps bitmap burn-in for HDR
// sources on the OpenCL device after tone mapping: overlay_vaapi or a
// scale_vaapi reading the tone-mapped surface corrupts frames. SDR bases never
// reach OpenCL and keep overlay_vaapi.
func TestQSVToneMapBitmapSubtitlesCompositeOnOpenCL(t *testing.T) {
	graphFor := func(kind tonemap.SourceKind, resolution string, softwareDecode bool) string {
		args := buildFFmpegArgs(TranscodeOpts{
			InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
			TargetResolution: resolution, HWAccel: transcodeHWQSV, SoftwareVideoDecode: softwareDecode, SourceVideoBitDepth: 10,
			ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: kind,
			ToneMapFilter: tonemap.HardwareFilterOpenCL, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "hdmv_pgs_subtitle",
		})
		return args[slices.Index(args, "-filter_complex")+1]
	}

	want := "[0:v:0]" + tonemap.IntelOpenCLToneMapFilter(tonemap.SourcePQ, 0) + "[vmain];" +
		"[0:s:0]scale=out_color_matrix=bt709:out_range=tv,format=yuva420p,hwupload=derive_device=opencl[sub];" +
		"[vmain][sub]overlay_opencl=eof_action=pass,scale_opencl=w=-2:h=1080," +
		tonemap.IntelOpenCLReturnFilter() + "," + tonemap.QSVInteropFilter() + "," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
	if got := graphFor(tonemap.SourcePQ, "1080p", false); got != want {
		t.Fatalf("QSV HDR bitmap burn-in graph:\n got %s\nwant %s", got, want)
	}

	for _, kind := range []tonemap.SourceKind{tonemap.SourcePQ, tonemap.SourceHLG, tonemap.SourceDolbyVision} {
		for _, resolution := range []string{"1080p", ""} {
			for _, softwareDecode := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s %q software decode %t", kind, resolution, softwareDecode), func(t *testing.T) {
					graph := graphFor(kind, resolution, softwareDecode)
					if strings.HasPrefix(graph, "[0:v:0]format=p010le,hwupload,") != softwareDecode {
						t.Fatalf("software-decode upload prefix is wrong: %s", graph)
					}
					toneMap := strings.Index(graph, tonemap.HardwareFilterOpenCL)
					if toneMap < 0 || strings.Contains(graph, "overlay_vaapi") || strings.Contains(graph[toneMap:], "scale_vaapi=w=") {
						t.Fatalf("a VAAPI filter processes the tone-mapped surface: %s", graph)
					}
					steps := []string{"overlay_opencl=eof_action=pass,", tonemap.IntelOpenCLReturnFilter() + "," + tonemap.QSVInteropFilter() + ","}
					if resolution != "" {
						steps = []string{"overlay_opencl=eof_action=pass,", "scale_opencl=w=-2:h=1080,", tonemap.IntelOpenCLReturnFilter() + "," + tonemap.QSVInteropFilter() + ","}
					} else if strings.Contains(graph, "scale_opencl") {
						t.Fatalf("unscaled graph resizes on OpenCL: %s", graph)
					}
					last := toneMap
					for _, step := range steps {
						index := strings.Index(graph, step)
						if index <= last {
							t.Fatalf("%q is missing or out of order: %s", step, graph)
						}
						last = index
					}
				})
			}
		}
	}

	if graph := graphFor(tonemap.SourceSDRBT2020, "1080p", false); !strings.Contains(graph, "overlay_vaapi=eof_action=pass") {
		t.Fatalf("SDR base layer bitmap burn-in does not use overlay_vaapi: %s", graph)
	}
}

// TestIntelVAAPIToneMapsHDROnOpenCL covers #2414: a VAAPI recipe the probe
// froze with tonemap_opencl (an Intel GPU) takes the OpenCL graph QSV uses on
// every subtitle path, hands VAAPI surfaces straight to h264_vaapi without the
// QSV interop, and declares the OpenCL device only for HDR sources.
func TestIntelVAAPIToneMapsHDROnOpenCL(t *testing.T) {
	argsFor := func(kind tonemap.SourceKind, subtitleCodec string) []string {
		opts := TranscodeOpts{
			InputPath: "/media/hdr.mkv", OutputDir: t.TempDir(), TargetCodecVideo: "h264", TargetCodecAudio: "aac",
			TargetResolution: "1080p", HWAccel: transcodeHWVAAPI, HWDevice: "/dev/dri/renderD128", SourceVideoBitDepth: 10,
			ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: kind,
			ToneMapFilter: tonemap.HardwareFilterOpenCL, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1, FileSize: 1, StreamSignature: "stream"},
			SubtitleTrackIndex:    -1,
		}
		if subtitleCodec != "" {
			opts.SubtitleBurnIn, opts.SubtitleTrackIndex, opts.SubtitleCodec = true, 0, subtitleCodec
		}
		if err := validateToneMapOpts(opts); err != nil {
			t.Fatalf("VAAPI OpenCL recipe rejected: %v", err)
		}
		return buildFFmpegArgs(opts)
	}

	for _, kind := range []tonemap.SourceKind{tonemap.SourcePQ, tonemap.SourceHLG, tonemap.SourceHLGBT709, tonemap.SourceDolbyVision} {
		for _, subtitleCodec := range []string{"", "subrip", "hdmv_pgs_subtitle"} {
			t.Run(fmt.Sprintf("%s subtitles %q", kind, subtitleCodec), func(t *testing.T) {
				joined := strings.Join(argsFor(kind, subtitleCodec), " ")
				for _, want := range []string{"-init_hw_device vaapi=hw:/dev/dri/renderD128 -init_hw_device opencl=ocl@hw -filter_hw_device hw", "tonemap_opencl=", tonemap.IntelOpenCLReturnFilter(), "-c:v h264_vaapi"} {
					if !strings.Contains(joined, want) {
						t.Fatalf("graph lacks %q: %s", want, joined)
					}
				}
				if strings.Contains(joined, tonemap.HardwareFilterVAAPI) || strings.Contains(joined, "derive_device=qsv") || strings.Contains(joined, "overlay_vaapi") {
					t.Fatalf("graph reaches tonemap_vaapi, the QSV interop, or overlay_vaapi: %s", joined)
				}
				toneMap := strings.Index(joined, tonemap.HardwareFilterOpenCL+"=")
				if strings.Contains(joined[toneMap:], "scale_vaapi=w=") {
					t.Fatalf("a VAAPI scale reads the tone-mapped surface: %s", joined)
				}
				if resize := strings.Index(joined, "scale_vaapi=w=-2:h=1080:format=p010"); subtitleCodec == "" && (resize < 0 || resize > toneMap) {
					t.Fatalf("graph does not resize before tone mapping: %s", joined)
				}
			})
		}
	}

	bitmap := argsFor(tonemap.SourcePQ, "hdmv_pgs_subtitle")
	want := "[0:v:0]" + tonemap.IntelOpenCLToneMapFilter(tonemap.SourcePQ, 0) + "[vmain];" +
		"[0:s:0]scale=out_color_matrix=bt709:out_range=tv,format=yuva420p,hwupload=derive_device=opencl[sub];" +
		"[vmain][sub]overlay_opencl=eof_action=pass,scale_opencl=w=-2:h=1080," +
		tonemap.IntelOpenCLReturnFilter() + "," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
	if got := bitmap[slices.Index(bitmap, "-filter_complex")+1]; got != want {
		t.Fatalf("VAAPI HDR bitmap burn-in graph:\n got %s\nwant %s", got, want)
	}

	// SDR bases keep the VAAPI color conversion and need no OpenCL runtime.
	sdr := strings.Join(argsFor(tonemap.SourceSDRBT2020, ""), " ")
	if strings.Contains(sdr, "opencl") || !strings.Contains(sdr, tonemap.VAAPIFilter(tonemap.SourceSDRBT2020)) {
		t.Fatalf("SDR base on the Intel VAAPI recipe uses OpenCL: %s", sdr)
	}
}

// TestDolbyVisionBaseGraphsApplyTheRPU verifies that a Profile 5 style base
// reaches FFmpeg only through filters told to apply its RPU, with or without
// subtitle burn-in, and that a frozen recipe naming a filter that cannot apply
// one is refused before FFmpeg starts.
func TestDolbyVisionBaseGraphsApplyTheRPU(t *testing.T) {
	revision := tonemap.SourceRevision{MediaFileID: 1, FileSize: 1, FileModifiedUnixNano: 1, FileHash: "hash", ProbeUpdatedUnixNano: 1, StreamSignature: "stream"}
	tests := []struct {
		name    string
		mode    tonemap.Mode
		hwAccel string
		filter  string
		want    string
	}{
		{name: "software", mode: tonemap.ModeSoftware, hwAccel: "none", filter: tonemap.SoftwareFilterBT2390, want: "tonemapx=tonemap=bt2390:peak=100:apply_dovi=1"},
		{name: "QSV", mode: tonemap.ModeHardware, hwAccel: "qsv", filter: tonemap.HardwareFilterOpenCL, want: "tonemap_opencl=format=nv12:p=bt709:t=bt709:m=bt709:tonemap=bt2390:peak=100:desat=0:apply_dovi=1"},
		{name: "NVENC", mode: tonemap.ModeHardware, hwAccel: "nvenc", filter: tonemap.HardwareFilterCUDA, want: "tonemap_cuda=tonemap=bt2390:format=nv12:p=bt709:t=bt709:m=bt709:r=tv:apply_dovi=1"},
	}
	for _, tt := range tests {
		for _, burnIn := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s burn-in %t", tt.name, burnIn), func(t *testing.T) {
				opts := TranscodeOpts{
					InputPath: "/media/dovi.mkv", OutputDir: t.TempDir(), SourceVideoBitDepth: 10,
					TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p", HWAccel: tt.hwAccel,
					ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tt.mode,
					ToneMapSourceKind: tonemap.SourceDolbyVision, ToneMapFilter: tt.filter,
					ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: revision,
				}
				if burnIn {
					opts.SubtitleBurnIn, opts.SubtitleTrackIndex, opts.SubtitleCodec = true, 0, "subrip"
				}
				if err := validateToneMapOpts(opts); err != nil {
					t.Fatalf("Dolby Vision recipe rejected: %v", err)
				}
				joined := strings.Join(buildFFmpegArgs(opts), " ")
				if !strings.Contains(joined, tt.want) {
					t.Fatalf("args missing %q: %s", tt.want, joined)
				}
				if !strings.Contains(joined, "sidedata=mode=delete:type=DOVI_METADATA") {
					t.Fatalf("Dolby Vision metadata survives the conversion: %s", joined)
				}
			})
		}
	}

	err := validateToneMapOpts(TranscodeOpts{
		TargetCodecVideo: "h264", HWAccel: "vaapi", ToneMapPolicy: tonemap.PolicyHardwareOnly,
		ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourceDolbyVision,
		ToneMapFilter: tonemap.HardwareFilterVAAPI, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: revision,
	})
	if err == nil || !strings.Contains(err.Error(), "Dolby Vision RPU") {
		t.Fatalf("VAAPI Dolby Vision recipe error = %v, want refusal", err)
	}
}

// TestSDRBaseGraphsBypassLuminanceToneMapping verifies SDR-compatible bases avoid needless luminance mapping.
func TestSDRBaseGraphsBypassLuminanceToneMapping(t *testing.T) {
	tests := []struct {
		name       string
		mode       tonemap.Mode
		hwAccel    string
		filter     string
		sourceKind tonemap.SourceKind
		want       []string
	}{
		{name: "software BT709", mode: tonemap.ModeSoftware, hwAccel: "none", filter: tonemap.SoftwareFilterHable, sourceKind: tonemap.SourceSDRBT709, want: []string{"color_primaries=bt709", "zscale=p=bt709"}},
		{name: "software BT2020", mode: tonemap.ModeSoftware, hwAccel: "none", filter: tonemap.SoftwareFilterBT2390, sourceKind: tonemap.SourceSDRBT2020, want: []string{"color_primaries=bt2020", "zscale=p=bt709"}},
		{name: "QSV", mode: tonemap.ModeHardware, hwAccel: "qsv", filter: tonemap.HardwareFilterOpenCL, sourceKind: tonemap.SourceSDRBT709, want: []string{"scale_vaapi=format=nv12", "hwmap=derive_device=qsv"}},
		{name: "VAAPI", mode: tonemap.ModeHardware, hwAccel: "vaapi", filter: tonemap.HardwareFilterVAAPI, sourceKind: tonemap.SourceSDRBT2020, want: []string{"scale_vaapi=format=nv12", "h264_vaapi"}},
		{name: "NVENC", mode: tonemap.ModeHardware, hwAccel: "nvenc", filter: tonemap.HardwareFilterCUDA, sourceKind: tonemap.SourceSDRBT2020, want: []string{"hwdownload,format=p010le", "zscale=p=bt709", "hwupload_cuda", "h264_nvenc"}},
		{name: "VideoToolbox", mode: tonemap.ModeHardware, hwAccel: "videotoolbox", filter: tonemap.HardwareFilterVideoToolbox, sourceKind: tonemap.SourceSDRBT2020, want: []string{"scale_vt", "hwdownload,format=p010le,format=nv12", "h264_videotoolbox"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildFFmpegArgs(TranscodeOpts{
				InputPath: "/media/dovi.mkv", OutputDir: t.TempDir(), SourceVideoBitDepth: 10,
				FFmpegPath:       videoToolboxTestFFmpegFor(t, tt.hwAccel),
				TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p", HWAccel: tt.hwAccel,
				ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tt.mode,
				ToneMapSourceKind: tt.sourceKind, ToneMapFilter: tt.filter, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
			})
			joined := strings.Join(args, " ")
			for _, token := range tt.want {
				if !strings.Contains(joined, token) {
					t.Fatalf("args missing %q: %s", token, joined)
				}
			}
			for _, token := range []string{"tonemapx=", "tonemap=hable", "tonemap_vaapi", "tonemap_cuda"} {
				if strings.Contains(joined, token) {
					t.Fatalf("SDR fallback unexpectedly applies luminance tone mapping %q: %s", token, joined)
				}
			}
			if tt.hwAccel == transcodeHWNVENC && !strings.Contains(joined, "sidedata=mode=delete:type=DOVI_RPU_BUFFER") {
				t.Fatalf("NVENC SDR fallback did not remove Dolby Vision side data: %s", joined)
			}
		})
	}
}

// TestNVENCSDRBaseGraphsDownloadBeforeSubtitleComposition verifies CUDA frames return to software before overlays.
func TestNVENCSDRBaseGraphsDownloadBeforeSubtitleComposition(t *testing.T) {
	base := TranscodeOpts{
		InputPath: "/media/dovi.mkv", OutputDir: t.TempDir(), SourceVideoBitDepth: 10,
		TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p", HWAccel: "nvenc",
		ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware,
		ToneMapSourceKind: tonemap.SourceSDRBT2020, ToneMapFilter: tonemap.HardwareFilterCUDA,
		ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, SubtitleBurnIn: true, SubtitleTrackIndex: 0,
	}
	for _, codec := range []string{"subrip", "hdmv_pgs_subtitle"} {
		t.Run(codec, func(t *testing.T) {
			opts := base
			opts.SubtitleCodec = codec
			graph := strings.Join(buildFFmpegArgs(opts), " ")
			download := strings.Index(graph, "hwdownload,format=p010le")
			convert := strings.Index(graph, "zscale=p=bt709")
			compose := strings.Index(graph, "subtitles=")
			if codec == "hdmv_pgs_subtitle" {
				compose = strings.Index(graph, "overlay=eof_action=pass")
			}
			upload := strings.LastIndex(graph, "hwupload_cuda")
			if download < 0 || convert <= download || compose <= convert || upload <= compose {
				t.Fatalf("unsafe filter order: %s", graph)
			}
			if !strings.Contains(graph, "sidedata=mode=delete:type=DOVI_RPU_BUFFER") {
				t.Fatalf("NVENC SDR subtitle graph did not remove Dolby Vision side data: %s", graph)
			}
		})
	}
}

func TestPrepareSubtitleFilterInputCreatesParserSafeAlias(t *testing.T) {
	outputDir := t.TempDir()
	inputPath := "/media/I'm here [1080p].mkv"
	opts := TranscodeOpts{
		InputPath:          inputPath,
		OutputDir:          outputDir,
		SubtitleBurnIn:     true,
		SubtitleTrackIndex: 2,
		SubtitleCodec:      "subrip",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
	}

	if err := prepareSubtitleFilterInput(&opts); err != nil {
		t.Fatalf("prepareSubtitleFilterInput() error = %v", err)
	}
	wantAlias := filepath.Join(outputDir, subtitleFilterAliasName)
	if opts.subtitleFilterInputPath != wantAlias {
		t.Fatalf("subtitleFilterInputPath = %q, want %q", opts.subtitleFilterInputPath, wantAlias)
	}
	target, err := os.Readlink(wantAlias)
	if err != nil {
		t.Fatalf("read subtitle filter alias: %v", err)
	}
	if target != inputPath {
		t.Fatalf("subtitle filter alias target = %q, want %q", target, inputPath)
	}

	joined := strings.Join(buildFFmpegArgs(opts), " ")
	if !strings.Contains(joined, "-i "+inputPath) {
		t.Fatalf("media input should keep its original path: %s", joined)
	}
	if !strings.Contains(joined, "subtitles=filename='"+wantAlias+"':si=2") {
		t.Fatalf("subtitle filter should use the parser-safe alias: %s", joined)
	}
}

func TestRequiresSoftwareVideoDecodeForH264High10(t *testing.T) {
	tests := []struct {
		codec    string
		profile  string
		bitDepth int
		want     bool
	}{
		{codec: "h264", profile: "High 10", bitDepth: 10, want: true},
		{codec: "avc", profile: "Hi10P", bitDepth: 0, want: true},
		{codec: "h264", profile: "High", bitDepth: 10, want: true},
		{codec: "h264", profile: "High", bitDepth: 8, want: false},
		{codec: "hevc", profile: "Main 10", bitDepth: 10, want: false},
	}
	for _, test := range tests {
		if got := RequiresSoftwareVideoDecode(test.codec, test.profile, test.bitDepth); got != test.want {
			t.Errorf("RequiresSoftwareVideoDecode(%q, %q, %d) = %v, want %v", test.codec, test.profile, test.bitDepth, got, test.want)
		}
	}
}

func TestBuildFFmpegArgs_H264High10UploadsBeforeHardwareToneMap(t *testing.T) {
	for _, test := range []struct {
		name    string
		hwAccel string
	}{
		{name: "QSV", hwAccel: transcodeHWQSV},
		{name: "VAAPI", hwAccel: transcodeHWVAAPI},
	} {
		for _, subtitle := range []struct {
			name  string
			codec string
		}{
			{name: "none"},
			{name: "text", codec: "ass"},
			{name: "bitmap", codec: "hdmv_pgs_subtitle"},
		} {
			t.Run(test.name+"/"+subtitle.name, func(t *testing.T) {
				opts := TranscodeOpts{
					InputPath: "/media/high10-hdr.mkv", OutputDir: t.TempDir(), SessionID: "high10-tone-map",
					SourceVideoCodec: "h264", SourceVideoProfile: "High 10", SourceVideoBitDepth: 10,
					TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
					HWAccel: test.hwAccel, TargetResolution: "720p",
					ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware,
					ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: map[string]string{transcodeHWQSV: tonemap.HardwareFilterOpenCL, transcodeHWVAAPI: tonemap.HardwareFilterVAAPI}[test.hwAccel],
					ToneMapRecipeVersion:  TransformationHDRToSDRToneMapRecipeVersionV3,
					ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1},
					SubtitleTrackIndex:    -1,
				}
				if subtitle.codec != "" {
					opts.SubtitleTrackIndex = 0
					opts.SubtitleBurnIn = true
					opts.SubtitleCodec = subtitle.codec
				}
				joined := strings.Join(buildFFmpegArgs(opts), " ")
				upload := strings.Index(joined, "format=p010le,hwupload")
				toneMapName := tonemap.HardwareFilterVAAPI
				if test.hwAccel == transcodeHWQSV {
					toneMapName = tonemap.HardwareFilterOpenCL
				}
				toneMap := strings.Index(joined, toneMapName)
				if upload < 0 || toneMap < 0 || upload >= toneMap {
					t.Fatalf("software frames were not uploaded before %s: %s", toneMapName, joined)
				}
				if strings.Contains(joined, "-hwaccel vaapi") || strings.Contains(joined, "-hwaccel_output_format vaapi") {
					t.Fatalf("High 10 source unexpectedly selected hardware decode: %s", joined)
				}
			})
		}
	}
}

func TestResolveEffectiveTranscodeHWAccel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts TranscodeOpts
		want string
	}{
		{
			name: "hardware video transcode",
			opts: TranscodeOpts{HWAccel: "qsv", SourceVideoCodec: "h264", TargetCodecVideo: "h264"},
			want: "qsv",
		},
		{
			name: "copy video does not use hardware encode",
			opts: TranscodeOpts{HWAccel: "qsv", SourceVideoCodec: "h264", TargetCodecVideo: "copy"},
			want: "none",
		},
		{
			name: "mpeg4 part 2 falls back to software",
			opts: TranscodeOpts{HWAccel: "vaapi", SourceVideoCodec: "mpeg4", TargetCodecVideo: "h264"},
			want: "none",
		},
		{
			name: "nvenc passthrough",
			opts: TranscodeOpts{HWAccel: "nvenc", SourceVideoCodec: "h264", TargetCodecVideo: "h264"},
			want: "nvenc",
		},
		{
			name: "qsv keeps hardware encode with software decode",
			opts: TranscodeOpts{HWAccel: "qsv", SourceVideoCodec: "h264", SoftwareVideoDecode: true, TargetCodecVideo: "h264"},
			want: "qsv",
		},
		{
			name: "explicit nvenc with software decode falls back to software encode",
			opts: TranscodeOpts{HWAccel: "nvenc", SourceVideoCodec: "h264", SoftwareVideoDecode: true, TargetCodecVideo: "h264"},
			want: "none",
		},
		{
			name: "automatic mixed stage keeps nvenc with software decode",
			opts: TranscodeOpts{HWAccel: "nvenc", SourceVideoCodec: "h264", SoftwareVideoDecode: true, TargetCodecVideo: "h264", nvencSoftwareDecode: true},
			want: "nvenc",
		},
		{
			name: "mixed permission never splices into a tone-map recipe",
			opts: TranscodeOpts{HWAccel: "nvenc", SourceVideoCodec: "hevc", SoftwareVideoDecode: true, TargetCodecVideo: "h264", nvencSoftwareDecode: true, ToneMapMode: tonemap.ModeHardware},
			want: "none",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveEffectiveTranscodeHWAccel(tt.opts); got != tt.want {
				t.Fatalf("resolveEffectiveTranscodeHWAccel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveEffectiveTranscodeHWAccelVideoToolboxChecksTargetEncoder(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{videotoolbox: true, h264VT: true, smokeOK: true})

	base := TranscodeOpts{
		FFmpegPath:        ffmpeg.path,
		HWAccel:           "videotoolbox",
		SourceVideoCodec:  "h264",
		TargetBitrateKbps: 2000,
	}

	h264 := base
	h264.TargetCodecVideo = "h264"
	if got := resolveEffectiveTranscodeHWAccel(h264); got != "videotoolbox" {
		t.Fatalf("H.264 target resolved to %q, want videotoolbox", got)
	}

	hevc := base
	hevc.TargetCodecVideo = "hevc"
	if got := resolveEffectiveTranscodeHWAccel(hevc); got != "none" {
		t.Fatalf("HEVC target resolved to %q, want software fallback", got)
	}
}

func TestNormalizeTranscodeOptsVideoToolboxHonorsCallerDeadline(t *testing.T) {
	setupHWAccelTest(t)
	ffmpeg := writeFakeFFmpeg(t, fakeFFmpegProbe{hang: true})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()

	started := time.Now()
	opts := normalizeTranscodeOptsContext(ctx, TranscodeOpts{
		FFmpegPath:        ffmpeg.path,
		HWAccel:           transcodeHWVideoToolbox,
		TargetCodecVideo:  "h264",
		TargetBitrateKbps: 2000,
	})
	if opts.HWAccel != HWAccelNone {
		t.Fatalf("HWAccel = %q, want software after caller deadline", opts.HWAccel)
	}
	if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
		t.Fatalf("normalization took %s, want less than the probe command timeout", elapsed)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
	}

	// Let the bounded shared probe finish before setupHWAccelTest restores its
	// package globals during cleanup.
	_ = cachedVideoToolboxProbe(ffmpeg.path)
}

func TestResolveEffectiveTranscodeHWAccelVideoToolboxUnconstrainedUsesSoftware(t *testing.T) {
	setupHWAccelTest(t)
	currentGOOS = "darwin"
	ffmpeg := writeFakeFFmpeg(t, successfulVideoToolboxProbe())

	unconstrained := TranscodeOpts{
		FFmpegPath:       ffmpeg.path,
		HWAccel:          "videotoolbox",
		SourceVideoCodec: "h264",
		TargetCodecVideo: "h264",
	}
	if got := resolveEffectiveTranscodeHWAccel(unconstrained); got != "none" {
		t.Fatalf("unconstrained transcode resolved to %q, want software (quality-based CRF)", got)
	}

	toneMap := unconstrained
	toneMap.SourceVideoCodec = transcodeCodecHEVC
	toneMap.SourceVideoProfile = "Main 10"
	toneMap.SourceVideoBitDepth = 10
	toneMap.ToneMapMode = tonemap.ModeHardware
	if got := resolveEffectiveTranscodeHWAccel(toneMap); got != transcodeHWVideoToolbox {
		t.Fatalf("unconstrained hardware tone map resolved to %q, want VideoToolbox", got)
	}

	withResolution := unconstrained
	withResolution.TargetResolution = "720p"
	if got := resolveEffectiveTranscodeHWAccel(withResolution); got != "videotoolbox" {
		t.Fatalf("resolution-constrained transcode resolved to %q, want videotoolbox", got)
	}

	withBitrate := unconstrained
	withBitrate.TargetBitrateKbps = 2000
	if got := resolveEffectiveTranscodeHWAccel(withBitrate); got != "videotoolbox" {
		t.Fatalf("bitrate-constrained transcode resolved to %q, want videotoolbox", got)
	}
}

// A cap must be a ceiling on every hardware encoder: QSV selects CBR when
// -b:v equals -maxrate, and VAAPI ignores -maxrate once -qp selects CQP.
// VAAPI forces the capped mode detected on the device (FFmpeg's automatic
// mode can pick AVBR, which does not honor -maxrate).
func TestAppendVideoArgs_HardwareBitrateCapIsVBRCeiling(t *testing.T) {
	for _, tc := range []struct {
		hwAccel, codec, want string
	}{
		{"qsv", "h264", "-c:v h264_qsv -preset veryfast -b:v 4500k -maxrate 5000k -bufsize 10000k"},
		{"qsv", "hevc", "-c:v hevc_qsv -preset veryfast -b:v 4500k -maxrate 5000k -bufsize 10000k"},
		{"vaapi", "h264", "-c:v h264_vaapi -b:v 4500k -maxrate 5000k -bufsize 10000k"},
		{"vaapi", "hevc", "-c:v hevc_vaapi -b:v 4500k -maxrate 5000k -bufsize 10000k"},
		{"nvenc", "h264", "-c:v h264_nvenc -rc:v vbr -b:v 4500k -maxrate 5000k -bufsize 10000k"},
		{"nvenc", "hevc", "-c:v hevc_nvenc -rc:v vbr -b:v 4500k -maxrate 5000k -bufsize 10000k"},
	} {
		joined := strings.Join(appendVideoArgs(nil, TranscodeOpts{HWAccel: tc.hwAccel, TargetCodecVideo: tc.codec, TargetBitrateKbps: 5000}), " ")
		if joined != tc.want {
			t.Errorf("%s/%s capped args = %q, want %q", tc.hwAccel, tc.codec, joined, tc.want)
		}
		if tc.hwAccel == "vaapi" {
			detected := strings.Join(appendVideoArgs(nil, TranscodeOpts{HWAccel: tc.hwAccel, TargetCodecVideo: tc.codec, TargetBitrateKbps: 5000, vaapiRateControl: "VBR"}), " ")
			if want := strings.Replace(tc.want, "_vaapi ", "_vaapi -rc_mode VBR ", 1); detected != want {
				t.Errorf("%s/%s capped args with VBR detected = %q, want %q", tc.hwAccel, tc.codec, detected, want)
			}
		}
		uncapped := strings.Join(appendVideoArgs(nil, TranscodeOpts{HWAccel: tc.hwAccel, TargetCodecVideo: tc.codec, vaapiRateControl: "VBR"}), " ")
		if strings.Contains(uncapped, "-maxrate") || strings.Contains(uncapped, "-rc_mode") {
			t.Errorf("%s/%s uncapped args must keep constant-quality mode: %q", tc.hwAccel, tc.codec, uncapped)
		}
	}
}

// TranscodesAudio must agree with appendAudioArgs: only an explicit "copy"
// passes audio through; an empty codec runs ffmpeg's AAC default.
func TestTranscodesAudioMatchesFFmpegDefault(t *testing.T) {
	cases := []struct {
		codec string
		want  bool
	}{
		{"copy", false},
		{"COPY", false},
		{" COPY ", false},
		{"", true},
		{"aac", true},
		{"opus", true},
	}
	for _, tc := range cases {
		if got := TranscodesAudio(tc.codec); got != tc.want {
			t.Errorf("TranscodesAudio(%q) = %v, want %v", tc.codec, got, tc.want)
		}
		args := appendAudioArgs(nil, TranscodeOpts{TargetCodecAudio: tc.codec})
		copied := strings.Contains(strings.Join(args, " "), "-c:a copy")
		if copied != !tc.want {
			t.Errorf("appendAudioArgs(%q) copy=%v disagrees with TranscodesAudio=%v", tc.codec, copied, tc.want)
		}
	}
}

func TestAppendAudioArgsNormalizesEveryAACEncodeAndBoostsOnlySurroundToStereo(t *testing.T) {
	const boostFilter = "aresample=out_chlayout=stereo:async=1,alimiter=level_in=2:limit=0.794328235:attack=5:release=50:level=false:latency=true"
	const normalizeFilter = "aresample=async=1"
	tests := []struct {
		name           string
		codec          string
		sourceChannels int
		targetChannels int
		wantFilter     string
	}{
		{name: "AAC alias to stereo", codec: " AAC ", sourceChannels: 8, targetChannels: 2, wantFilter: boostFilter},
		{name: "DTS 5.1 to AAC stereo", codec: "aac", sourceChannels: 6, targetChannels: 2, wantFilter: boostFilter},
		{name: "TrueHD 7.1 to default AAC stereo", sourceChannels: 8, targetChannels: 2, wantFilter: boostFilter},
		{name: "EAC3 surround to default AAC stereo", codec: "aac", sourceChannels: 6, wantFilter: boostFilter},
		{name: "opus has no versioned boost recipe", codec: "opus", sourceChannels: 6},
		{name: "unknown codec AAC fallback", codec: "unknown", sourceChannels: 6, targetChannels: 2, wantFilter: normalizeFilter},
		{name: "stereo AAC encode", codec: "aac", sourceChannels: 2, targetChannels: 2, wantFilter: normalizeFilter},
		{name: "unknown source channels", codec: "aac", targetChannels: 2, wantFilter: normalizeFilter},
		{name: "surround to AAC mono", codec: "aac", sourceChannels: 6, targetChannels: 1, wantFilter: normalizeFilter},
		{name: "negative target resolves to ordinary AAC stereo", codec: "aac", sourceChannels: 6, targetChannels: -1, wantFilter: normalizeFilter},
		{name: "noncanonical target resolves to ordinary AAC stereo", codec: "aac", sourceChannels: 6, targetChannels: 3, wantFilter: normalizeFilter},
		{name: "surround AAC preserved", codec: "aac", sourceChannels: 6, targetChannels: 6, wantFilter: normalizeFilter},
		{name: "copy", codec: "copy", sourceChannels: 6, targetChannels: 2},
		{name: "ac3 preserves source layout", codec: "ac3", sourceChannels: 6, targetChannels: 2},
		{name: "eac3 preserves source layout", codec: "eac3", sourceChannels: 6, targetChannels: 2},
		{name: "stereo opus encode", codec: "opus", sourceChannels: 2, targetChannels: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := IsAudioToAACStereoDownmixV3(tt.sourceChannels, tt.codec, tt.targetChannels), tt.wantFilter == boostFilter; got != want {
				t.Fatalf("downmix recipe match = %t, want %t", got, want)
			}
			args := appendAudioArgs(nil, TranscodeOpts{
				TargetCodecAudio:    tt.codec,
				SourceAudioChannels: tt.sourceChannels,
				TargetAudioChannels: tt.targetChannels,
			})
			var gotFilter string
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-af" {
					gotFilter = args[i+1]
					break
				}
			}
			if gotFilter != tt.wantFilter {
				t.Fatalf("audio filter = %q, want %q; args=%s", gotFilter, tt.wantFilter, strings.Join(args, " "))
			}
		})
	}
}

// videoToolboxTestFFmpeg supplies a fake VideoToolbox-capable ffmpeg so the
// argument tests validate construction independent of the host OS: the
// builder's encoder probe must succeed on Linux CI as well as macOS.
func videoToolboxTestFFmpeg(t *testing.T) string {
	t.Helper()
	resetHWProbeCacheForTest()
	t.Cleanup(resetHWProbeCacheForTest)
	return writeFakeFFmpeg(t, successfulVideoToolboxProbe()).path
}

func videoToolboxTestFFmpegFor(t *testing.T, hwAccel string) string {
	t.Helper()
	if hwAccel != transcodeHWVideoToolbox {
		return ""
	}
	return videoToolboxTestFFmpeg(t)
}

// nvencMixedStageOpts returns the automatic pipeline's CPU-decode + NVENC stage.
func nvencMixedStageOpts(t *testing.T, opts TranscodeOpts) TranscodeOpts {
	t.Helper()
	pipeline := newResolvedAutoTranscodePipeline(opts, newAutoTranscodePipelineCache())
	if !pipeline.AdvanceAfterFailure("") {
		t.Fatal("expected mixed stage")
	}
	mixed := pipeline.Current()
	if mixed.HWAccel != transcodeHWNVENC || !mixed.SoftwareVideoDecode {
		t.Fatalf("mixed stage = hw_accel %q, software_decode %v", mixed.HWAccel, mixed.SoftwareVideoDecode)
	}
	return mixed
}

// Every backend scales to the same height for a ladder label or an exact
// box-fit height, and leaves the source alone for anything else.
func TestScaleFiltersShareTargetHeightParsing(t *testing.T) {
	for _, tc := range []struct {
		res    string
		height string
	}{
		{"2160p", "2160"}, {"1080p", "1080"}, {"720p", "720"}, {"540p", "540"},
		{"480p", "480"}, {"420p", "420"}, {"328p", "328"}, {"800p", "800"}, {" 1080P ", "1080"},
		{"66p", "66"}, // a very wide source fitted into the 480p box
	} {
		if got, want := resolutionToScale(tc.res), "scale=-2:"+tc.height; got != want {
			t.Errorf("resolutionToScale(%q) = %q, want %q", tc.res, got, want)
		}
		if got, want := vaapiScaleFilter(tc.res), "scale_vaapi=w=-2:h="+tc.height+":format=nv12"; got != want {
			t.Errorf("vaapiScaleFilter(%q) = %q, want %q", tc.res, got, want)
		}
		if got, want := qsvScaleFilter(tc.res), "scale_vaapi=w=-2:h="+tc.height+":format=nv12,hwmap=derive_device=qsv,format=qsv"; got != want {
			t.Errorf("qsvScaleFilter(%q) = %q, want %q", tc.res, got, want)
		}
		if got, want := nvencScaleFilter(tc.res), "scale_cuda=w=-2:h="+tc.height+":format=nv12"; got != want {
			t.Errorf("nvencScaleFilter(%q) = %q, want %q", tc.res, got, want)
		}
		if w, h := videoToolboxScaleDimensions(tc.res); w != "-2" || h != tc.height {
			t.Errorf("videoToolboxScaleDimensions(%q) = %s:%s, want -2:%s", tc.res, w, h, tc.height)
		}
	}
	for _, res := range []string{"", "4k", "original", "817p", "8640p", "0p", "-2p", "p"} {
		if got := resolutionToScale(res); got != "" {
			t.Errorf("resolutionToScale(%q) = %q, want no scale", res, got)
		}
		if got := vaapiScaleFilter(res); got != "scale_vaapi=format=nv12" {
			t.Errorf("vaapiScaleFilter(%q) = %q, want format-only", res, got)
		}
		if w, h := videoToolboxScaleDimensions(res); w != "iw" || h != "ih" {
			t.Errorf("videoToolboxScaleDimensions(%q) = %s:%s, want iw:ih", res, w, h)
		}
	}
}

// The rate-control probe runs the ordinary VAAPI smoke encode in the capped
// mode a transcode would request, and a detected mode is forced.
func TestVAAPIRateControlSmokeArgsAndForcedMode(t *testing.T) {
	joined := strings.Join(vaapiRateControlSmokeArgs("/dev/dri/renderD128", "hevc_vaapi", "CBR"), " ")
	for _, want := range []string{"-c:v hevc_vaapi -rc_mode CBR -b:v 1800k -maxrate 2000k -f null -", "/dev/dri/renderD128"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rate-control smoke args missing %q: %s", want, joined)
		}
	}
	cbr := strings.Join(appendVideoArgs(nil, TranscodeOpts{HWAccel: "vaapi", TargetCodecVideo: "h264", TargetBitrateKbps: 5000, vaapiRateControl: "CBR"}), " ")
	if cbr != "-c:v h264_vaapi -rc_mode CBR -b:v 4500k -maxrate 5000k -bufsize 10000k" {
		t.Fatalf("CBR-only device args = %q", cbr)
	}
	if opts, err := resolveVAAPIRateControl(context.Background(), TranscodeOpts{HWAccel: "qsv", TargetBitrateKbps: 5000, vaapiRateControl: "VBR"}); err != nil || opts.vaapiRateControl != "" {
		t.Fatalf("a non-VAAPI encode must not keep a VAAPI mode: %q %v", opts.vaapiRateControl, err)
	}
	if opts, err := resolveVAAPIRateControl(context.Background(), TranscodeOpts{HWAccel: "vaapi", vaapiRateControl: "VBR"}); err != nil || opts.vaapiRateControl != "" {
		t.Fatalf("an uncapped VAAPI encode needs no mode: %q %v", opts.vaapiRateControl, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveVAAPIRateControl(canceled, TranscodeOpts{HWAccel: "vaapi", TargetCodecVideo: "h264", TargetBitrateKbps: 5000, FFmpegPath: "/nonexistent/ffmpeg"}); err == nil {
		t.Fatal("a canceled start must stop instead of launching FFmpeg")
	}
}

// nvencMixedOpts is an NVENC transcode the automatic pipeline can split into
// CPU decode plus NVENC encode; a subtitle codec turns on burn-in of track 1.
func nvencMixedOpts(hwDevice, subtitleCodec string) TranscodeOpts {
	opts := TranscodeOpts{
		InputPath:         "/media/movie.mkv",
		OutputDir:         "/tmp/out",
		SessionID:         "session-nvenc-mixed",
		SourceVideoCodec:  "hevc",
		TargetCodecVideo:  "h264",
		TargetCodecAudio:  "aac",
		SegmentDuration:   2,
		HWAccel:           transcodeHWNVENC,
		HWDevice:          hwDevice,
		TargetResolution:  "720p",
		TargetBitrateKbps: 2000,
	}
	if subtitleCodec != "" {
		opts.SubtitleTrackIndex, opts.SubtitleBurnIn, opts.SubtitleCodec = 1, true, subtitleCodec
	}
	return opts
}

func nvencAutomaticFullHardwareStage(_ *testing.T, opts TranscodeOpts) TranscodeOpts {
	return newResolvedAutoTranscodePipeline(opts, newAutoTranscodePipelineCache()).Current()
}

var recipeTokenEncoders = []string{"h264_qsv", "h264_vaapi", "h264_nvenc", "libx264", "hevc_qsv", "hevc_nvenc"}

var toneMapSubtitleBase = TranscodeOpts{
	InputPath: "/media/hdr.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p",
	HWAccel: "none", ToneMapPolicy: tonemap.PolicySoftwareOnly, ToneMapMode: tonemap.ModeSoftware, ToneMapSourceKind: tonemap.SourcePQ,
	ToneMapFilter: "tonemapx", ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, SubtitleBurnIn: true, SubtitleTrackIndex: 0,
}

var videoToolboxToneMapSubtitleBase = TranscodeOpts{
	InputPath: "/media/hdr.mkv", SourceVideoBitDepth: 10,
	TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetResolution: "1080p",
	HWAccel: transcodeHWVideoToolbox, ToneMapPolicy: tonemap.PolicyHardwareOnly,
	ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ,
	ToneMapFilter:        tonemap.HardwareFilterVideoToolbox,
	ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
	SubtitleBurnIn:       true, SubtitleTrackIndex: 0,
}

func withSubtitle(opts TranscodeOpts, codec string) TranscodeOpts {
	opts.SubtitleCodec = codec
	return opts
}

var mixedStageWant = []string{"-vf format=nv12,hwupload,scale_cuda=w=-2:h=720:format=nv12", "-c:v h264_nvenc"}

// Mixed stages decode on the CPU, upload with the device-aware hwupload
// (hwupload_cuda ignores -filter_hw_device), and let FFmpeg autorotate.
var mixedStageForbid = []string{"-hwaccel cuda", "-hwaccel_device", "hwupload_cuda", "-noautorotate"}

var fullHardwareNVENC = []string{"-c:v h264_nvenc", "-hwaccel cuda -hwaccel_output_format cuda -noautorotate -hwaccel_device 1 ", "-vf scale_cuda=w=-2:h=720:format=nv12"}

func textBurnInDepth(name, hwAccel string, bitDepth int, wantVF string) ffmpegArgsTokenCase {
	return ffmpegArgsTokenCase{
		name: "text burn-in downloads source depth " + name,
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-srt-" + name,
			SourceVideoCodec: "hevc", SourceVideoProfile: "main10", SourceVideoBitDepth: bitDepth,
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: hwAccel, TargetResolution: "720p",
			SubtitleTrackIndex: 1, SubtitleBurnIn: true, SubtitleCodec: "subrip",
		},
		want:   []string{wantVF},
		forbid: []string{"hwdownload,format=yuv420p"},
	}
}

func mpeg4Part2(hwAccel string) ffmpegArgsTokenCase {
	return ffmpegArgsTokenCase{
		name: "mpeg4 part 2 disables hardware decode " + hwAccel,
		opts: TranscodeOpts{
			InputPath: "/media/xvid.avi", OutputDir: "/tmp/out", SessionID: "session-xvid", SourceVideoCodec: "mpeg4",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: hwAccel,
			TargetResolution: "420p", TargetBitrateKbps: 720,
		},
		want:   []string{"-c:v libx264", "-vf scale=-2:420"},
		forbid: []string{"-hwaccel vaapi", "h264_qsv", "h264_vaapi", "scale_vaapi", "hwmap=derive_device=qsv"},
	}
}

func nonQSVForcedIDR(hwAccel string) ffmpegArgsTokenCase {
	return ffmpegArgsTokenCase{
		name: "non-QSV omits forced_idr " + hwAccel,
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-non-qsv-idr", SourceVideoCodec: "vp9",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: hwAccel,
		},
		forbid: []string{"-forced_idr"},
	}
}

var buildFFmpegArgsEmitsRecipeTokensCases = []ffmpegArgsTokenCase{
	{
		name: "QSV drops superfast preset",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-1", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
			SegmentDuration: 2, HWAccel: "qsv", FastStart: true, TargetResolution: "1080p", TargetBitrateKbps: 2000,
		},
		want:   []string{"-preset veryfast"},
		forbid: []string{"-preset superfast"},
	},
	{
		name: "bounds HLS manifest size",
		opts: TranscodeOpts{
			InputPath: "/media/long.mkv", OutputDir: "/tmp/out", SessionID: "session-long", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
			SegmentDuration: 2, TotalDuration: 1_000_000,
		},
		want: []string{"-hls_list_size 50000"},
	},
	{
		// Copy video from the start preserves source timestamps, lifts the
		// AAC priming delay out of the first tfdt, and starts at zero.
		name: "copy video from start uses Jellyfin timestamps",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-copy", TargetCodecVideo: "copy", TargetCodecAudio: "aac",
			SegmentDuration: 2,
		},
		want:   []string{"-copyts", "-avoid_negative_ts make_non_negative", "-start_at_zero"},
		forbid: []string{"-avoid_negative_ts disabled", "-avoid_negative_ts make_zero"},
	},
	{
		name: "copy video applies validated bitstream filter",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-dv7", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
			VideoBitstreamFilter: DV7ToHDR10BitstreamFilter, SegmentDuration: 2,
		},
		want: []string{"-c:v copy -bsf:v dovi_rpu=strip=1,filter_units=remove_types=63 "},
	},
	{
		// Copy video stays copy, seeks before the input, transcodes audio
		// with -noaccurate_seek, and aligns fMP4 segment numbers.
		name: "copy video seek preserves codec copy",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-copy-seek", SeekSeconds: 240.86, StartSegmentNumber: 120,
			TargetCodecVideo: "copy", TargetCodecAudio: "aac", SegmentDuration: 2,
		},
		want:   []string{"-c:v copy", "-c:a aac", "-noaccurate_seek", "-hls_segment_type fmp4", "-start_number 120"},
		forbid: recipeTokenEncoders,
		order:  []string{"-ss", "-i "},
	},
	{
		// MPEG-TS has no tfdt, so the fMP4 negative-timestamp lift does not apply.
		name: "MPEG-TS copy video keeps source timestamps",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-copy-ts", SourceVideoCodec: "hevc",
			TargetCodecVideo: "copy", TargetCodecAudio: "aac", CopyVideoMPEGTS: true, SegmentDuration: 2,
		},
		want:   []string{"-copyts -avoid_negative_ts disabled -start_at_zero"},
		forbid: []string{"make_non_negative"},
	},
	{
		name: "copy video sample entry Dolby Vision",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", SourceVideoCodec: "hevc", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
			VideoSampleEntry: VideoSampleEntryDVH1, SegmentDuration: 2,
		},
		want: []string{"-c:v copy -bsf:v hevc_mp4toannexb -tag:v dvh1 -strict unofficial"},
	},
	{
		// MPEG-TS has no sample entry, but -strict unofficial is what makes the
		// MPEG-TS muxer write the DOVI video stream descriptor, so a preserved
		// Dolby Vision copy keeps its configuration in TS as well.
		name: "MPEG-TS copy video keeps Dolby Vision configuration",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SourceVideoCodec: "hevc", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
			VideoSampleEntry: VideoSampleEntryDVH1, CopyVideoMPEGTS: true, SegmentDuration: 2,
		},
		want:   []string{"-c:v copy -bsf:v hevc_mp4toannexb", "-strict unofficial", "-hls_segment_type mpegts", "seg_%05d.ts"},
		forbid: []string{"movflags=+frag_discont", ".m4s"},
	},
	{
		name: "copy video sample entry HDR10",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", SourceVideoCodec: "hevc", TargetCodecVideo: "copy", TargetCodecAudio: "copy",
			VideoSampleEntry: VideoSampleEntryHVC1, SegmentDuration: 2,
		},
		want:   []string{"-c:v copy -bsf:v hevc_mp4toannexb -tag:v hvc1"},
		forbid: []string{"-strict unofficial"},
	},
	{
		name: "copy video accepts noncanonical codec case",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", TargetCodecVideo: "COPY", TargetCodecAudio: "copy",
			VideoSampleEntry: VideoSampleEntryHVC1, SegmentDuration: 2,
		},
		want: []string{"-c:v copy -tag:v hvc1"},
	},
	{
		name: "encoded transcode preserves existing timestamp policy",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-encoded", SeekSeconds: 2780.63,
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
		},
		want: []string{"-copyts", "-avoid_negative_ts disabled"},
	},
	mpeg4Part2("qsv"),
	mpeg4Part2("vaapi"),
	{
		name: "QSV promotes forced segment keyframes to IDR",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-qsv-idr", SourceVideoCodec: "vp9",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "qsv",
		},
		want: []string{"-force_key_frames expr:gte(t,n_forced*2)", "-g 60 -keyint_min 60", "-forced_idr 1"},
	},
	nonQSVForcedIDR("vaapi"),
	nonQSVForcedIDR("nvenc"),
	nonQSVForcedIDR("none"),
	{
		name: "VAAPI scaling uses hardware filter",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-vaapi", SourceVideoCodec: "h264",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "vaapi", TargetResolution: "720p", TargetBitrateKbps: 2000,
		},
		want:   []string{"-hwaccel vaapi", "-vf scale_vaapi=w=-2:h=720:format=nv12"},
		forbid: []string{"-vf scale=-2:720"},
	},
	{
		// High 10 AVC must software-decode and scale software frames before the upload.
		name: "H.264 High 10 QSV uses software decode upload",
		opts: TranscodeOpts{
			InputPath: "/media/high10.mkv", OutputDir: "/tmp/out", SessionID: "session-high10-pgs-sidecar", SourceVideoCodec: "h264",
			SoftwareVideoDecode: true, TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "qsv",
			TargetResolution: "720p", SubtitleTrackIndex: 2, SubtitleCodec: "hdmv_pgs_subtitle",
		},
		want: []string{
			"-init_hw_device vaapi=va:", "-init_hw_device qsv=qs@va", "-c:v h264_qsv",
			"-vf scale=-2:720,format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv",
		},
		forbid: []string{"-hwaccel vaapi", "-hwaccel_output_format vaapi", "hwdownload", "scale_vaapi"},
	},
	{
		name: "H.264 High 10 QSV ASS burn-in uses software frames",
		opts: TranscodeOpts{
			InputPath: "/media/high10.mkv", OutputDir: "/tmp/out", SessionID: "session-high10-ass", SourceVideoCodec: "h264",
			SoftwareVideoDecode: true, TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "qsv",
			TargetResolution: "720p", SubtitleTrackIndex: 0, SubtitleBurnIn: true, SubtitleCodec: "ass",
		},
		want:   []string{"-vf format=yuv420p,scale=-2:720,subtitles=filename='/media/high10.mkv':si=0,format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv"},
		forbid: []string{"hwdownload", "-hwaccel vaapi"},
	},
	{
		name: "H.264 High 10 QSV bitmap burn-in uses software frames",
		opts: TranscodeOpts{
			InputPath: "/media/high10.mkv", OutputDir: "/tmp/out", SessionID: "session-high10-pgs-burn", SourceVideoCodec: "h264",
			SoftwareVideoDecode: true, TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "qsv",
			TargetResolution: "720p", SubtitleTrackIndex: 2, SubtitleBurnIn: true, SubtitleCodec: "hdmv_pgs_subtitle",
		},
		want:   []string{"-filter_complex [0:v:0]format=yuv420p[vmain];[vmain][0:s:2]overlay=eof_action=pass,scale=-2:720,format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv[vout]"},
		forbid: []string{"overlay_vaapi", "hwdownload", "-hwaccel vaapi"},
	},
	{
		// Overlay runs at native resolution first, then scales. The graph
		// output replaces the raw video mapping, and -vf alongside
		// -filter_complex on one stream is an FFmpeg error.
		name: "bitmap burn-in CPU uses overlay filter_complex",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-pgs", SourceVideoCodec: "h264",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "none", TargetResolution: "1080p",
			SubtitleTrackIndex: 2, SubtitleBurnIn: true, SubtitleCodec: "hdmv_pgs_subtitle",
		},
		want:   []string{"-filter_complex [0:v:0][0:s:2]overlay=eof_action=pass,scale=-2:1080[vout]", "-map [vout]", "-c:v libx264"},
		forbid: []string{"-map 0:v:0", "-vf ", "subtitles="},
	},
	{
		// Only the subtitle bitmap is uploaded; the video stays on the VAAPI
		// surface and is composited with overlay_vaapi.
		name: "bitmap burn-in VAAPI composites on GPU",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-pgs-vaapi", SourceVideoCodec: "h264",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "vaapi", TargetResolution: "720p",
			SubtitleTrackIndex: 1, SubtitleBurnIn: true, SubtitleCodec: "hdmv_pgs_subtitle",
		},
		want:   []string{"-filter_complex [0:s:1]format=bgra,hwupload[sub];[0:v:0][sub]overlay_vaapi=eof_action=pass,scale_vaapi=w=-2:h=720:format=nv12[vout]", "-map [vout]", "-c:v h264_vaapi"},
		forbid: []string{"hwdownload", "-vf "},
	},
	{
		// GPU composite via overlay_vaapi, then map the VAAPI surface to QSV
		// for the encoder; the video never leaves hardware memory.
		name: "bitmap burn-in QSV composites on GPU",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-pgs-qsv", SourceVideoCodec: "h264",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "qsv", TargetResolution: "720p",
			SubtitleTrackIndex: 1, SubtitleBurnIn: true, SubtitleCodec: "hdmv_pgs_subtitle",
		},
		want:   []string{"-filter_complex [0:s:1]format=bgra,hwupload[sub];[0:v:0][sub]overlay_vaapi=eof_action=pass,scale_vaapi=w=-2:h=720:format=nv12,hwmap=derive_device=qsv,format=qsv[vout]", "-c:v h264_qsv"},
		forbid: []string{"hwdownload", "-vf "},
	},
	{
		// A CUDA surface can only be downloaded in the software format its
		// frames context was created with, so a 10-bit source must come off
		// the GPU as p010le and reach yuv420p through a second conversion.
		// Requesting yuv420p from hwdownload itself fails the graph before
		// the encoder opens, which took out every 10-bit HEVC burn-in attempt.
		name: "bitmap burn-in NVENC downloads source depth",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-pgs-nvenc-10bit", SourceVideoCodec: "hevc",
			SourceVideoProfile: "main10", SourceVideoBitDepth: 10, TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2,
			HWAccel: "nvenc", TargetResolution: "720p", SubtitleTrackIndex: 0, SubtitleBurnIn: true, SubtitleCodec: "hdmv_pgs_subtitle",
		},
		want:   []string{"-filter_complex [0:v:0]hwdownload,format=p010le,format=yuv420p[vmain];[vmain][0:s:0]overlay=eof_action=pass,scale=-2:720,format=nv12,hwupload_cuda[vout]"},
		forbid: []string{"hwdownload,format=yuv420p"},
	},
	textBurnInDepth("NVENC8Bit", "nvenc", 8, "hwdownload,format=nv12,format=yuv420p,scale=-2:720,subtitles="),
	textBurnInDepth("NVENC10Bit", "nvenc", 10, "hwdownload,format=p010le,format=yuv420p,scale=-2:720,subtitles="),
	textBurnInDepth("VAAPI10Bit", "vaapi", 10, "hwdownload,format=p010le,format=yuv420p,scale=-2:720,subtitles="),
	textBurnInDepth("QSV10Bit", "qsv", 10, "hwdownload,format=p010le,format=yuv420p,scale=-2:720,subtitles="),
	{
		name: "text burn-in still uses subtitles filter",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-srt", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
			SegmentDuration: 2, HWAccel: "none", TargetResolution: "1080p", SubtitleTrackIndex: 1, SubtitleBurnIn: true, SubtitleCodec: "subrip",
		},
		want:   []string{"-vf scale=-2:1080,subtitles=filename='/media/movie.mkv':si=1", "-map 0:v:0"},
		forbid: []string{"-filter_complex"},
	},
	{
		// The API layer forces an encode before a burn-in transcode; if a
		// copy recipe slips through anyway the builder must stay a valid
		// copy command. "-filter_complex_threads" is a legitimate copy-mode
		// arg; only the filter graph option itself must be absent.
		name: "bitmap burn-in with copy video is inert",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-copy-burnin", TargetCodecVideo: "copy", TargetCodecAudio: "aac",
			SegmentDuration: 2, SubtitleTrackIndex: 0, SubtitleBurnIn: true, SubtitleCodec: "hdmv_pgs_subtitle",
		},
		want:   []string{"-c:v copy", "-map 0:v:0"},
		forbid: []string{"-filter_complex ", "overlay"},
	},
	{
		name:  "tone map orders text subtitles after color conversion",
		opts:  withSubtitle(toneMapSubtitleBase, "subrip"),
		order: []string{"tonemapx=tonemap=bt2390", "scale=-2:1080", "subtitles="},
	},
	{
		name:  "tone map orders bitmap subtitles after color conversion",
		opts:  withSubtitle(toneMapSubtitleBase, "hdmv_pgs_subtitle"),
		order: []string{"tonemapx=tonemap=bt2390", "overlay=eof_action=pass", "scale=-2:1080"},
	},
	{
		// VideoToolbox decode outputs software frames and takes no x264 preset.
		name: "VideoToolbox H.264 uses software filters",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-vt", SourceVideoCodec: "h264",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "videotoolbox", TargetResolution: "720p", TargetBitrateKbps: 2000,
		},
		videoToolbox: true,
		want: []string{
			"-hwaccel videotoolbox", "-c:v h264_videotoolbox", "-pix_fmt yuv420p", "-vf scale=-2:720",
			"-b:v 2000k -maxrate 2000k -bufsize 4000k", "-g 60 -keyint_min 60",
		},
		forbid: []string{"-hwaccel_output_format", "-preset"},
	},
	{
		name: "VideoToolbox Hi10P decodes in software",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-vt-hi10p", SourceVideoCodec: "h264", SourceVideoProfile: "High 10",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "videotoolbox", TargetBitrateKbps: 2000,
		},
		videoToolbox: true,
		want:         []string{"-c:v h264_videotoolbox"},
		forbid:       []string{"-hwaccel videotoolbox"},
	},
	{
		// Uncapped VideoToolbox HEVC forces Main 8-bit at the portable default bitrate.
		name: "VideoToolbox HEVC forces Main 8-bit",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-vt-hevc", SourceVideoCodec: "hevc", SourceVideoProfile: "Main 10",
			SourceVideoBitDepth: 10, TargetCodecVideo: "hevc", TargetCodecAudio: "copy", SegmentDuration: 2, HWAccel: "videotoolbox", TargetResolution: "1080p",
		},
		videoToolbox: true,
		want:         []string{"-c:v hevc_videotoolbox", "-pix_fmt yuv420p -profile:v main", "-b:v 6000k -maxrate 6000k -bufsize 12000k"},
	},
	{
		name: "VideoToolbox HEVC hardware tone map keeps NV12",
		opts: TranscodeOpts{
			InputPath: "/media/hdr.mkv", SessionID: "session-vt-hevc-tonemap",
			SourceVideoCodec: "hevc", SourceVideoProfile: "Main 10", SourceVideoBitDepth: 10,
			TargetCodecVideo: "hevc", TargetCodecAudio: "aac", SegmentDuration: 2, TargetResolution: "1080p",
			HWAccel: transcodeHWVideoToolbox, ToneMapPolicy: tonemap.PolicyHardwareThenSoftware,
			ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: tonemap.HardwareFilterVideoToolbox,
			ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
		},
		videoToolbox: true,
		want:         []string{"-c:v hevc_videotoolbox", "hwdownload,format=p010le,format=nv12"},
		forbid:       []string{"-pix_fmt", "-profile:v"},
	},
	{
		name: "VideoToolbox text burn-in stays on CPU filters",
		opts: TranscodeOpts{
			InputPath: "/media/movie.mkv", OutputDir: "/tmp/out", SessionID: "session-vt-sub", SourceVideoCodec: "h264",
			TargetCodecVideo: "h264", TargetCodecAudio: "aac", SegmentDuration: 2, HWAccel: "videotoolbox", TargetResolution: "720p",
			SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "subrip",
		},
		videoToolbox: true,
		want:         []string{"subtitles="},
		forbid:       []string{"hwdownload", "hwupload"},
	},
	{
		name:         "VideoToolbox tone map downloads before text subtitles",
		opts:         withSubtitle(videoToolboxToneMapSubtitleBase, "subrip"),
		videoToolbox: true,
		order:        []string{"scale_vt=w=-2:h=1080", "hwdownload,format=p010le,format=nv12", "subtitles=", "sidedata=mode=delete"},
		forbid:       []string{"scale=-2:1080"},
	},
	{
		name:         "VideoToolbox tone map downloads before bitmap subtitles",
		opts:         withSubtitle(videoToolboxToneMapSubtitleBase, "hdmv_pgs_subtitle"),
		videoToolbox: true,
		order:        []string{"scale_vt=w=-2:h=1080", "hwdownload,format=p010le,format=nv12", "overlay=eof_action=pass", "sidedata=mode=delete"},
		forbid:       []string{"scale=-2:1080"},
	},
	{
		name:   "NVENC mixed stage uploads to default device",
		opts:   nvencMixedOpts("", ""),
		stage:  nvencMixedStageOpts,
		want:   append([]string{"-init_hw_device cuda=cu -filter_hw_device cu"}, mixedStageWant...),
		forbid: mixedStageForbid,
	},
	{
		name:   "NVENC mixed stage uploads to configured device",
		opts:   nvencMixedOpts("1", ""),
		stage:  nvencMixedStageOpts,
		want:   append([]string{"-init_hw_device cuda=cu:1 -filter_hw_device cu"}, mixedStageWant...),
		forbid: mixedStageForbid,
	},
	{
		name:   "NVENC mixed stage text burn-in uploads without download",
		opts:   nvencMixedOpts("1", "subrip"),
		stage:  nvencMixedStageOpts,
		want:   []string{"-vf format=yuv420p,scale=-2:720,subtitles=filename='/media/movie.mkv':si=1,format=nv12,hwupload ", "-init_hw_device cuda=cu:1 -filter_hw_device cu", "-c:v h264_nvenc"},
		forbid: []string{"hwdownload", "hwupload_cuda"},
	},
	{
		name:   "NVENC mixed stage bitmap burn-in uploads without download",
		opts:   nvencMixedOpts("1", "hdmv_pgs_subtitle"),
		stage:  nvencMixedStageOpts,
		want:   []string{"-filter_complex [0:v:0]format=yuv420p[vmain];[vmain][0:s:1]overlay=eof_action=pass,scale=-2:720,format=nv12,hwupload[vout]", "-init_hw_device cuda=cu:1 -filter_hw_device cu", "-c:v h264_nvenc"},
		forbid: []string{"hwdownload", "hwupload_cuda"},
	},
	{
		name: "NVENC full hardware default device",
		opts: nvencMixedOpts("", ""),
		want: []string{"-hwaccel cuda -hwaccel_output_format cuda -noautorotate"},
	},
	{
		name:   "NVENC full hardware explicit",
		opts:   nvencMixedOpts("1", ""),
		want:   fullHardwareNVENC,
		forbid: []string{"-init_hw_device", "-filter_hw_device"},
	},
	{
		name:   "NVENC full hardware automatic stage",
		opts:   nvencMixedOpts("1", ""),
		stage:  nvencAutomaticFullHardwareStage,
		want:   fullHardwareNVENC,
		forbid: []string{"-init_hw_device", "-filter_hw_device"},
	},
}

// TestBuildFFmpegArgsEmitsRecipeTokens pins the argv each recipe produces:
// every want token must appear, no forbid token may, and order tokens must
// appear in that order (first occurrence of each).
func TestBuildFFmpegArgsEmitsRecipeTokens(t *testing.T) {
	for _, tc := range buildFFmpegArgsEmitsRecipeTokensCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			if opts.OutputDir == "" {
				opts.OutputDir = t.TempDir()
			}
			if tc.videoToolbox {
				opts.FFmpegPath = videoToolboxTestFFmpeg(t)
			}
			if tc.stage != nil {
				opts = tc.stage(t, opts)
			}
			joined := strings.Join(buildFFmpegArgs(opts), " ")
			for _, token := range tc.want {
				if !strings.Contains(joined, token) {
					t.Errorf("args missing %q: %s", token, joined)
				}
			}
			for _, token := range tc.forbid {
				if strings.Contains(joined, token) {
					t.Errorf("args contain forbidden %q: %s", token, joined)
				}
			}
			last := -1
			for _, token := range tc.order {
				index := strings.Index(joined, token)
				if index < 0 || index <= last {
					t.Fatalf("tokens %v are not ordered in %s", tc.order, joined)
				}
				last = index
			}
		})
	}
}

type ffmpegArgsTokenCase struct {
	name         string
	opts         TranscodeOpts
	videoToolbox bool // run against a fake VideoToolbox-capable ffmpeg
	stage        func(*testing.T, TranscodeOpts) TranscodeOpts
	want         []string
	forbid       []string
	order        []string
}

// TestStartTranscodeRejectsUnvalidatedRecipes covers admission of bitstream
// filter, sample-entry, and HLS video range recipes before FFmpeg starts.
func TestStartTranscodeRejectsUnvalidatedRecipes(t *testing.T) {
	for _, tc := range []struct {
		opts    TranscodeOpts
		wantErr string
	}{
		{TranscodeOpts{VideoBitstreamFilter: "arbitrary_filter=1", TargetCodecVideo: "copy"}, "unsupported video bitstream filter recipe"},
		{TranscodeOpts{VideoBitstreamFilter: DV7ToHDR10BitstreamFilter, TargetCodecVideo: "h264"}, "unsupported video bitstream filter recipe"},
		{TranscodeOpts{TargetCodecVideo: "copy", VideoSampleEntry: "dvhe"}, "unsupported video sample-entry recipe"},
		{TranscodeOpts{TargetCodecVideo: "h264", VideoSampleEntry: VideoSampleEntryDVH1}, "unsupported video sample-entry recipe"},
		// The range is written into the playlist verbatim.
		{TranscodeOpts{TargetCodecVideo: "copy", VideoRange: "PQ\n#EXT-X-ENDLIST"}, "unsupported HLS video range"},
		{TranscodeOpts{TargetCodecVideo: "h264", VideoRange: HLSVideoRangePQ}, "unsupported HLS video range"},
	} {
		if _, err := StartTranscode(context.Background(), tc.opts); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("StartTranscode(%+v) error = %v, want %q", tc.opts, err, tc.wantErr)
		}
	}
}

// TestValidateToneMapOptsAcceptsOnlyCompleteRecipes binds executable recipes
// to stable source facts and keeps VAAPI recipes to the two filters its probe
// can freeze.
func TestValidateToneMapOptsAcceptsOnlyCompleteRecipes(t *testing.T) {
	revision := tonemap.SourceRevision{MediaFileID: 1, FileSize: 1, StreamSignature: "stream"}
	recipe := func(hwAccel, filter string, revision tonemap.SourceRevision) TranscodeOpts {
		return TranscodeOpts{
			TargetCodecVideo: "h264", HWAccel: hwAccel, ToneMapPolicy: tonemap.PolicyHardwareOnly,
			ToneMapMode: tonemap.ModeHardware, ToneMapSourceKind: tonemap.SourcePQ, ToneMapFilter: filter,
			ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3, ToneMapSourceRevision: revision,
		}
	}
	for _, tc := range []struct {
		name   string
		opts   TranscodeOpts
		wantOK bool
	}{
		{"QSV without a frozen source revision", recipe("qsv", tonemap.HardwareFilterOpenCL, tonemap.SourceRevision{}), false},
		{"QSV with a frozen source revision", recipe("qsv", tonemap.HardwareFilterOpenCL, revision), true},
		{"VAAPI tonemap_vaapi", recipe(transcodeHWVAAPI, tonemap.HardwareFilterVAAPI, revision), true},
		{"VAAPI tonemap_opencl", recipe(transcodeHWVAAPI, tonemap.HardwareFilterOpenCL, revision), true},
		{"VAAPI tonemap_cuda", recipe(transcodeHWVAAPI, tonemap.HardwareFilterCUDA, revision), false},
		{"VideoToolbox", recipe(transcodeHWVideoToolbox, tonemap.HardwareFilterVideoToolbox, tonemap.SourceRevision{MediaFileID: 1, FileSize: 1, FileModifiedUnixNano: 1, FileHash: "hash", ProbeUpdatedUnixNano: 1, StreamSignature: "stream"}), true},
	} {
		if err := validateToneMapOpts(tc.opts); (err == nil) != tc.wantOK {
			t.Errorf("%s: err = %v, want ok %t", tc.name, err, tc.wantOK)
		}
	}
}
