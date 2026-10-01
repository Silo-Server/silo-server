import { describe, expect, it } from "vitest";

import {
  EDGE_ZONE_PX,
  MAX_SCROLL_SPEED_PX,
  edgeScrollSpeed,
  findScrollContainer,
  scrollViewportBounds,
} from "./dragAutoScroll";

describe("edgeScrollSpeed", () => {
  const top = 0;
  const bottom = 600;

  it("is zero in the middle, outside both edge zones", () => {
    expect(edgeScrollSpeed(300, top, bottom)).toBe(0);
    expect(edgeScrollSpeed(EDGE_ZONE_PX, top, bottom)).toBe(0);
    expect(edgeScrollSpeed(bottom - EDGE_ZONE_PX, top, bottom)).toBe(0);
  });

  it("scrolls up (negative) near the top and down (positive) near the bottom", () => {
    expect(edgeScrollSpeed(10, top, bottom)).toBeLessThan(0);
    expect(edgeScrollSpeed(bottom - 10, top, bottom)).toBeGreaterThan(0);
  });

  it("ramps: the closer to the edge, the faster", () => {
    const far = edgeScrollSpeed(70, top, bottom);
    const near = edgeScrollSpeed(10, top, bottom);
    expect(Math.abs(near)).toBeGreaterThan(Math.abs(far));
    expect(Math.abs(far)).toBeGreaterThan(0);
  });

  it("caps at the max speed at and past the edge", () => {
    expect(edgeScrollSpeed(0, top, bottom)).toBe(-MAX_SCROLL_SPEED_PX);
    expect(edgeScrollSpeed(-50, top, bottom)).toBe(-MAX_SCROLL_SPEED_PX);
    expect(edgeScrollSpeed(bottom + 50, top, bottom)).toBe(MAX_SCROLL_SPEED_PX);
  });

  it("shrinks the zones on a short container so they never overlap", () => {
    // 120px tall: full 80px zones would cover everything; a third each leaves
    // a dead middle band.
    expect(edgeScrollSpeed(60, 0, 120)).toBe(0);
    expect(edgeScrollSpeed(5, 0, 120)).toBeLessThan(0);
    expect(edgeScrollSpeed(115, 0, 120)).toBeGreaterThan(0);
  });

  it("is zero for a degenerate (empty or inverted) view", () => {
    expect(edgeScrollSpeed(0, 100, 100)).toBe(0);
    expect(edgeScrollSpeed(0, 100, 50)).toBe(0);
  });
});

/** An element jsdom will report as vertically scrollable. */
function makeScrollable(el: HTMLElement) {
  el.style.overflowY = "auto";
  Object.defineProperty(el, "scrollHeight", { value: 1000, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: 200, configurable: true });
}

describe("findScrollContainer", () => {
  it("returns the nearest scrollable ancestor", () => {
    const outer = document.createElement("div");
    makeScrollable(outer);
    const middle = document.createElement("div");
    const inner = document.createElement("div");
    outer.appendChild(middle);
    middle.appendChild(inner);
    document.body.appendChild(outer);
    try {
      expect(findScrollContainer(inner)).toBe(outer);
    } finally {
      outer.remove();
    }
  });

  it("skips overflow-y: auto ancestors that have nothing to scroll", () => {
    const outer = document.createElement("div");
    outer.style.overflowY = "auto"; // scrollHeight === clientHeight === 0 in jsdom
    const inner = document.createElement("div");
    outer.appendChild(inner);
    document.body.appendChild(outer);
    try {
      expect(findScrollContainer(inner)).toBe(document.scrollingElement);
    } finally {
      outer.remove();
    }
  });

  it("falls back to the document's scrolling element", () => {
    const el = document.createElement("div");
    document.body.appendChild(el);
    try {
      expect(findScrollContainer(el)).toBe(document.scrollingElement);
    } finally {
      el.remove();
    }
    expect(findScrollContainer(null)).toBe(document.scrollingElement);
  });
});

describe("scrollViewportBounds", () => {
  it("uses the viewport for the document itself", () => {
    // jsdom leaves `document.scrollingElement` null, so the root element
    // stands in — the function treats both as "the page scrolls".
    expect(scrollViewportBounds(document.documentElement)).toEqual({
      top: 0,
      bottom: window.innerHeight,
    });
  });

  it("clips an inner container's rect to the viewport", () => {
    const el = document.createElement("div");
    el.getBoundingClientRect = () => ({ top: -50, bottom: window.innerHeight + 50 }) as DOMRect;
    expect(scrollViewportBounds(el)).toEqual({ top: 0, bottom: window.innerHeight });

    el.getBoundingClientRect = () => ({ top: 40, bottom: 400 }) as DOMRect;
    expect(scrollViewportBounds(el)).toEqual({ top: 40, bottom: 400 });
  });
});
