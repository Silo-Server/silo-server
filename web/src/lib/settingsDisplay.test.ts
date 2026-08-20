import { describe, expect, it } from "vitest";

import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "./settingsContract";
import {
  ALL_DEVICE_SETTING_KEYS,
  controlKindFor,
  defaultValueToString,
  deviceSettingKeysForRevision,
  formatSettingValue,
  getSettingDefinition,
  isStructuredSetting,
  optionsFor,
} from "./settingsDisplay";

describe("settingsDisplay", () => {
  it("derives the device-overridable keys from the contract rather than a parallel list", () => {
    // The hand-written registry this replaced had to be edited alongside the
    // manifest, and drifted: it declared several profile-scoped keys as device
    // overrides. Deriving the list means a key is offered as a device override
    // exactly when the manifest allows one.
    for (const key of ALL_DEVICE_SETTING_KEYS) {
      const definition = SETTING_DEFINITIONS[key];
      expect(definition.persistence).toBe("remote");
      expect(definition.scopes).toContain("profile_device");
    }

    const missed = (Object.keys(SETTING_DEFINITIONS) as SettingKey[]).filter(
      (key) =>
        SETTING_DEFINITIONS[key].persistence === "remote" &&
        SETTING_DEFINITIONS[key].scopes.includes("profile_device") &&
        !ALL_DEVICE_SETTING_KEYS.includes(key),
    );
    expect(missed).toEqual([]);
  });

  it("offers a device override for the skip preferences the player now resolves", () => {
    // Recaps and next-episode previews are declared at profile_device and read
    // by the player, so both must be editable as device overrides.
    expect(ALL_DEVICE_SETTING_KEYS).toContain(SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP);
    expect(ALL_DEVICE_SETTING_KEYS).toContain(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT_PREVIEW);
  });

  it("filters device batches to keys the connected server revision understands", () => {
    const revisionFour = deviceSettingKeysForRevision(4);
    expect(revisionFour).not.toContain(SETTING_KEYS.NAV_PRIMARY_MENU);
    expect(revisionFour).not.toContain(SETTING_KEYS.UI_CARD_PRESENTATION);
    expect(
      revisionFour.every((key) => {
        const definition = SETTING_DEFINITIONS[key];
        const scopeIndex = definition.scopes.indexOf("profile_device");
        return (definition.scopeIntroducedIn[scopeIndex] ?? Number.POSITIVE_INFINITY) <= 4;
      }),
    ).toBe(true);

    const revisionFive = deviceSettingKeysForRevision(5);
    expect(revisionFive).toContain(SETTING_KEYS.NAV_PRIMARY_MENU);
    expect(revisionFive).toContain(SETTING_KEYS.UI_CARD_PRESENTATION);
  });

  it("falls back to the value type when a definition names no control", () => {
    expect(controlKindFor(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS])).toBe(
      "switch",
    );
    // ui.disabled_library_ids declares no recommended control; an object is
    // never an inline widget.
    expect(isStructuredSetting(getSettingDefinition(SETTING_KEYS.UI_DISABLED_LIBRARY_IDS))).toBe(
      true,
    );
    // Subtitle appearance opens a bespoke panel rather than a select.
    expect(
      isStructuredSetting(getSettingDefinition(SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE)),
    ).toBe(true);
  });

  it("treats an unknown key as structured so it opens the raw editor", () => {
    expect(getSettingDefinition("playback.invented_by_a_client")).toBeNull();
    expect(isStructuredSetting(null)).toBe(true);
  });

  it("builds enum options from the manifest, adding an unset entry only when nullable", () => {
    expect(optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SUBTITLE_MODE])).toEqual([
      { value: "auto", label: "Auto" },
      { value: "always", label: "Always on" },
      { value: "off", label: "Off" },
    ]);
    // Date formats spell themselves, so the manifest leaves the label empty and
    // the value has to stand in rather than rendering a blank row.
    expect(optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.UI_DATE_FORMAT])).toContainEqual({
      value: "YYYY-MM-DD",
      label: "YYYY-MM-DD",
    });
  });

  it("renders a language tag through the shared language list", () => {
    expect(formatSettingValue(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, "ja")).toBe("Japanese");
    expect(formatSettingValue(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, "")).toBe("No preference");
  });

  it("renders booleans and enum members with their contract labels", () => {
    expect(formatSettingValue(SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO, "true")).toBe("Enabled");
    expect(formatSettingValue(SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, "always")).toBe("Always on");
  });

  it("passes an unknown key's value through rather than inventing a label", () => {
    expect(formatSettingValue("playback.invented_by_a_client", "17")).toBe("17");
    expect(formatSettingValue("playback.invented_by_a_client", undefined)).toBe("Unset");
  });

  it("stringifies defaults in the form the admin controls compare against", () => {
    expect(defaultValueToString(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT])).toBe(
      "true",
    );
    expect(defaultValueToString(SETTING_DEFINITIONS[SETTING_KEYS.PLAYER_AUDIO_SYNC_MS])).toBe("0");
    // A null default is "unset", not the string "null".
    expect(defaultValueToString(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE])).toBe(
      "",
    );
  });
});
