import { describe, expect, it } from "vitest";
import { fixturePlanV3 } from "./protocol-v3.fixtures";
import { FEATURE_PLAN_QUALITY_REASONS_V3, type QualityReasonV3 } from "./protocol-v3";
import { autoQualityLabel, qualityReasonMenuLine, qualityReasonStatsLine } from "./quality-reasons";

const advertised = [FEATURE_PLAN_QUALITY_REASONS_V3];

function planWith(reasons?: QualityReasonV3[], frame: { width?: number; height?: number } = {}) {
  const plan = fixturePlanV3({
    quality_reasons: reasons,
    effective_recipe: { width: frame.width, height: frame.height },
  });
  plan.source = { ...plan.source, width: undefined, height: undefined };
  return plan;
}

const dtsToDolbyDigital: QualityReasonV3 = { code: "audio_converted", from: "dts", to: "ac3" };

describe("quality reasons", () => {
  it.each<{
    name: string;
    reasons?: QualityReasonV3[];
    features?: string[];
    menu: string | null;
    stats: string | null;
  }>([
    {
      name: "a second reason the viewer cannot see stays out of the menu",
      reasons: [
        { code: "network_estimate", kbps: 5100 },
        { code: "alternate_version", media_file_id: "42" },
      ],
      menu: "Your connection measured 5.1 Mbps",
      stats: "Your connection measured 5.1 Mbps · Playing another version that fits",
    },
    {
      // Joined, the line is exactly 72 characters.
      name: "a joined line at the limit",
      reasons: [{ code: "network_learned_limit", kbps: 3000 }, dtsToDolbyDigital],
      menu: "Limited to 3 Mbps after buffering · DTS audio converted to Dolby Digital",
      stats: "Limited to 3 Mbps after buffering · DTS audio converted to Dolby Digital",
    },
    {
      // Joined, the line would be 73 characters.
      name: "a joined line one past the limit",
      reasons: [{ code: "network_learned_limit", kbps: 30000 }, dtsToDolbyDigital],
      menu: "Limited to 30 Mbps after buffering",
      stats: "Limited to 30 Mbps after buffering · DTS audio converted to Dolby Digital",
    },
    {
      name: "a same-codec downmix",
      reasons: [
        { code: "original" },
        { code: "audio_converted", from: "aac", to: "aac", channels: 2 },
      ],
      menu: "Original quality · Audio downmixed to stereo",
      stats: "Original quality · Audio downmixed to stereo",
    },
    {
      name: "a codec change that also drops channels",
      reasons: [
        { code: "compatibility_conversion" },
        { code: "audio_converted", from: "truehd", to: "aac", channels: 2 },
      ],
      menu: "Converted for this device · TrueHD audio converted to AAC",
      stats: "Converted for this device · TrueHD audio converted to AAC",
    },
    {
      name: "unknown codec and channel figures",
      reasons: [
        { code: "video_codec_unsupported", from: "prores" },
        { code: "audio_converted", from: "opus", to: "opus", channels: 4 },
      ],
      menu: "Converted: this device can't play PRORES video",
      stats: "Converted: this device can't play PRORES video · Audio downmixed to 4 channels",
    },
    {
      name: "an unknown code",
      reasons: [{ code: "future_reason" }, { code: "audio_converted", from: "dts", to: "aac" }],
      menu: "Adjusted for this device · DTS audio converted to AAC",
      stats: "future_reason · DTS audio converted to AAC",
    },
    {
      name: "known codes missing their figures",
      reasons: [{ code: "network_estimate" }, { code: "audio_converted", from: "aac", to: "aac" }],
      menu: "Adjusted for this device",
      stats: "network_estimate · audio_converted",
    },
    { name: "an empty list", reasons: [], menu: null, stats: null },
    {
      name: "a server that does not advertise reasons",
      reasons: [{ code: "original" }],
      features: [],
      menu: null,
      stats: null,
    },
  ])("$name", ({ reasons, features = advertised, menu, stats }) => {
    const plan = planWith(reasons);
    expect(qualityReasonMenuLine(plan, features)).toBe(menu);
    expect(qualityReasonStatsLine(plan, features)).toBe(stats);
  });

  // Either dimension earns a class, so scope frames keep theirs.
  it.each<{ width?: number; height?: number; control: string }>([
    { width: 1920, height: 800, control: "Auto · 1080p" },
    { width: 3840, height: 1600, control: "Auto · 4K" },
    { width: 1280, height: 536, control: "Auto · 720p" },
    { control: "Auto" },
  ])("names $width x $height as $control", ({ width, height, control }) => {
    expect(autoQualityLabel(planWith(undefined, { width, height }))).toBe(control);
  });
});
