import { describe, expect, it } from "vitest";

import { getOverlayDef } from "./registry";
import { ATTENTION_ACCENT, COMPLETE_ACCENT } from "./registry/ribbons";

describe("availability overlay", () => {
  const badge = getOverlayDef("availability");

  it("marks a complete poster with the check and a partial one with the attention accent", () => {
    const complete = { availability_have: 10, availability_total: 10 };
    const partial = { availability_have: 8, availability_total: 10 };
    expect(badge?.getIcon?.(complete)).toBe("check");
    expect(badge?.getAccent?.(complete)).toBe(COMPLETE_ACCENT);
    expect(badge?.getIcon?.(partial)).toBeNull();
    expect(badge?.getAccent?.(partial)).toBe(ATTENTION_ACCENT);
    expect(badge?.getValue(partial)).not.toBe(badge?.getValue(complete));
  });

  it("stays blank without counts", () => {
    for (const d of [{}, { availability_have: 0, availability_total: 0 }]) {
      expect(badge?.getValue(d)).toBeNull();
      expect(badge?.getAccent?.(d)).toBeNull();
    }
  });
});
