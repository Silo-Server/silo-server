import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import PlaybackSettings from "./PlaybackSettings";

const useSettingsFormMock = vi.fn();
const useHWAccelDetectionMock = vi.fn();

beforeEach(() => {
  useSettingsFormMock.mockClear();
  useHWAccelDetectionMock.mockReturnValue({ data: undefined, isLoading: false });
});

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/queries/admin/system", () => ({
  useHWAccelDetection: (...args: unknown[]) => useHWAccelDetectionMock(...args),
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

function settingSwitch(markup: string, labelText: string): Element {
  const container = document.createElement("div");
  container.innerHTML = markup;
  const label = Array.from(container.querySelectorAll("label")).find(
    (candidate) => candidate.textContent === labelText,
  );
  const toggle = label?.htmlFor ? container.querySelector(`[id="${label.htmlFor}"]`) : null;
  if (!toggle) throw new Error(`${labelText} toggle was not rendered`);
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

    const toggle = settingSwitch(
      renderToStaticMarkup(<PlaybackSettings />),
      "Enable CPU Tone Mapping",
    );

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

    const toggle = settingSwitch(
      renderToStaticMarkup(<PlaybackSettings />),
      "Enable CPU Tone Mapping",
    );

    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).toHaveAttribute("disabled");
  });
});

describe("PlaybackSettings transcode tone mapping", () => {
  it("registers independent hardware and software settings disabled by default", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "auto" }));

    const markup = renderToStaticMarkup(<PlaybackSettings />);
    const keys = useSettingsFormMock.mock.calls[0]?.[0]?.keys as string[];

    expect(keys).toContain("playback.transcode_hardware_tone_map_enabled");
    expect(keys).toContain("playback.transcode_software_tone_map_enabled");
    expect(settingSwitch(markup, "Enable Hardware HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(settingSwitch(markup, "Enable Software HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("keeps the two policies independent", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "qsv",
        "playback.transcode_hardware_tone_map_enabled": "true",
        "playback.transcode_software_tone_map_enabled": "false",
      }),
    );

    const markup = renderToStaticMarkup(<PlaybackSettings />);

    expect(settingSwitch(markup, "Enable Hardware HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(settingSwitch(markup, "Enable Software HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("keeps hardware tone mapping configurable for remote executors when local acceleration is off", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.transcode_hardware_tone_map_enabled": "true",
      }),
    );

    const toggle = settingSwitch(
      renderToStaticMarkup(<PlaybackSettings />),
      "Enable Hardware HDR Tone Mapping",
    );

    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).not.toHaveAttribute("disabled");
  });

  it("keeps hardware tone mapping configurable when one detected executor is software-only", () => {
    useHWAccelDetectionMock.mockReturnValue({
      data: {
        resolved: "none",
        nodes: [
          { node_url: "http://software-node", resolved: "none" },
          { node_url: "http://gpu-node", resolved: "qsv" },
        ],
      },
      isLoading: false,
    });
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "auto",
        "playback.transcode_hardware_tone_map_enabled": "true",
      }),
    );

    const toggle = settingSwitch(
      renderToStaticMarkup(<PlaybackSettings />),
      "Enable Hardware HDR Tone Mapping",
    );

    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).not.toHaveAttribute("disabled");
  });
});
