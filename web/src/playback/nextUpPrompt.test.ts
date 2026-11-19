import { describe, expect, it } from "vitest";
import type { EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { isInNextUpPromptWindow, resolveNextUp } from "./nextUpPrompt";

describe("isInNextUpPromptWindow", () => {
  it.each([
    { at: 1290, duration: 1320, seconds: 30, expected: true, why: "exactly N seconds left" },
    { at: 1289, duration: 1320, seconds: 30, expected: false, why: "more than N seconds left" },
    { at: 1319.9, duration: 1320, seconds: 0, expected: false, why: "At end waits for ended" },
    { at: 0, duration: 20, seconds: 30, expected: false, why: "not yet started" },
    { at: 10, duration: 0, seconds: 30, expected: false, why: "duration unknown" },
  ])("$why", ({ at, duration, seconds, expected }) => {
    expect(isInNextUpPromptWindow(at, duration, seconds)).toBe(expected);
  });
});

function settings(values: { enabled?: boolean; seconds?: number }): EffectiveSettingsMap {
  const map: EffectiveSettingsMap = {};
  if (values.enabled !== undefined) {
    map[SETTING_KEYS.PLAYBACK_NEXT_UP_ENABLED] = {
      key: SETTING_KEYS.PLAYBACK_NEXT_UP_ENABLED,
      value: values.enabled,
      source: "profile",
    } as EffectiveSettingsMap[typeof SETTING_KEYS.PLAYBACK_NEXT_UP_ENABLED];
  }
  if (values.seconds !== undefined) {
    map[SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS] = {
      key: SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS,
      value: values.seconds,
      source: "profile",
    } as EffectiveSettingsMap[typeof SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS];
  }
  return map;
}

describe("resolveNextUp", () => {
  it.each([
    {
      why: "nothing opens early before the settings load",
      input: { settings: undefined, readFailed: false, shuffled: false },
      expected: { promptSeconds: 0, atEnd: "post-roll" },
    },
    {
      why: "a failed read falls back to the contract defaults",
      input: { settings: undefined, readFailed: true, shuffled: false },
      expected: { promptSeconds: 30, atEnd: "post-roll" },
    },
    {
      why: "the viewer's lead time applies while Next Up is on",
      input: {
        settings: settings({ enabled: true, seconds: 60 }),
        readFailed: false,
        shuffled: false,
      },
      expected: { promptSeconds: 60, atEnd: "post-roll" },
    },
    {
      why: "Off never opens early and closes at the end",
      input: {
        settings: settings({ enabled: false, seconds: 60 }),
        readFailed: false,
        shuffled: false,
      },
      expected: { promptSeconds: 0, atEnd: "close" },
    },
    {
      why: "a shuffle keeps continuing with Next Up off",
      input: {
        settings: settings({ enabled: false, seconds: 60 }),
        readFailed: false,
        shuffled: true,
      },
      expected: { promptSeconds: 60, atEnd: "post-roll" },
    },
    {
      why: "a server without the key keeps Next Up on",
      input: { settings: settings({ seconds: 10 }), readFailed: false, shuffled: false },
      expected: { promptSeconds: 10, atEnd: "post-roll" },
    },
  ])("$why", ({ input, expected }) => {
    expect(resolveNextUp(input)).toEqual(expected);
  });
});
