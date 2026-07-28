import { describe, expect, it } from "vitest";
import { SETTING_DEFINITIONS } from "./settingsContract";
import { describeQuality, presetById, presetIdFor, QUALITY_PRESETS } from "./qualityPresets";

describe("quality presets", () => {
  it("only composes values the contract accepts", () => {
    // The presets are a client-side convenience over two server settings. If one
    // named a resolution the enum does not have, or a bitrate outside the
    // declared bounds, picking it would 400 at the moment of saving.
    const resolutions = new Set(
      (SETTING_DEFINITIONS["playback.preferred_quality"].values ?? []).map(
        (member) => member.value,
      ),
    );
    const bitrate = SETTING_DEFINITIONS["playback.max_bitrate_kbps"];

    for (const preset of QUALITY_PRESETS) {
      expect(resolutions, `${preset.id} resolution`).toContain(preset.resolution);
      if (preset.bitrateKbps !== null) {
        expect(preset.bitrateKbps, `${preset.id} bitrate`).toBeGreaterThanOrEqual(
          bitrate.minimum ?? 0,
        );
        expect(preset.bitrateKbps, `${preset.id} bitrate`).toBeLessThanOrEqual(
          bitrate.maximum ?? Number.MAX_SAFE_INTEGER,
        );
      }
    }
  });

  it("has no duplicate ids or duplicate axis pairs", () => {
    const ids = new Set(QUALITY_PRESETS.map((preset) => preset.id));
    expect(ids.size).toBe(QUALITY_PRESETS.length);

    // Two presets resolving to the same pair would make the picker ambiguous:
    // whichever matched first would win on read and the other could never
    // display as selected.
    const pairs = new Set(
      QUALITY_PRESETS.map((preset) => `${preset.resolution}|${preset.bitrateKbps}`),
    );
    expect(pairs.size).toBe(QUALITY_PRESETS.length);
  });

  it("round-trips every preset through the stored pair", () => {
    for (const preset of QUALITY_PRESETS) {
      expect(presetIdFor(preset.resolution, preset.bitrateKbps)).toBe(preset.id);
      expect(presetById(preset.id)).toEqual(preset);
    }
  });

  it("treats a missing bitrate as uncapped rather than unmatched", () => {
    // The server omits a null value, so undefined and null both arrive here.
    expect(presetIdFor("auto", null)).toBe("auto");
    expect(presetIdFor("auto", undefined)).toBe("auto");
    expect(presetIdFor("2160p", undefined)).toBe("2160p");
  });

  it("describes combinations no preset covers", () => {
    // Reachable two ways: someone set the axes independently through the API, or
    // the migration decomposed a legacy value whose bitrate is not on this
    // ladder. Either way the picker has to say something true.
    expect(presetIdFor("1080p", 4500)).toBeNull();
    expect(describeQuality("1080p", 4500)).toBe("1080p at 4.5 Mbps");
    expect(describeQuality("720p", 3000)).toBe("720p at 3 Mbps");
    expect(describeQuality("2160p", 25000)).toBe("4K at 25 Mbps");
  });

  it("describes the sentinels without inventing a bitrate", () => {
    expect(describeQuality("auto", null)).toBe("Auto");
    expect(describeQuality("original", null)).toBe("Original");
    expect(describeQuality(null, null)).toBe("Auto");
  });

  it("covers the legacy ladder the migration decomposes", () => {
    // Every compound value the server used to accept now maps to a pair. These
    // are the pairs internal/settingsmigrate writes, so a user who had one of
    // them should open the picker and see a named preset, not "custom".
    for (const [resolution, bitrate, label] of [
      ["1080p", 10000, "1080p High"],
      ["1080p", 6000, "1080p"],
      ["720p", 4000, "720p High"],
      ["720p", 2000, "720p"],
      ["480p", 1500, "480p"],
    ] as const) {
      expect(describeQuality(resolution, bitrate)).toBe(label);
    }
  });
});
