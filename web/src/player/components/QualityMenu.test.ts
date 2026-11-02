// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { createElement, type ComponentProps } from "react";
import { describe, expect, it, vi } from "vitest";
import { qualityOptionsFromPlanV3 } from "../playback-info";
import { fixturePlanV3 } from "../protocol-v3.fixtures";
import { buildVersionStatusLabels, QualityMenu, type VersionInfo } from "./QualityMenu";

function makeVersionInfo(overrides: Partial<VersionInfo> = {}): VersionInfo {
  return {
    fileId: overrides.fileId ?? 1,
    label: overrides.label ?? "2160p HEVC HDR",
    isCurrentSource: overrides.isCurrentSource ?? false,
    isRequestedSource: overrides.isRequestedSource ?? false,
  };
}

describe("buildVersionStatusLabels", () => {
  it("shows only Playing when requested and current source match", () => {
    expect(
      buildVersionStatusLabels(
        makeVersionInfo({
          isCurrentSource: true,
          isRequestedSource: true,
        }),
      ),
    ).toEqual(["Playing"]);
  });

  it("shows Playing and Requested on different versions", () => {
    expect(
      buildVersionStatusLabels(
        makeVersionInfo({
          isCurrentSource: true,
        }),
      ),
    ).toEqual(["Playing"]);

    expect(
      buildVersionStatusLabels(
        makeVersionInfo({
          fileId: 2,
          isRequestedSource: true,
        }),
      ),
    ).toEqual(["Requested"]);
  });
});

describe("QualityMenu", () => {
  it("keeps quality adjustments available while the room locks version switching", () => {
    const select = vi.fn();
    const switchVersion = vi.fn();
    render(
      createElement(QualityMenu, {
        options: [
          { id: "auto", label: "Auto", sublabel: "", resolution: "", bitrateKbps: 0 },
          {
            id: "720p",
            label: "720p",
            sublabel: "3 Mbps",
            resolution: "720p",
            bitrateKbps: 3000,
          },
        ],
        activeId: "auto",
        isTranscoding: false,
        error: null,
        onSelect: select,
        onSwitchVersion: switchVersion,
        versionLocked: true,
        versions: [makeVersionInfo(), makeVersionInfo({ fileId: 2, label: "1080p H264" })],
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Quality" }));
    expect(screen.getByText(/Watch Party keeps everyone on the same version/)).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /2160p HEVC HDR/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitem", { name: /720p/ }));
    expect(select).toHaveBeenCalledWith("720p");
    expect(switchVersion).not.toHaveBeenCalled();
  });

  it("lights the entry the plan delivers for a fixed preference", () => {
    render(
      createElement(QualityMenu, {
        options: [
          { id: "auto", label: "Auto", sublabel: "", resolution: "", bitrateKbps: 0 },
          {
            id: "1080p-medium",
            label: "1080p Medium",
            sublabel: "6 Mbps",
            resolution: "1080p",
            bitrateKbps: 6000,
          },
        ],
        activeId: "1080p",
        activeQuality: "1080p-medium",
        isTranscoding: false,
        error: null,
        onSelect: () => {},
      }),
    );

    expect(screen.getByRole("button", { name: "Quality" })).toHaveTextContent("1080p Medium");
    fireEvent.click(screen.getByRole("button", { name: "Quality" }));
    expect(screen.getByRole("menu")).toHaveClass("z-30");
    expect(screen.getByRole("menuitem", { name: /1080p Medium.*Selected/ })).toHaveAttribute(
      "aria-current",
      "true",
    );
  });

  it("names the saved resolution when the plan delivers no listed entry", () => {
    render(
      createElement(QualityMenu, {
        options: [
          { id: "auto", label: "Auto", sublabel: "", resolution: "", bitrateKbps: 0 },
          {
            id: "1080p-medium",
            label: "1080p Medium",
            sublabel: "6 Mbps",
            resolution: "1080p",
            bitrateKbps: 6000,
          },
        ],
        activeId: "1080p",
        isTranscoding: false,
        error: null,
        onSelect: () => {},
      }),
    );

    const trigger = screen.getByRole("button", { name: "Quality" });
    expect(trigger).toHaveTextContent(/^1080p$/);
    fireEvent.click(trigger);
    expect(screen.queryByText("Selected")).not.toBeInTheDocument();
  });

  it("never shows an original entry from the plan, and Auto is the way back to the source", () => {
    const select = vi.fn();
    const plan = fixturePlanV3({
      available_qualities: [
        { label: "original", height: 2160, bitrate_kbps: 40_000, preserves_source: true },
        { label: "1080p-medium", height: 1080, bitrate_kbps: 6000, preserves_source: false },
      ],
    });
    render(
      createElement(QualityMenu, {
        options: qualityOptionsFromPlanV3(plan),
        activeId: "1080p-medium",
        activeQuality: "1080p-medium",
        isTranscoding: false,
        error: null,
        onSelect: select,
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Quality" }));
    expect(screen.queryByRole("menuitem", { name: /Original/i })).not.toBeInTheDocument();
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "Auto",
      "1080p-medium6 MbpsSelected",
    ]);
    fireEvent.click(screen.getByRole("menuitem", { name: /Auto/ }));
    expect(select).toHaveBeenCalledWith("auto");
  });
});

const rung720 = {
  id: "720p",
  label: "720p",
  sublabel: "3 Mbps",
  resolution: "720p",
  bitrateKbps: 3000,
};
const auto = {
  id: "auto",
  label: "Auto",
  sublabel: "",
  resolution: "",
  bitrateKbps: 0,
};
const versions = [
  makeVersionInfo({ isCurrentSource: true }),
  makeVersionInfo({ fileId: 2, label: "1080p H264" }),
];
function menuProps(
  overrides: Partial<ComponentProps<typeof QualityMenu>> = {},
): ComponentProps<typeof QualityMenu> {
  return {
    options: [rung720],
    activeId: "auto",
    isTranscoding: false,
    error: null,
    onSelect: vi.fn(),
    versions,
    onSwitchVersion: vi.fn(),
    ...overrides,
  };
}

describe("available playback choices", () => {
  it.each([{ options: [] }, { options: [rung720] }])(
    "keeps version switching without a quality choice (%j)",
    ({ options }) => {
      const props = menuProps({ options });
      render(createElement(QualityMenu, props));
      fireEvent.click(screen.getByRole("button", { name: "Version" }));
      expect(screen.queryByText("Quality")).not.toBeInTheDocument();
      expect(screen.queryByRole("menuitem", { name: /720p/ })).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole("menuitem", { name: /1080p H264/ }));
      expect(props.onSwitchVersion).toHaveBeenCalledWith(2);
      expect(props.onSelect).not.toHaveBeenCalled();
    },
  );

  it.each([
    { versions: [makeVersionInfo()] },
    { versionLocked: true },
    { onSwitchVersion: undefined },
    { options: [], versions: [] },
  ])("hides the trigger when neither choice is actionable (%j)", (overrides) => {
    render(createElement(QualityMenu, menuProps(overrides)));
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("preserves Auto when playback changes from one quality to multiple qualities", () => {
    const props = menuProps();
    const { rerender } = render(createElement(QualityMenu, props));
    expect(screen.getByRole("button", { name: "Version" })).toHaveTextContent("Version");
    rerender(createElement(QualityMenu, { ...props, options: [auto, rung720] }));
    fireEvent.click(screen.getByRole("button", { name: "Quality" }));
    expect(screen.getByRole("menuitem", { name: /Auto.*Selected/ })).toBeInTheDocument();
    expect(props.onSelect).not.toHaveBeenCalled();
  });
});

describe("playback speed", () => {
  it("keeps the menu for speed alone and picks a speed without closing it", () => {
    const onPlaybackSpeedChange = vi.fn();
    render(
      createElement(
        QualityMenu,
        menuProps({ options: [rung720], versions: undefined, onPlaybackSpeedChange }),
      ),
    );
    const trigger = screen.getByRole("button", { name: "Playback settings" });

    fireEvent.click(trigger);
    expect(screen.getByRole("group", { name: "Speed" })).toBeInTheDocument();
    expect(screen.getAllByRole("menuitemradio")).toHaveLength(9);
    expect(screen.getByRole("menuitemradio", { name: "1×" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    fireEvent.click(screen.getByRole("menuitemradio", { name: "1.5×" }));
    expect(onPlaybackSpeedChange).toHaveBeenCalledExactlyOnceWith(1.5);
    // The menu stays open so the viewer sees the speed take effect.
    expect(screen.getByRole("menu")).toBeInTheDocument();
  });

  it("moves between speeds with the left and right arrow keys", () => {
    render(createElement(QualityMenu, menuProps({ onPlaybackSpeedChange: vi.fn() })));
    fireEvent.click(screen.getByRole("button", { name: "Playback settings" }));
    const half = screen.getByRole("menuitemradio", { name: "0.5×" });
    half.focus();
    fireEvent.keyDown(half, { key: "ArrowRight" });
    expect(screen.getByRole("menuitemradio", { name: "0.75×" })).toHaveFocus();
    fireEvent.keyDown(document.activeElement!, { key: "ArrowLeft" });
    expect(half).toHaveFocus();
  });

  it("keeps focus in the menu when the track is pressed between stops", () => {
    render(createElement(QualityMenu, menuProps({ onPlaybackSpeedChange: vi.fn() })));
    fireEvent.click(screen.getByRole("button", { name: "Playback settings" }));
    const track = screen.getByRole("menuitemradio", { name: "1×" }).parentElement!;
    expect(fireEvent.pointerDown(track, { button: 0 })).toBe(false);
    expect(
      fireEvent.pointerDown(screen.getByRole("menuitemradio", { name: "2×" }), { button: 0 }),
    ).toBe(true);
  });
});
