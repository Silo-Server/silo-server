import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import bootSource from "./themeBoot.js?raw";
import indexHtml from "../index.html?raw";
import { storage } from "@/utils/storage";

const mocks = vi.hoisted(() => ({
  useOptionalAuth: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => mocks.useOptionalAuth(),
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: () => ({ data: undefined }),
  useSetSettingValue: () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  useClearSettingValue: () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
}));

import { ThemeProvider } from "@/hooks/useTheme";

const KEYS = storage.KEYS;
const ATTRIBUTES = ["data-text-scale", "data-text-weight", "data-high-contrast"];

function clearAttributes(): void {
  for (const name of ATTRIBUTES) document.documentElement.removeAttribute(name);
}

function readAttributes(): Record<string, string | null> {
  return Object.fromEntries(
    ATTRIBUTES.map((name) => [name, document.documentElement.getAttribute(name)]),
  );
}

/** What the boot script paints before any app code has run. */
function bootAttributes(): Record<string, string | null> {
  clearAttributes();
  new Function(bootSource)();
  return readAttributes();
}

/** What ThemeProvider applies on mount while auth is still resolving. */
function providerAttributes(): Record<string, string | null> {
  clearAttributes();
  const { unmount } = render(
    <QueryClientProvider client={new QueryClient()}>
      <ThemeProvider>
        <span />
      </ThemeProvider>
    </QueryClientProvider>,
  );
  const attributes = readAttributes();
  unmount();
  return attributes;
}

function seed(values: Record<string, string | null>): void {
  localStorage.clear();
  for (const [key, value] of Object.entries(values)) {
    if (value !== null) localStorage.setItem(key, value);
  }
}

describe("themeBoot", () => {
  beforeEach(() => {
    localStorage.clear();
    // A cold start: the first frame paints before auth has resolved.
    mocks.useOptionalAuth.mockReturnValue({ loading: true, user: null, profile: null });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    localStorage.clear();
    clearAttributes();
  });

  // Under the last identity's namespace and under the pre-sign-in device
  // namespace.
  const owners: Array<[string | null, string]> = [
    ["7:p1", "7:p1"],
    [null, "device"],
  ];

  for (const [owner, namespace] of owners) {
    it.each([
      ["x-large", "strong", "true"],
      ["large", "default", "false"],
      ["bogus", "bogus", "bogus"],
      [null, null, null],
    ])(
      `parses text scale %s, weight %s, and high contrast %s the way ThemeProvider does (owner ${owner})`,
      (textScale, textWeight, highContrast) => {
        const values = {
          [KEYS.UI_CACHE_OWNER]: owner,
          [`${KEYS.UI_TEXT_SCALE}:${namespace}`]: textScale,
          [`${KEYS.UI_TEXT_WEIGHT}:${namespace}`]: textWeight,
          [`${KEYS.UI_HIGH_CONTRAST}:${namespace}`]: highContrast,
        };
        seed(values);
        const boot = bootAttributes();
        seed(values);
        expect(boot).toEqual(providerAttributes());
      },
    );
  }

  it("reads only the last identity's namespace", () => {
    seed({
      [KEYS.UI_CACHE_OWNER]: "7:p2",
      [`${KEYS.UI_TEXT_SCALE}:7:p1`]: "x-large",
      [`${KEYS.UI_TEXT_SCALE}:device`]: "large",
    });
    expect(bootAttributes()["data-text-scale"]).toBe("default");
  });

  it("paints what ThemeProvider mounts with when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("blocked", "SecurityError");
    });

    const boot = bootAttributes();
    expect(boot).toEqual(providerAttributes());
    expect(boot["data-text-scale"]).toBe("default");
  });

  it("leaves the static base theme alone", () => {
    document.documentElement.setAttribute("data-theme", "midnight-cinema");
    seed({ [KEYS.UI_CACHE_OWNER]: "7:p1", "silo-theme:7:p1": "cinema-light" });
    bootAttributes();
    expect(document.documentElement.getAttribute("data-theme")).toBe("midnight-cinema");
  });
});

describe("index.html", () => {
  it("ships Cinema Dark as the one base theme", () => {
    expect(indexHtml).toMatch(/<html [^>]*data-theme="midnight-cinema"/);
  });

  it("loads nothing from another origin", () => {
    // A cross-origin stylesheet or preconnect in the shell puts a third party on
    // the first-paint path; fonts are self-hosted from fonts.css instead.
    expect(indexHtml).not.toMatch(/\b(?:href|src)="(?:https?:)?\/\//);
  });
});
