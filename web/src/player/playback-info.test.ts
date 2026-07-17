import { describe, expect, it } from "vitest";
import {
  buildPlaybackInfoSections,
  deriveDisplayedPlaybackState,
  formatProtocol,
  formatStreamType,
} from "./playback-info";
import type { PlaybackSessionPlaybackInfo, PlayerFileVersion } from "./types";

function makeVersion(overrides: Partial<PlayerFileVersion> = {}): PlayerFileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    file_name: overrides.file_name ?? "Movie.2160p.mkv",
    resolution: overrides.resolution ?? "2160p",
    codec_video: overrides.codec_video ?? "hevc",
    codec_audio: overrides.codec_audio ?? "eac3",
    hdr: overrides.hdr ?? true,
    container: overrides.container ?? "mkv",
    file_size: overrides.file_size ?? 7.1 * 1024 ** 3,
    duration: overrides.duration ?? 7200,
    bitrate: overrides.bitrate ?? 22500,
    audio_channels: overrides.audio_channels ?? 6,
    video_tracks: overrides.video_tracks ?? [
      {
        codec: "hevc",
        profile: "Main 10",
        width: 3840,
        height: 2160,
        bitrate: 21900,
        video_range: "HDR10",
        dolby_vision: "Profile 8.1",
      },
    ],
    audio_tracks: overrides.audio_tracks ?? [
      {
        title: "EAC3 Dolby Digital Plus + Dolby Atmos",
        codec: "eac3",
        channels: 6,
        bitrate: 640,
        sample_rate: 48000,
        default: true,
      },
    ],
  };
}

function rowValue(
  sections: ReturnType<typeof buildPlaybackInfoSections>,
  sectionTitle: string,
  label: string,
): string {
  const section = sections.find((item) => item.title === sectionTitle);
  if (!section) {
    throw new Error(`section ${sectionTitle} not found`);
  }
  const row = section.rows.find((item) => item.label === label);
  if (!row) {
    throw new Error(`row ${label} not found`);
  }
  return row.value;
}

describe("playback info helpers", () => {
  it("formats a direct-play session with source metadata and live runtime stats", () => {
    const playbackInfo: PlaybackSessionPlaybackInfo = {
      stream_type: "progressive",
      transcode_audio: false,
      video_codec: "hevc",
      audio_codec: "aac",
    };

    const sections = buildPlaybackInfoSections({
      streamUrl: "https://app.example.com/api/v1/stream/abc123",
      playMethod: "direct",
      playbackInfo,
      currentSourceVersion: makeVersion(),
      runtimeStats: {
        playerWidth: 2560,
        playerHeight: 1277,
        videoWidth: 3840,
        videoHeight: 2160,
        droppedFrames: 0,
        corruptedFrames: 0,
      },
    });

    expect(rowValue(sections, "Player", "Protocol")).toBe("https");
    expect(rowValue(sections, "Player", "Play method")).toBe("Direct Play");
    expect(rowValue(sections, "Video Info", "Player dimensions")).toBe("2560x1277");
    expect(rowValue(sections, "Video Info", "Video resolution")).toBe("3840x2160");
    expect(rowValue(sections, "Playback Stream Info", "Video codec")).toBe("HEVC (direct)");
    expect(rowValue(sections, "Current Source File", "Size")).toBe("7.1 GiB");
    expect(rowValue(sections, "Current Source File", "Bitrate")).toBe("22.5 Mbps");
    expect(rowValue(sections, "Current Source File", "Video codec")).toBe("HEVC Main 10");
    expect(rowValue(sections, "Current Source File", "Video range type")).toBe(
      "Dolby Vision Profile 8.1 (HDR10)",
    );
    expect(rowValue(sections, "Current Source File", "Audio codec")).toBe(
      "EAC3 Dolby Digital Plus + Dolby Atmos",
    );
    expect(rowValue(sections, "Current Source File", "Audio bitrate")).toBe("640 kbps");
    expect(rowValue(sections, "Current Source File", "Audio channels")).toBe("6");
    expect(rowValue(sections, "Current Source File", "Audio sample rate")).toBe("48,000 Hz");
  });

  it("marks remuxed audio as transcoded while keeping video copied", () => {
    const sections = buildPlaybackInfoSections({
      streamUrl: "https://app.example.com/api/v1/stream/remux/abc123",
      playMethod: "remux",
      playbackInfo: {
        stream_type: "progressive",
        transcode_audio: true,
        video_codec: "h264",
        audio_codec: "aac",
      },
      currentSourceVersion: makeVersion({
        codec_video: "h264",
        codec_audio: "dts",
        hdr: false,
      }),
      runtimeStats: {},
    });

    expect(rowValue(sections, "Player", "Play method")).toBe("Direct Streaming");
    expect(rowValue(sections, "Playback Stream Info", "Video codec")).toBe("H.264 (copy)");
    expect(rowValue(sections, "Playback Stream Info", "Audio codec")).toBe("AAC (transcoded)");
  });

  it("shows explicit unavailable placeholders when metadata is missing", () => {
    const sections = buildPlaybackInfoSections({
      streamUrl: "/api/v1/stream/test",
      playMethod: "direct",
      playbackInfo: null,
      currentSourceVersion: makeVersion({
        container: "",
        bitrate: 0,
        file_size: 0,
        video_tracks: [],
        audio_tracks: [],
        audio_channels: 0,
      }),
      runtimeStats: {},
    });

    expect(rowValue(sections, "Video Info", "Player dimensions")).toBe("—");
    expect(rowValue(sections, "Current Source File", "Container")).toBe("—");
    expect(rowValue(sections, "Current Source File", "Size")).toBe("—");
    expect(rowValue(sections, "Current Source File", "Audio sample rate")).toBe("—");
  });

  it("shows the requested source when playback auto-switches to a lower version", () => {
    const sections = buildPlaybackInfoSections({
      streamUrl: "https://app.example.com/api/v1/playback/transcode/session/master.m3u8",
      playMethod: "transcode",
      playbackInfo: {
        stream_type: "hls",
        transcode_audio: true,
        video_codec: "h264",
        audio_codec: "aac",
      },
      currentSourceVersion: makeVersion({
        file_id: 2,
        resolution: "1080p",
        codec_video: "h264",
        hdr: false,
        file_name: "Movie.1080p.mkv",
        video_tracks: [{ codec: "h264", profile: "High", width: 1920, height: 1080 }],
      }),
      requestedVersion: makeVersion(),
      runtimeStats: {},
    });

    // The default fixture is a Dolby Vision (Profile 8.1) file, so the range
    // badge reads "DV" instead of the old generic boolean-derived "HDR".
    expect(rowValue(sections, "Player", "Auto-switched from")).toBe("2160p HEVC DV");
    expect(rowValue(sections, "Current Source File", "Video codec")).toBe("H.264 High");
  });

  it("detects HLS from both metadata and manifest URLs", () => {
    expect(
      formatStreamType(
        {
          stream_type: "hls",
          transcode_audio: false,
          video_codec: "h264",
          audio_codec: "aac",
        },
        "https://app.example.com/master.m3u8",
      ),
    ).toBe("HLS");
    expect(formatProtocol("https://app.example.com/master.m3u8")).toBe("https");
  });

  it("preserves remux semantics when original quality is delivered over HLS", () => {
    const displayed = deriveDisplayedPlaybackState({
      playMethod: "remux",
      playbackInfo: {
        stream_type: "progressive",
        transcode_audio: true,
        video_codec: "h264",
        audio_codec: "aac",
      },
      selectedVersion: makeVersion({
        codec_video: "h264",
        codec_audio: "dts",
        hdr: false,
        video_tracks: [{ codec: "h264", profile: "High" }],
        audio_tracks: [{ codec: "dts", channels: 6, default: true }],
      }),
      transcodeStreamUrl: "https://app.example.com/api/v1/playback/transcode/session/master.m3u8",
      activeQualityId: "original",
    });

    expect(displayed.playMethod).toBe("remux");
    expect(displayed.playbackInfo).toEqual({
      stream_type: "hls",
      transcode_audio: true,
      video_codec: "h264",
      audio_codec: "aac",
    });
  });

  it("keeps transcode semantics when transcode-base session switches to original quality", () => {
    const displayed = deriveDisplayedPlaybackState({
      playMethod: "transcode",
      playbackInfo: {
        stream_type: "hls",
        transcode_audio: true,
        video_codec: "h264",
        audio_codec: "aac",
      },
      selectedVersion: makeVersion({
        codec_video: "h264",
        codec_audio: "eac3",
        hdr: false,
        video_tracks: [{ codec: "h264", profile: "High" }],
        audio_tracks: [{ codec: "eac3", channels: 6, default: true }],
      }),
      transcodeStreamUrl: "https://app.example.com/api/v1/playback/transcode/session/master.m3u8",
      activeQualityId: "original",
    });

    expect(displayed.playMethod).toBe("transcode");
    expect(displayed.playbackInfo).toEqual({
      stream_type: "hls",
      transcode_audio: true,
      video_codec: "h264",
      audio_codec: "aac",
    });
  });

  it("shows full transcode semantics when quality switching creates an HLS transcode", () => {
    const displayed = deriveDisplayedPlaybackState({
      playMethod: "direct",
      playbackInfo: {
        stream_type: "progressive",
        transcode_audio: false,
        video_codec: "h264",
        audio_codec: "aac",
      },
      selectedVersion: makeVersion({
        codec_video: "h264",
        codec_audio: "aac",
        hdr: false,
      }),
      transcodeStreamUrl: "https://app.example.com/api/v1/playback/transcode/session/master.m3u8",
      activeQualityId: "720p",
    });

    expect(displayed.playMethod).toBe("transcode");
    expect(displayed.playbackInfo).toEqual({
      stream_type: "hls",
      transcode_audio: true,
      video_codec: "h264",
      audio_codec: "aac",
    });
  });
});
