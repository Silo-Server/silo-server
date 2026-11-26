// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import type { FileVersion } from "@/api/types";
import VersionDropdown from "./VersionDropdown";

afterEach(cleanup);

function file(file_id: number, edition_raw?: string, resolution = "1080p"): FileVersion {
  return {
    file_id,
    resolution,
    codec_video: "h264",
    codec_audio: "aac",
    hdr: false,
    container: "mkv",
    file_size: 1,
    duration: 60,
    bitrate: 1,
    edition_raw,
    edition_key: edition_raw?.toLowerCase().replace(/\W+/g, "_"),
  };
}

function renderDropdown(versions: FileVersion[]) {
  return render(
    <VersionDropdown
      versions={versions}
      selectedVersion={versions[0] ?? null}
      onSelectVersion={() => {}}
    />,
  );
}

describe("VersionDropdown edition", () => {
  it.each([
    ["one file", [file(1, "Director's Cut")], false],
    ["two files", [file(1, "Director's Cut"), file(2, "Director's Cut", "720p")], true],
  ])("labels a film's only edition without a picker (%s)", (_name, versions, versionPicker) => {
    renderDropdown(versions);
    expect(screen.getByText("Director's Cut")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Edition/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Version/ }) !== null).toBe(versionPicker);
  });

  it("shows nothing for a single file without an edition", () => {
    const { container } = renderDropdown([file(1)]);
    expect(container.textContent).toBe("");
  });

  it("keeps the picker when a film has two editions", () => {
    renderDropdown([file(1), file(2, "Extended")]);
    expect(screen.getByRole("button", { name: /Edition/ })).toBeTruthy();
  });
});
