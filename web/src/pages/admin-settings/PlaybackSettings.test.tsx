import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import PlaybackSettings from "./PlaybackSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/queries/admin/system", () => ({
  useHWAccelDetection: () => ({ data: undefined, isLoading: false }),
}));

function makeForm(values: Record<string, string>) {
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    dirtyCount: 0,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
  };
}

function cpuToneMapSwitch(markup: string): Element {
  const container = document.createElement("div");
  container.innerHTML = markup;
  const label = Array.from(container.querySelectorAll("label")).find(
    (candidate) => candidate.textContent === "Enable CPU Tone Mapping",
  );
  const toggle = label?.htmlFor ? container.querySelector(`[id="${label.htmlFor}"]`) : null;
  if (!toggle) throw new Error("CPU tone-mapping toggle was not rendered");
  return toggle;
}

describe("PlaybackSettings CPU tone mapping", () => {
  it("includes the setting and renders it off by default", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.chapter_thumbnail_hdr_policy": "best_effort",
      }),
    );

    const toggle = cpuToneMapSwitch(renderToStaticMarkup(<PlaybackSettings />));

    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toContain(
      "playback.chapter_thumbnail_software_tone_map_enabled",
    );
    expect(toggle).toHaveAttribute("aria-checked", "false");
    expect(toggle).not.toHaveAttribute("disabled");
  });

  it("disables the toggle while HDR chapter thumbnails are disabled", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.chapter_thumbnail_hdr_policy": "disabled",
        "playback.chapter_thumbnail_software_tone_map_enabled": "true",
      }),
    );

    const toggle = cpuToneMapSwitch(renderToStaticMarkup(<PlaybackSettings />));

    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).toHaveAttribute("disabled");
  });
});
