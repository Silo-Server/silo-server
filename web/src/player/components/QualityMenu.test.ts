// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
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
