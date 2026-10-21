import { describe, expect, it } from "vitest";

import { batchResult } from "./copy";

describe("select mode and Delete all wording", () => {
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
});
