// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { createElement, type ComponentProps } from "react";
import { describe, expect, it, vi } from "vitest";
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
          {
            id: "original",
            label: "Original",
            sublabel: "",
            resolution: "1080p",
            bitrateKbps: 8000,
            isOriginal: true,
          },
          {
            id: "720p",
            label: "720p",
            sublabel: "3 Mbps",
            resolution: "720p",
            bitrateKbps: 3000,
            isOriginal: false,
          },
        ],
        activeId: "original",
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
    fireEvent.click(screen.getByRole("menuitem", { name: /Original/ }));
    expect(select).toHaveBeenCalledWith("original");
    expect(switchVersion).not.toHaveBeenCalled();
  });

  it("shows the stored resolution preference as the selected bitrate rung", () => {
    render(
      createElement(QualityMenu, {
        options: [
          {
            id: "original",
            label: "Original",
            sublabel: "25 Mbps",
            resolution: "2160p",
            bitrateKbps: 25_000,
            isOriginal: true,
          },
          {
            id: "1080p-medium",
            label: "1080p Medium",
            sublabel: "6 Mbps",
            resolution: "1080p",
            bitrateKbps: 6000,
            isOriginal: false,
          },
        ],
        activeId: "1080p",
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
});

const original = {
  id: "original",
  label: "Original",
  sublabel: "8 Mbps",
  resolution: "1080p",
  bitrateKbps: 8000,
  isOriginal: true,
};
const auto = {
  id: "auto",
  label: "Auto",
  sublabel: "",
  resolution: "",
  bitrateKbps: 0,
  isOriginal: false,
};
const versions = [
  makeVersionInfo({ isCurrentSource: true }),
  makeVersionInfo({ fileId: 2, label: "1080p H264" }),
];
function menuProps(
  overrides: Partial<ComponentProps<typeof QualityMenu>> = {},
): ComponentProps<typeof QualityMenu> {
  return {
    options: [original],
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
  it.each([{ options: [] }, { options: [original] }])(
    "keeps version switching without a quality choice (%j)",
    ({ options }) => {
      const props = menuProps({ options });
      render(createElement(QualityMenu, props));
      fireEvent.click(screen.getByRole("button", { name: "Version" }));
      expect(screen.queryByText("Quality")).not.toBeInTheDocument();
      expect(screen.queryByRole("menuitem", { name: /Original/ })).not.toBeInTheDocument();
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

  it("shows both sections when versions and qualities are available", () => {
    render(createElement(QualityMenu, menuProps({ options: [auto, original] })));
    fireEvent.click(screen.getByRole("button", { name: "Quality" }));
    expect(screen.getByText("Version")).toBeInTheDocument();
    expect(screen.getByText("Quality")).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /Auto.*Selected/ })).toHaveAttribute(
      "aria-current",
      "true",
    );
  });

  it("preserves Auto when playback changes from one quality to multiple qualities", () => {
    const props = menuProps();
    const { rerender } = render(createElement(QualityMenu, props));
    expect(screen.getByRole("button", { name: "Version" })).toHaveTextContent("Version");
    rerender(createElement(QualityMenu, { ...props, options: [auto, original] }));
    fireEvent.click(screen.getByRole("button", { name: "Quality" }));
    expect(screen.getByRole("menuitem", { name: /Auto.*Selected/ })).toBeInTheDocument();
    expect(props.onSelect).not.toHaveBeenCalled();
  });
});
