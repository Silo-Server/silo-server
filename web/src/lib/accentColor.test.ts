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
    for (const accent of ["#000000", "#4f46e5", "#64748B", "#00f"]) {
      expect(accentDeclarations(accent), accent).toMatchObject({
        primary: accent,
        "primary-foreground": "#ffffff",
        "sidebar-primary-foreground": "#ffffff",
      });
    }
  });

  it("leaves the labels alone for a value it cannot measure", () => {
    expect(accentDeclarations("rebeccapurple")).toEqual({
      primary: "rebeccapurple",
      ring: "rebeccapurple",
      "sidebar-primary": "rebeccapurple",
    });
  });

  it("rejects a value that could break out of the declaration", () => {
    expect(accentDeclarations("red;} body{display:none")).toBeNull();
    expect(accentDeclarations("red /* x")).toBeNull();
  });
});
