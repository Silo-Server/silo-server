import { act, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { appearanceCache, storage } from "@/utils/storage";
import { DEFAULT_THEME } from "@/lib/themes";

const mocks = vi.hoisted(() => ({
  useOptionalAuth: vi.fn(),
  useSettings: vi.fn(),
  useBranding: vi.fn(),
  mutate: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => mocks.useOptionalAuth(),
}));

vi.mock("@/hooks/queries/settings", () => ({
  useSettings: (options?: { enabled?: boolean }) => mocks.useSettings(options),
  useSetSetting: () => ({ mutate: mocks.mutate }),
}));

vi.mock("@/hooks/useBranding", () => ({
  useBranding: () => mocks.useBranding(),
}));

import { ThemeProvider, useTheme } from "./useTheme";
import { useCustomTheme } from "./useCustomTheme";

const KEYS = storage.KEYS;

interface Captured {
  theme: ReturnType<typeof useTheme>;
  custom: ReturnType<typeof useCustomTheme>;
}

function Probe({ onRender }: { onRender: (captured: Captured) => void }) {
  const theme = useTheme();
  const custom = useCustomTheme();
  onRender({ theme, custom });
  return null;
}

/**
 * Renders the appearance providers and keeps returning the latest captured
 * values, so a test can change the signed-in account and re-render the same
 * tree — the account switch a running SPA actually performs.
 */
function renderAppearance() {
  const latest: { current: Captured | null } = { current: null };
  // A fresh element each time: re-rendering the identical element reference
  // lets React bail out, which would silently skip the account change.
  const tree = () => (
    <ThemeProvider>
      <Probe
        onRender={(next) => {
          latest.current = next;
        }}
      />
    </ThemeProvider>
  );
  const { rerender } = render(tree());
  if (!latest.current) throw new Error("probe never rendered");
  return {
    get captured(): Captured {
      if (!latest.current) throw new Error("probe never rendered");
      return latest.current;
    },
    rerender: () => rerender(tree()),
  };
}

/** Everything account 1 left behind on this browser. */
function seedAccountOneAppearance(): void {
  appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
  appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");
  appearanceCache.set(KEYS.UI_TEXT_WEIGHT, "strong", "1");
  appearanceCache.set(KEYS.UI_HIGH_CONTRAST, "true", "1");
  appearanceCache.set(KEYS.UI_CUSTOM_THEME_VARS, JSON.stringify({ "color-bg": "#ff0000" }), "1");
  appearanceCache.set(KEYS.UI_CUSTOM_CSS, "body { filter: invert(1); }", "1");
}

function signedInAs(id: number): void {
  mocks.useOptionalAuth.mockReturnValue({ loading: false, user: { id } });
}

describe("appearance cache ownership", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    mocks.useSettings.mockReturnValue({ data: {} });
    mocks.useBranding.mockReturnValue({ defaultTheme: null });
  });

  it("does not apply another account's cached appearance", () => {
    seedAccountOneAppearance();
    signedInAs(2);

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe(DEFAULT_THEME);
    expect(captured.theme.textScale).toBe("default");
    expect(captured.theme.textWeight).toBe("default");
    expect(captured.theme.highContrast).toBe(false);
    expect(captured.custom.vars).toEqual({});
    expect(captured.custom.customCss).toBe("");
  });

  it("leaves the other account's values intact instead of deleting them", () => {
    seedAccountOneAppearance();
    signedInAs(2);

    renderAppearance();

    // Account 1 signing back in must still get their warm start; the previous
    // design cleared these keys, which cost account 1 a default-theme flash on
    // every cold start from then on.
    expect(appearanceCache.get(KEYS.THEME, "1")).toBe("cobalt-studio");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1")).toBe("large");
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "1")).toBe("body { filter: invert(1); }");
  });

  it("still applies the admin default theme to an account with no cached appearance", () => {
    seedAccountOneAppearance();
    signedInAs(2);
    mocks.useBranding.mockReturnValue({ defaultTheme: "evergreen-studio" });

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe("evergreen-studio");
  });

  it("keeps the warm start for the account that stored it", () => {
    seedAccountOneAppearance();
    signedInAs(1);

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe("cobalt-studio");
    expect(captured.theme.textScale).toBe("large");
    expect(captured.theme.textWeight).toBe("strong");
    expect(captured.theme.highContrast).toBe(true);
    expect(captured.custom.vars).toEqual({ "color-bg": "#ff0000" });
    expect(captured.custom.customCss).toBe("body { filter: invert(1); }");
  });

  it("keeps the warm start while auth is still bootstrapping", () => {
    seedAccountOneAppearance();
    mocks.useOptionalAuth.mockReturnValue({ loading: true, user: null });

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe("cobalt-studio");
    expect(captured.theme.textScale).toBe("large");
    expect(captured.custom.customCss).toBe("body { filter: invert(1); }");
  });

  it("ignores a legacy cache written before namespacing existed", () => {
    storage.set(KEYS.THEME, "cobalt-studio");
    storage.set(KEYS.UI_TEXT_SCALE, "large");
    storage.set(KEYS.UI_CUSTOM_CSS, "body { filter: invert(1); }");
    signedInAs(2);

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe(DEFAULT_THEME);
    expect(captured.theme.textScale).toBe("default");
    expect(captured.custom.customCss).toBe("");
  });

  it("lets the signed-in account's own server values win over an empty local cache", () => {
    seedAccountOneAppearance();
    signedInAs(2);
    mocks.useSettings.mockReturnValue({
      data: {
        ui_theme: "oxblood-noir",
        ui_text_scale: "x-large",
        ui_custom_css: "body { color: blue; }",
      },
    });

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe("oxblood-noir");
    expect(captured.theme.textScale).toBe("x-large");
    expect(captured.custom.customCss).toBe("body { color: blue; }");
  });

  it("keeps applying the server's theme once the mirror has written it back", () => {
    signedInAs(2);
    mocks.useSettings.mockReturnValue({
      data: { ui_theme: "oxblood-noir", ui_text_scale: "x-large" },
    });

    const view = renderAppearance();
    expect(view.captured.theme.theme).toBe("oxblood-noir");

    // The mirror effect writes the server's theme into the same namespace the
    // resolver reads. A resolver that compared the two would see them agree
    // here and fall back to the default from the second render on, so this
    // re-renders rather than trusting the first paint.
    act(() => {
      view.rerender();
    });
    act(() => {
      view.rerender();
    });

    expect(view.captured.theme.theme).toBe("oxblood-noir");
    expect(view.captured.theme.textScale).toBe("x-large");
    expect(document.documentElement.getAttribute("data-theme")).toBe("oxblood-noir");
  });

  it("mirrors the server's appearance so the next cold start paints it", () => {
    signedInAs(2);
    mocks.useSettings.mockReturnValue({
      data: {
        ui_theme: "oxblood-noir",
        ui_text_scale: "x-large",
        ui_text_weight: "strong",
        ui_high_contrast: "true",
      },
    });

    renderAppearance();

    // Without this the cache only ever held choices made on this device, so a
    // user who picked their theme elsewhere flashed the default on every load.
    expect(appearanceCache.get(KEYS.THEME, "2")).toBe("oxblood-noir");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2")).toBe("x-large");
    expect(appearanceCache.get(KEYS.UI_TEXT_WEIGHT, "2")).toBe("strong");
    expect(appearanceCache.get(KEYS.UI_HIGH_CONTRAST, "2")).toBe("true");
  });

  it("does not mirror a theme the user never chose, so the admin default still moves", () => {
    signedInAs(2);
    mocks.useBranding.mockReturnValue({ defaultTheme: "evergreen-studio" });

    renderAppearance();

    expect(appearanceCache.get(KEYS.THEME, "2")).toBeNull();
  });

  it("stops painting the previous account when the signed-in account changes", () => {
    seedAccountOneAppearance();
    signedInAs(1);

    const view = renderAppearance();
    expect(view.captured.theme.theme).toBe("cobalt-studio");

    act(() => {
      signedInAs(2);
      view.rerender();
    });

    expect(view.captured.theme.theme).toBe(DEFAULT_THEME);
    expect(view.captured.theme.textScale).toBe("default");
    expect(view.captured.theme.highContrast).toBe(false);
    expect(view.captured.custom.vars).toEqual({});
    expect(view.captured.custom.customCss).toBe("");
  });

  it("restores the first account's look when they sign back in", () => {
    seedAccountOneAppearance();
    signedInAs(2);

    const view = renderAppearance();
    expect(view.captured.theme.theme).toBe(DEFAULT_THEME);

    act(() => {
      signedInAs(1);
      view.rerender();
    });

    expect(view.captured.theme.theme).toBe("cobalt-studio");
    expect(view.captured.theme.textScale).toBe("large");
    expect(view.captured.custom.customCss).toBe("body { filter: invert(1); }");
  });
});

describe("custom theme debounced writes", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    mocks.useSettings.mockReturnValue({ data: {} });
    mocks.useBranding.mockReturnValue({ defaultTheme: null });
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("drops a pending write when the account changes mid-debounce", () => {
    signedInAs(1);
    const view = renderAppearance();

    act(() => {
      view.captured.custom.setCustomCss("body { filter: invert(1); }");
    });

    // Account 1 signs out and account 2 signs in inside the 1s debounce.
    act(() => {
      signedInAs(2);
      view.rerender();
    });
    act(() => {
      vi.advanceTimersByTime(2000);
    });

    // The timer captured account 1's owner and account 2's live session. It
    // must not store account 1's CSS against account 2.
    expect(mocks.mutate).not.toHaveBeenCalled();
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "2")).toBeNull();
    expect(view.captured.custom.customCss).toBe("");
  });

  it("still persists a write that is not interrupted", () => {
    signedInAs(1);
    const view = renderAppearance();

    act(() => {
      view.captured.custom.setCustomCss("body { color: red; }");
    });
    act(() => {
      vi.advanceTimersByTime(2000);
    });

    expect(mocks.mutate).toHaveBeenCalledWith({
      key: "ui_custom_css",
      value: "body { color: red; }",
    });
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "1")).toBe("body { color: red; }");
  });
});
