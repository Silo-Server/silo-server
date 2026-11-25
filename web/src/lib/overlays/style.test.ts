import { describe, expect, it } from "vitest";

import { OVERLAY_PRESETS } from "./presets";
import { applyOverlayStyle } from "./style";

const NONE = "#00000000";

describe("applyOverlayStyle with a see-through fill", () => {
  it.each([
    ["vibrant without an accent", "vibrant", undefined],
    ["vibrant with a light accent", "vibrant", "#f5c518"],
  ] as const)("draws white text on the artwork for %s", (_, id, accent) => {
    const preset = applyOverlayStyle(OVERLAY_PRESETS[id], { background: NONE });
    expect(preset.badgeStyle(accent).color).toBe("#ffffff");
  });

  it("lifts the text off the artwork with a shadow", () => {
    const preset = applyOverlayStyle(OVERLAY_PRESETS.vibrant, { background: NONE });
    expect(preset.textShadow).toBeDefined();
  });

  it("adds no shadow when the viewer turned shadows off", () => {
    const preset = applyOverlayStyle(OVERLAY_PRESETS.vibrant, { background: NONE, shadow: false });
    expect(preset.textShadow).toBeUndefined();
  });

  it("keeps black text and no shadow on a light solid fill", () => {
    const preset = applyOverlayStyle(OVERLAY_PRESETS.vibrant, { background: "#FFFFFFE6" });
    expect(preset.badgeStyle(undefined).color).toBe("#000000");
    expect(preset.textShadow).toBeUndefined();
  });
});
