import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { FileVersion } from "@/api/types";

import DownloadVersionPicker from "./DownloadVersionPicker";

vi.mock("@/hooks/queries/downloads", () => ({
  buildDirectDownloadUrl: (fileId: number) => `/api/downloads/files/${fileId}`,
}));

function version(overrides: Partial<FileVersion>): FileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    file_name: overrides.file_name,
    file_path: overrides.file_path,
    resolution: overrides.resolution ?? "",
    codec_video: overrides.codec_video ?? "",
    codec_audio: overrides.codec_audio ?? "",
    hdr: overrides.hdr ?? false,
    container: overrides.container ?? "",
    file_size: overrides.file_size ?? 0,
    duration: overrides.duration ?? 0,
    bitrate: overrides.bitrate ?? 0,
  };
}

describe("DownloadVersionPicker", () => {
  it("uses file-size copy instead of quality copy for multi-file downloads", () => {
    render(
      <DownloadVersionPicker
        open
        onOpenChange={() => undefined}
        title="A Psalm for the Wild-Built"
        versions={[
          version({ file_id: 1, container: "epub", file_size: 512 * 1024 }),
          version({ file_id: 2, container: "pdf", file_size: 2 * 1024 * 1024 }),
        ]}
      />,
    );

    expect(screen.getByText("Larger files require more storage space.")).toBeTruthy();
    expect(screen.queryByText("Higher quality files require more storage space.")).toBeNull();
  });

  it("describes download choices as files instead of versions", () => {
    render(
      <DownloadVersionPicker
        open
        onOpenChange={() => undefined}
        title="A Psalm for the Wild-Built"
        versions={[version({ file_id: 1, container: "epub", file_size: 512 * 1024 })]}
      />,
    );

    expect(
      screen.getByText("Choose a file to download. Make sure you have enough disk space."),
    ).toBeTruthy();
    expect(
      screen.queryByText("Choose a version to download. Make sure you have enough disk space."),
    ).toBeNull();
  });
});
