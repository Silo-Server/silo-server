import { describe, expect, it } from "vitest";
import { fixturePlanV3 } from "./protocol-v3.fixtures";
import { FEATURE_PLAN_QUALITY_REASONS_V3, type QualityReasonV3 } from "./protocol-v3";
import { autoQualityLabel, qualityReasonMenuLine, qualityReasonStatsLine } from "./quality-reasons";

const advertised = [FEATURE_PLAN_QUALITY_REASONS_V3];

describe("quality reasons", () => {
  it.each<{
    name: string;
    reasons?: QualityReasonV3[];
    features?: string[];
    frame?: { width?: number; height?: number };
    menu: string | null;
    stats: string | null;
    control: string;
  }>([
    {
      name: "the original",
      reasons: [{ code: "original" }],
      frame: { width: 1920, height: 1080 },
      menu: "Original quality",
      stats: "Original quality",
      control: "Auto · 1080p",
    },
    {
      name: "an estimate and the version it chose",
      reasons: [
        { code: "network_estimate", kbps: 5100 },
        { code: "alternate_version", media_file_id: "42" },
      ],
      frame: { width: 1280, height: 720 },
      menu: "Your connection measured 5.1 Mbps",
      stats: "Your connection measured 5.1 Mbps · Playing another version that fits",
      control: "Auto · 720p",
    },
    {
      name: "a codec conversion with its range change",
      reasons: [
        { code: "video_codec_unsupported", from: "hevc" },
        { code: "dynamic_range_converted", from: "dolby_vision", to: "sdr" },
      ],
      frame: { width: 3840, height: 1600 },
      menu: "Converted: this device can't play HEVC video · Dolby Vision shown as SDR",
      stats: "Converted: this device can't play HEVC video · Dolby Vision shown as SDR",
      control: "Auto · 4K",
    },
    {
      // Joined, the line would run past 72 characters.
      name: "a long second reason",
      reasons: [
        { code: "network_learned_limit", kbps: 3000, expires_at: "2026-10-23T12:00:00Z" },
        { code: "audio_converted", from: "truehd", to: "eac3" },
      ],
      frame: { width: 1920, height: 800 },
      menu: "Limited to 3 Mbps after buffering",
      stats: "Limited to 3 Mbps after buffering · TrueHD audio converted to Dolby Digital Plus",
      control: "Auto · 1080p",
    },
    {
      name: "caps and the device",
      reasons: [
        { code: "server_cap", kbps: 8000 },
        { code: "client_cap", kbps: 4500 },
        { code: "device_resolution_limit", height: 720 },
        { code: "user_selected" },
      ],
      menu: "Limited to 8 Mbps by the server",
      stats:
        "Limited to 8 Mbps by the server · Limited to 4.5 Mbps by this device's settings · This device plays up to 720p · Quality you picked",
      control: "Auto",
    },
    {
      name: "burn-in, packaging and unknown token names",
      reasons: [
        { code: "subtitle_burn_in" },
        { code: "container_unsupported" },
        { code: "video_codec_unsupported", from: "prores" },
      ],
      menu: "Subtitles burned into the video",
      stats:
        "Subtitles burned into the video · Repackaged for this device · Converted: this device can't play PRORES video",
      control: "Auto",
    },
    {
      name: "an unknown code",
      reasons: [
        { code: "compatibility_conversion" },
        { code: "audio_converted", from: "dts", to: "aac" },
      ],
      menu: "Adjusted for this device · DTS audio converted to AAC",
      stats: "compatibility_conversion · DTS audio converted to AAC",
      control: "Auto",
    },
    {
      name: "a known code missing its figure",
      reasons: [{ code: "network_estimate" }],
      menu: "Adjusted for this device",
      stats: "network_estimate",
      control: "Auto",
    },
    {
      name: "a server that does not advertise reasons",
      reasons: [{ code: "original" }],
      features: [],
      frame: { width: 1280, height: 720 },
      menu: null,
      stats: null,
      control: "Auto · 720p",
    },
  ])("$name", ({ reasons, features = advertised, frame = {}, menu, stats, control }) => {
    const plan = fixturePlanV3({
      quality_reasons: reasons,
      effective_recipe: { width: frame.width, height: frame.height },
    });
    plan.source = { ...plan.source, width: undefined, height: undefined };
    expect(qualityReasonMenuLine(plan, features)).toBe(menu);
    expect(qualityReasonStatsLine(plan, features)).toBe(stats);
    expect(autoQualityLabel(plan)).toBe(control);
  });
});
