// @vitest-environment jsdom

import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { BrandingContext, type BrandingContextValue } from "@/contexts/BrandingProvider";
import type { ThemeId } from "@/lib/themes";

import { SiloBrand, type SiloBrandVariant } from "./SiloBrand";

// The theme context is optional for SiloBrand, so the mock models the two
// shapes that matter: a provider is present, or the component renders outside
// one (login chrome, other component tests).
let mockActiveTheme: ThemeId | null = null;

vi.mock("@/hooks/useTheme", () => ({
  useOptionalTheme: () => (mockActiveTheme ? { activeTheme: mockActiveTheme } : null),
}));

const BRANDING_DEFAULTS: BrandingContextValue = {
  serverName: "Silo",
  loginSubtitle: "Sign in with an existing account.",
  accentColor: null,
  defaultTheme: null,
  wordmarkUrl: null,
  markUrl: null,
  wordmarkLightUrl: null,
  markLightUrl: null,
  faviconUrl: null,
  loginBgUrl: null,
  storageAvailable: false,
};

function renderBrandSrc(
  variant: SiloBrandVariant,
  branding: Partial<BrandingContextValue> = {},
): string | null {
  const markup = renderToStaticMarkup(
    <BrandingContext value={{ ...BRANDING_DEFAULTS, ...branding }}>
      <SiloBrand variant={variant} />
    </BrandingContext>,
  );
  return new DOMParser()
    .parseFromString(markup, "text/html")
    .querySelector("img")
    ?.getAttribute("src") as string | null;
}

describe("SiloBrand", () => {
  beforeEach(() => {
    mockActiveTheme = "midnight-cinema";
  });

  it("uses the white-text built-in wordmark on a dark theme", () => {
    expect(renderBrandSrc("wordmark")).toBe("/silo-wordmark-sidebar.png");
  });

  it("uses the custom wordmark on a dark theme", () => {
    expect(renderBrandSrc("wordmark", { wordmarkUrl: "https://cdn/custom.png" })).toBe(
      "https://cdn/custom.png",
    );
  });

  it("uses the dark-text built-in wordmark on a light theme", () => {
    mockActiveTheme = "cinema-light";
    expect(renderBrandSrc("wordmark")).toBe("/silo-wordmark-sidebar-light.png");
  });

  it("falls back to the main custom wordmark on a light theme when no variant is set", () => {
    mockActiveTheme = "cinema-light";
    expect(renderBrandSrc("wordmark", { wordmarkUrl: "https://cdn/custom.png" })).toBe(
      "https://cdn/custom.png",
    );
  });

  it("prefers the light wordmark variant over the main custom wordmark on a light theme", () => {
    mockActiveTheme = "cinema-light";
    expect(
      renderBrandSrc("wordmark", {
        wordmarkUrl: "https://cdn/custom.png",
        wordmarkLightUrl: "https://cdn/custom-light.png",
      }),
    ).toBe("https://cdn/custom-light.png");
  });

  it("uses the custom mark on a dark theme", () => {
    expect(renderBrandSrc("mark", { markUrl: "https://cdn/mark.png" })).toBe(
      "https://cdn/mark.png",
    );
  });

  it("keeps the shared built-in mark on a light theme with no custom asset", () => {
    mockActiveTheme = "cinema-light";
    expect(renderBrandSrc("mark")).toBe("/silo-icon-1024.png");
  });

  it("prefers the light mark variant over the main custom mark on a light theme", () => {
    mockActiveTheme = "cinema-light";
    expect(
      renderBrandSrc("mark", {
        markUrl: "https://cdn/mark.png",
        markLightUrl: "https://cdn/mark-light.png",
      }),
    ).toBe("https://cdn/mark-light.png");
  });

  it("falls back to the main custom mark on a light theme when no variant is set", () => {
    mockActiveTheme = "cinema-light";
    expect(renderBrandSrc("mark", { markUrl: "https://cdn/mark.png" })).toBe(
      "https://cdn/mark.png",
    );
  });

  it("assumes a dark appearance outside ThemeProvider", () => {
    mockActiveTheme = null;
    expect(renderBrandSrc("wordmark")).toBe("/silo-wordmark-sidebar.png");
    expect(renderBrandSrc("mark")).toBe("/silo-icon-1024.png");
  });
});
