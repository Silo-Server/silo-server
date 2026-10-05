import { describe, expect, it } from "vitest";

import {
  alsoDeletedElsewhere,
  batchResult,
  deleteCollectionsTitle,
  keptForRows,
  syncSkipNote,
} from "./copy";

describe("select mode and Delete all wording", () => {
  it("names what Sync passes over, or nothing when it passes over none", () => {
    expect(syncSkipNote(0, 0)).toBeNull();
    expect(syncSkipNote(0, 2)).toBe("Sync skips manual collections (2 here).");
    expect(syncSkipNote(1, 1)).toBe("Sync skips smart and manual collections (2 here).");
  });

  it("says when none of a batch went through", () => {
    expect(batchResult("hide", 0, 3)).toEqual({
      tone: "error",
      message: "Couldn't hide 3 collections.",
    });
    expect(batchResult("sync", 1, 1)).toEqual({ tone: "success", message: "Synced 1 list." });
  });

  it("says how many synced with warnings", () => {
    expect(batchResult("sync", 3, 3, 1)).toEqual({
      tone: "warning",
      message: "Synced 3 lists, 1 with warnings.",
    });
    expect(batchResult("sync", 2, 3, 2)).toEqual({
      tone: "warning",
      message: "Synced 2 of 3 lists, 2 with warnings.",
    });
  });

  it("titles a delete by type when they share one", () => {
    expect(deleteCollectionsTitle(7, "synced", "Movies")).toBe("Delete 7 synced lists in Movies?");
    expect(deleteCollectionsTitle(2, null, null)).toBe("Delete 2 collections?");
  });

  it("names several shared collections and where else they go", () => {
    expect(
      alsoDeletedElsewhere(
        [
          { title: "Netflix Originals", libraryNames: ["TV Shows"] },
          { title: "Studio Ghibli", libraryNames: ["Kids", "4K"] },
        ],
        null,
      ),
    ).toBe(
      "Collections that are also in other libraries go there too: Netflix Originals (TV Shows) and Studio Ghibli (Kids and 4K).",
    );
  });

  it("names up to five kept collections, then how many more", () => {
    expect(keptForRows(["A", "B", "C", "D", "E", "F", "G"])).toBe(
      "7 are kept because rows use them: A, B, C, D and 3 more.",
    );
    expect(keptForRows(["A", "B"])).toBe("2 are kept because rows use them: A and B.");
  });
});
