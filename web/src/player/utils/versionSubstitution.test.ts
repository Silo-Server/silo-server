import { describe, expect, it } from "vitest";
import {
  compareReleaseIdentity,
  resolveVersionSubstitution,
  type ReleaseIdentityRow,
} from "./versionSubstitution";

function row(overrides: Partial<ReleaseIdentityRow> = {}): ReleaseIdentityRow {
  return {
    file_id: 7,
    resolution: "1080p",
    codec_video: "h264",
    codec_audio: "aac",
    hdr: false,
    container: "mkv",
    file_size: 4_000_000_000,
    ...overrides,
  };
}

describe("compareReleaseIdentity", () => {
  it("treats two rows with the same release name as one release", () => {
    expect(
      compareReleaseIdentity(
        row({ file_id: 7, release_name: "Example.2024.1080p-GRP" }),
        row({ file_id: 8, release_name: "Example.2024.1080p-GRP" }),
      ),
    ).toBe("same");
  });

  it("treats different release names as different releases", () => {
    expect(
      compareReleaseIdentity(
        row({ file_id: 7, release_name: "Example.2024.1080p-GRP" }),
        row({ file_id: 8, release_name: "Example.2024.REPACK-GRP" }),
      ),
    ).toBe("different");
  });

  it("treats an identical byte count with the same encode as one release", () => {
    expect(compareReleaseIdentity(row({ file_id: 7 }), row({ file_id: 8 }))).toBe("same");
  });

  it("treats a different byte count as a different release", () => {
    expect(
      compareReleaseIdentity(row({ file_id: 7 }), row({ file_id: 8, file_size: 4_100_000_000 })),
    ).toBe("different");
  });

  it("treats the same byte count with a different encode as a different release", () => {
    expect(
      compareReleaseIdentity(row({ file_id: 7 }), row({ file_id: 8, codec_video: "hevc" })),
    ).toBe("different");
  });

  it("stays unknown when neither row carries a release identity", () => {
    expect(
      compareReleaseIdentity(
        row({ file_id: 7, file_size: 0, resolution: undefined }),
        row({ file_id: 8, file_size: 0, resolution: undefined }),
      ),
    ).toBe("unknown");
  });

  it("stays unknown for a neutral virtual row that names no release", () => {
    expect(
      compareReleaseIdentity(
        row({ file_id: 7, container: "virtual", file_path: "virtual://movie/tt1?results=all" }),
        row({ file_id: 8 }),
      ),
    ).toBe("unknown");
  });

  it("compares concrete virtual candidates by their own declared release", () => {
    expect(
      compareReleaseIdentity(
        row({ file_id: 7, container: "virtual", file_path: "virtual://movie/tt1?result=a" }),
        row({ file_id: 8, container: "virtual", file_path: "virtual://movie/tt1?result=b" }),
      ),
    ).toBe("same");
  });
});

describe("resolveVersionSubstitution", () => {
  const requested = row({ file_id: 7, release_name: "Example.2024.1080p-GRP" });

  it("does not claim a substitution when the identity did not move", () => {
    expect(
      resolveVersionSubstitution({
        requestedVersion: requested,
        effectiveVersion: requested,
        identitiesDiffer: false,
        substitutedFromFileId: 7,
      }),
    ).toEqual({ substituted: false, releaseMatch: "same" });
  });

  it("does not claim a substitution for a same-release sibling mirror", () => {
    expect(
      resolveVersionSubstitution({
        requestedVersion: requested,
        effectiveVersion: { ...requested, file_id: 8 },
        identitiesDiffer: true,
        substitutedFromFileId: 7,
      }),
    ).toEqual({ substituted: false, releaseMatch: "same" });
  });

  it("claims the substitution the server published for a different release", () => {
    expect(
      resolveVersionSubstitution({
        requestedVersion: requested,
        effectiveVersion: row({ file_id: 8, release_name: "Example.2024.REPACK-GRP" }),
        identitiesDiffer: true,
        substitutedFromFileId: 7,
      }),
    ).toEqual({ substituted: true, releaseMatch: "different" });
  });

  it("claims a different release the rows prove even without a server signal", () => {
    expect(
      resolveVersionSubstitution({
        requestedVersion: requested,
        effectiveVersion: row({ file_id: 8, file_size: 9_000_000_000 }),
        identitiesDiffer: true,
      }),
    ).toEqual({ substituted: true, releaseMatch: "different" });
  });

  it("makes no claim when the rows cannot identify a release", () => {
    expect(
      resolveVersionSubstitution({
        requestedVersion: row({ file_id: 7, file_size: 0 }),
        effectiveVersion: row({ file_id: 8, file_size: 0 }),
        identitiesDiffer: true,
      }),
    ).toEqual({ substituted: false, releaseMatch: "unknown" });
  });
});
