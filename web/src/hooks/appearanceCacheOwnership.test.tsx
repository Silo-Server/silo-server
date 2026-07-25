import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { appearanceCache, customThemeCache, storage } from "@/utils/storage";
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

function renderAppearance(): Captured {
  let captured: Captured | null = null;
  render(
    <ThemeProvider>
      <Probe
        onRender={(next) => {
          captured = next;
        }}
      />
    </ThemeProvider>,
  );
  if (!captured) throw new Error("probe never rendered");
  return captured;
}

/** Everything account 1 left behind on this browser. */
function seedAccountOneAppearance(): void {
  appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
  appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");
  appearanceCache.set(KEYS.UI_TEXT_WEIGHT, "strong", "1");
  appearanceCache.set(KEYS.UI_HIGH_CONTRAST, "true", "1");
  customThemeCache.set(KEYS.UI_CUSTOM_THEME_VARS, JSON.stringify({ "color-bg": "#ff0000" }), "1");
  customThemeCache.set(KEYS.UI_CUSTOM_CSS, "body { filter: invert(1); }", "1");
}

function signedInAs(id: number): void {
  mocks.useOptionalAuth.mockReturnValue({ loading: false, user: { id } });
}

describe("appearance cache ownership", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Object.values(KEYS).forEach((key) => storage.remove(key));
    mocks.useSettings.mockReturnValue({ data: {} });
    mocks.useBranding.mockReturnValue({ defaultTheme: null });
  });

  it("does not apply another account's cached appearance", () => {
    seedAccountOneAppearance();
    signedInAs(2);

    const captured = renderAppearance();

    expect(captured.theme.theme).toBe(DEFAULT_THEME);
    expect(captured.theme.textScale).toBe("default");
    expect(captured.theme.textWeight).toBe("default");
    expect(captured.theme.highContrast).toBe(false);
    expect(captured.custom.vars).toEqual({});
    expect(captured.custom.customCss).toBe("");
  });

  it("drops another account's cached appearance instead of leaving it to be re-trusted", () => {
    seedAccountOneAppearance();
    signedInAs(2);

    renderAppearance();

    expect(storage.get(KEYS.THEME)).toBeNull();
    expect(storage.get(KEYS.UI_TEXT_SCALE)).toBeNull();
    expect(storage.get(KEYS.UI_TEXT_WEIGHT)).toBeNull();
    expect(storage.get(KEYS.UI_HIGH_CONTRAST)).toBeNull();
    expect(storage.get(KEYS.UI_CUSTOM_THEME_VARS)).toBeNull();
    expect(storage.get(KEYS.UI_CUSTOM_CSS)).toBeNull();
    expect(appearanceCache.isTrusted("2")).toBe(true);
    expect(customThemeCache.isTrusted("2")).toBe(true);
  });

  it("still applies the admin default theme to an account that inherited a foreign cache", () => {
    seedAccountOneAppearance();
    signedInAs(2);
    mocks.useBranding.mockReturnValue({ defaultTheme: "evergreen-studio" });

    const captured = renderAppearance();

    expect(captured.theme.theme).toBe("evergreen-studio");
  });

  it("keeps the warm start for the account that stored it", () => {
    seedAccountOneAppearance();
    signedInAs(1);

    const captured = renderAppearance();

    expect(captured.theme.theme).toBe("cobalt-studio");
    expect(captured.theme.textScale).toBe("large");
    expect(captured.theme.textWeight).toBe("strong");
    expect(captured.theme.highContrast).toBe(true);
    expect(captured.custom.vars).toEqual({ "color-bg": "#ff0000" });
    expect(captured.custom.customCss).toBe("body { filter: invert(1); }");
    expect(storage.get(KEYS.THEME)).toBe("cobalt-studio");
  });

  it("keeps the warm start while auth is still bootstrapping", () => {
    seedAccountOneAppearance();
    mocks.useOptionalAuth.mockReturnValue({ loading: true, user: null });

    const captured = renderAppearance();

    expect(captured.theme.theme).toBe("cobalt-studio");
    expect(captured.theme.textScale).toBe("large");
    expect(captured.custom.customCss).toBe("body { filter: invert(1); }");
    expect(storage.get(KEYS.THEME)).toBe("cobalt-studio");
  });

  it("ignores an unstamped legacy cache once an account is known", () => {
    storage.set(KEYS.THEME, "cobalt-studio");
    storage.set(KEYS.UI_TEXT_SCALE, "large");
    storage.set(KEYS.UI_CUSTOM_CSS, "body { filter: invert(1); }");
    signedInAs(2);

    const captured = renderAppearance();

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

    const captured = renderAppearance();

    expect(captured.theme.theme).toBe("oxblood-noir");
    expect(captured.theme.textScale).toBe("x-large");
    expect(captured.custom.customCss).toBe("body { color: blue; }");
    expect(storage.get(KEYS.UI_CUSTOM_CSS)).toBe("body { color: blue; }");
    expect(customThemeCache.isTrusted("2")).toBe(true);
  });
});
