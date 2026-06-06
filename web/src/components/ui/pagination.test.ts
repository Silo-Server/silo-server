import { describe, expect, it } from "vitest";

import { pageWindow } from "./pagination.utils";

describe("pageWindow", () => {
  it("lists every page without ellipsis when the count is small", () => {
    expect(pageWindow(0, 5)).toEqual([0, 1, 2, 3, 4]);
    expect(pageWindow(3, 7)).toEqual([0, 1, 2, 3, 4, 5, 6]);
  });

  it("anchors first/last and the current neighbours in the middle", () => {
    expect(pageWindow(10, 20)).toEqual([0, "ellipsis", 9, 10, 11, "ellipsis", 19]);
  });

  it("collapses only the trailing gap near the start", () => {
    expect(pageWindow(0, 20)).toEqual([0, 1, "ellipsis", 19]);
    expect(pageWindow(1, 20)).toEqual([0, 1, 2, "ellipsis", 19]);
  });

  it("collapses only the leading gap near the end", () => {
    expect(pageWindow(19, 20)).toEqual([0, "ellipsis", 18, 19]);
    expect(pageWindow(18, 20)).toEqual([0, "ellipsis", 17, 18, 19]);
  });

  it("never emits duplicate or out-of-range indices", () => {
    for (let count = 1; count <= 30; count++) {
      for (let current = 0; current < count; current++) {
        const window = pageWindow(current, count);
        const pages = window.filter((entry): entry is number => entry !== "ellipsis");
        // Strictly increasing, in-range, and always includes the current page.
        expect(pages).toEqual([...pages].sort((a, b) => a - b));
        expect(new Set(pages).size).toBe(pages.length);
        expect(pages.every((p) => p >= 0 && p < count)).toBe(true);
        expect(pages).toContain(current);
        expect(pages).toContain(0);
        expect(pages).toContain(count - 1);
      }
    }
  });
});
