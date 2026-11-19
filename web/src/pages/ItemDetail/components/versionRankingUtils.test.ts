// @vitest-environment node

import { describe, expect, it } from "vitest";
import { makeFileVersion as makeVersion } from "../itemDetail.test-support";
import { selectDefaultPlaybackVariantVersion } from "./versionRankingUtils";

describe("selectDefaultPlaybackVariantVersion with no history", () => {
  it("picks the standard cut over a longer cut in better quality", () => {
    const extended = makeVersion({
      file_id: 1,
      resolution: "2160p",
      hdr: true,
      edition_key: "extended",
      edition_raw: "EXTENDED CUT",
    });
    const standard = makeVersion({ file_id: 2, resolution: "1080p" });
    expect(selectDefaultPlaybackVariantVersion([extended, standard], undefined)).toBe(standard);
  });

  it("compares quality among cuts with no standard cut", () => {
    const directors = makeVersion({ file_id: 1, resolution: "1080p", edition_key: "directors" });
    const extended = makeVersion({ file_id: 2, resolution: "2160p", edition_key: "extended" });
    expect(selectDefaultPlaybackVariantVersion([directors, extended], undefined)).toBe(extended);
  });
});
