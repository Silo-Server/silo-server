import { afterEach, describe, expect, it, vi } from "vitest";
import {
  hasRunningSidebarTransition,
  isCollapsedSidebarSurface,
  parseOptionalLibraryId,
  parseItemNavigationHref,
  SIDEBAR_TRANSITION_FALLBACK_MS,
  sidebarDetailsRevealDelay,
} from "./sidebarItemNavigation";

afterEach(() => vi.unstubAllGlobals());

describe("sidebar collapse completion", () => {
  it("accepts only the collapsed sidebar surface", () => {
    const surface = document.createElement("aside");
    surface.classList.add("sidebar-surface");

    expect(isCollapsedSidebarSurface(surface)).toBe(false);

    surface.dataset.collapsed = "true";
    expect(isCollapsedSidebarSurface(surface)).toBe(true);

    surface.classList.remove("sidebar-surface");
    expect(isCollapsedSidebarSurface(surface)).toBe(false);
  });

  it("returns false when the Web Animations API is unavailable", () => {
    const surface = document.createElement("aside");
    Object.defineProperty(surface, "getAnimations", { value: undefined });

    expect(hasRunningSidebarTransition(surface)).toBe(false);
  });

  it("ignores running animations that are not the transform transition", () => {
    class FakeTransition {
      playState = "running";
      constructor(readonly transitionProperty: string) {}
    }
    vi.stubGlobal("CSSTransition", FakeTransition);
    const surface = document.createElement("aside");
    Object.defineProperty(surface, "getAnimations", {
      value: () => [new FakeTransition("opacity"), { playState: "running" }],
    });

    expect(hasRunningSidebarTransition(surface)).toBe(false);
  });

  it("detects a running transform transition", () => {
    class FakeTransition {
      playState = "running";
      transitionProperty = "transform";
    }
    vi.stubGlobal("CSSTransition", FakeTransition);
    const surface = document.createElement("aside");
    Object.defineProperty(surface, "getAnimations", {
      value: () => [new FakeTransition()],
    });

    expect(hasRunningSidebarTransition(surface)).toBe(true);
  });
});

describe("sidebarDetailsRevealDelay", () => {
  it("uses the transition fallback for motion and reveals immediately for reduced motion", () => {
    expect(sidebarDetailsRevealDelay(false)).toBe(SIDEBAR_TRANSITION_FALLBACK_MS);
    expect(sidebarDetailsRevealDelay(true)).toBe(0);
  });
});

describe("parseItemNavigationHref", () => {
  it("parses an encoded item id and optional library id", () => {
    expect(
      parseItemNavigationHref("/item/movie%201%2Fpart?libraryId=12", "http://localhost:5173"),
    ).toEqual({ contentId: "movie 1/part", libraryId: 12 });
  });

  it("accepts an item without a library id", () => {
    expect(parseItemNavigationHref("/item/movie-1", "http://localhost:5173")).toEqual({
      contentId: "movie-1",
      libraryId: undefined,
    });
  });

  it("rejects non-item, cross-origin, and malformed destinations", () => {
    expect(parseItemNavigationHref("/library/1", "http://localhost:5173")).toBeNull();
    expect(
      parseItemNavigationHref("https://example.com/item/movie-1", "http://localhost:5173"),
    ).toBeNull();
    expect(parseItemNavigationHref("/item/%E0%A4%A", "http://localhost:5173")).toBeNull();
  });
});

describe("parseOptionalLibraryId", () => {
  it("accepts finite ids and rejects malformed ones", () => {
    expect(parseOptionalLibraryId("12")).toBe(12);
    expect(parseOptionalLibraryId("abc")).toBeUndefined();
    expect(parseOptionalLibraryId(null)).toBeUndefined();
  });
});
