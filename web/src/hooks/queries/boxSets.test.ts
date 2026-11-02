import { describe, expect, it } from "vitest";

import { boxSetKeys } from "./boxSets";
import { catalogKeys } from "./keys";

describe("boxSetKeys", () => {
  it("nests under the catalog keys so catalog invalidations refresh box sets", () => {
    for (const key of [
      boxSetKeys.all,
      boxSetKeys.search("harry"),
      boxSetKeys.detail("1241", "1"),
    ]) {
      expect(key.slice(0, catalogKeys.all.length)).toEqual([...catalogKeys.all]);
    }
  });
});
