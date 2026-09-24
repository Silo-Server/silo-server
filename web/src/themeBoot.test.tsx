import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import bootSource from "./themeBoot.js?raw";
import indexHtml from "../index.html?raw";
import { DEFAULT_THEME, THEME_IDS } from "@/lib/themes";
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

// The branding request is still in flight on the first frame, so the real
// BrandingProvider has only what the server stamped on the shell.
vi.mock("@/api/v2/request", () => ({
  v2: () => new Promise(() => {}),
}));

import { BrandingProvider } from "@/contexts/BrandingProvider";
import { ThemeProvider } from "@/hooks/useTheme";

const KEYS = storage.KEYS;
const ATTRIBUTES = ["data-theme", "data-text-scale", "data-text-weight", "data-high-contrast"];

function clearAttributes(): void {
  for (const name of ATTRIBUTES) document.documentElement.removeAttribute(name);
}

function readAttributes(): Record<string, string | null> {
  return Object.fromEntries(
    ATTRIBUTES.map((name) => [name, document.documentElement.getAttribute(name)]),
  );
}

/**
 * Stamps the admin's default theme on <html> the way the server does when it
 * serves the shell (internal/branding RenderIndexHTML), or removes it.
 */
function serveShell(defaultTheme: string | null): void {
  if (defaultTheme === null) {
    document.documentElement.removeAttribute("data-default-theme");
  } else {
    document.documentElement.setAttribute("data-default-theme", defaultTheme);
  }
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
      <BrandingProvider>
        <ThemeProvider>
          <span />
        </ThemeProvider>
      </BrandingProvider>
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
    serveShell(null);
  });

  // Every theme id, plus a stale id and no value at all, under the last
  // identity's namespace and under the pre-sign-in device namespace, with no
  // server default, a branded one, and one naming a theme that does not exist.
  const themeCases = [...THEME_IDS, "arctic-frost", null];
  const owners: Array<[string | null, string]> = [
    ["7:p1", "7:p1"],
    [null, "device"],
  ];
  const serverDefaults = [null, "cinema-light", "arctic-frost"];
  const isThemeId = (value: string | null): value is string =>
    value !== null && (THEME_IDS as readonly string[]).includes(value);

  for (const serverDefault of serverDefaults) {
    for (const [owner, namespace] of owners) {
      for (const theme of themeCases) {
        it(`paints what ThemeProvider mounts with (server default ${serverDefault}, owner ${owner}, theme ${theme})`, () => {
          serveShell(serverDefault);
          const values = {
            [KEYS.UI_CACHE_OWNER]: owner,
            [`${KEYS.THEME}:${namespace}`]: theme,
            [`${KEYS.UI_TEXT_SCALE}:${namespace}`]: "x-large",
            [`${KEYS.UI_TEXT_WEIGHT}:${namespace}`]: "strong",
            [`${KEYS.UI_HIGH_CONTRAST}:${namespace}`]: "true",
          };
          seed(values);
          const boot = bootAttributes();
          seed(values);
          expect(boot).toEqual(providerAttributes());
          // A cached choice wins, even a stale one, which ThemeProvider reads
          // as a choice of the built-in default. Without one, the server's
          // branded default applies when it names a real theme.
          const expected =
            theme !== null
              ? isThemeId(theme)
                ? theme
                : DEFAULT_THEME
              : isThemeId(serverDefault)
                ? serverDefault
                : DEFAULT_THEME;
          expect(boot["data-theme"]).toBe(expected);
        });
      }
    }
  }

  it.each([
    ["large", "default", "false"],
    ["bogus", "bogus", "bogus"],
    [null, null, null],
  ])(
    "parses text scale %s, weight %s, and high contrast %s the way ThemeProvider does",
    (textScale, textWeight, highContrast) => {
      const values = {
        [KEYS.UI_CACHE_OWNER]: "7:p1",
        [`${KEYS.UI_TEXT_SCALE}:7:p1`]: textScale,
        [`${KEYS.UI_TEXT_WEIGHT}:7:p1`]: textWeight,
        [`${KEYS.UI_HIGH_CONTRAST}:7:p1`]: highContrast,
      };
      seed(values);
      const boot = bootAttributes();
      seed(values);
      expect(boot).toEqual(providerAttributes());
    },
  );

  it("reads only the last identity's namespace", () => {
    seed({
      [KEYS.UI_CACHE_OWNER]: "7:p2",
      [`${KEYS.THEME}:7:p1`]: "cobalt-studio",
      [`${KEYS.THEME}:device`]: "cinema-light",
    });
    expect(bootAttributes()["data-theme"]).toBe(DEFAULT_THEME);
  });

  it.each([
    [null, DEFAULT_THEME],
    ["cobalt-studio", "cobalt-studio"],
  ])(
    "paints what ThemeProvider mounts with when storage throws (server default %s)",
    (serverDefault, expected) => {
      vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
        throw new DOMException("blocked", "SecurityError");
      });
      serveShell(serverDefault);

      const boot = bootAttributes();
      expect(boot).toEqual(providerAttributes());
      expect(boot["data-theme"]).toBe(expected);
    },
  );
});

describe("index.html", () => {
  it("ships a real theme as the static default", () => {
    // What paints if the boot script does not run.
    expect(indexHtml).toMatch(new RegExp(`<html [^>]*data-theme="${DEFAULT_THEME}"`));
  });

  it("leaves the branded default to the server", () => {
    // internal/branding stamps data-default-theme when it serves the shell.
    expect(indexHtml).not.toContain("data-default-theme");
  });

  it("loads nothing from another origin", () => {
    // A cross-origin stylesheet or preconnect in the shell puts a third party on
    // the first-paint path; fonts are self-hosted from fonts.css instead.
    expect(indexHtml).not.toMatch(/\b(?:href|src)="(?:https?:)?\/\//);
  });
});
