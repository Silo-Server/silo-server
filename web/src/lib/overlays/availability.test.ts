import { describe, expect, it } from "vitest";

import { getOverlayDef } from "./registry";
import { overlayDataFromSeason } from "./extractors";

describe("availability overlay", () => {
  const badge = getOverlayDef("availability");

  it("shows a season fraction and Complete when nothing is missing", () => {
    expect(badge?.getValue({ availability_have: 8, availability_total: 10 })).toBe("8/10");
    expect(badge?.getValue({ availability_have: 10, availability_total: 10 })).toBe("Complete");
    expect(badge?.getIcon?.({ availability_have: 8, availability_total: 10 })).toBeNull();
    expect(badge?.getIcon?.({ availability_have: 10, availability_total: 10 })).toBe("check");
  });

  it("reads a season poster payload", () => {
    expect(overlayDataFromSeason({ availability: { have: 2, total: 3 } })).toMatchObject({
      availability_have: 2,
      availability_total: 3,
    });
  });
});
