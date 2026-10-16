import { describe, expect, it } from "vitest";

import { accentDeclarations } from "./accentColor";

describe("accentDeclarations", () => {
  it("keeps Cinema Dark's dark labels on a light accent", () => {
    expect(accentDeclarations("#10b981")).toEqual({
      primary: "#10b981",
      ring: "#10b981",
      "sidebar-primary": "#10b981",
    });
  });

  it("turns the labels white on a dark accent", () => {
    const cases: [accent: string, hex: string][] = [
      ["#000000", "#000000"],
      ["#4f46e5", "#4f46e5"],
      ["#64748B", "#64748b"],
      ["#00f", "#0000ff"],
    ];
    for (const [accent, hex] of cases) {
      expect(accentDeclarations(accent), accent).toMatchObject({
        primary: hex,
        "primary-foreground": "#ffffff",
        "sidebar-primary-foreground": "#ffffff",
      });
    }
  });

  // Email buttons honor only hex too, and the admin picker only writes hex.
  it("ignores a value that is not a hex color", () => {
    expect(accentDeclarations("rebeccapurple")).toBeNull();
    expect(accentDeclarations("rgb(0, 0, 0)")).toBeNull();
    expect(accentDeclarations("red;} body{display:none")).toBeNull();
    expect(accentDeclarations("#12345")).toBeNull();
  });
});
