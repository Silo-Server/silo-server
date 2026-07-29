import { act, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { appearanceCache, storage } from "@/utils/storage";
import { DEFAULT_THEME } from "@/lib/themes";
import { SETTING_KEYS } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  useOptionalAuth: vi.fn(),
  useEffectiveSettings: vi.fn(),
  useBranding: vi.fn(),
  mutate: vi.fn(),
  clearMutate: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => mocks.useOptionalAuth(),
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: (options?: { keys?: readonly string[]; enabled?: boolean }) =>
    mocks.useEffectiveSettings(options),
  useSetSettingValue: () => ({ mutate: mocks.mutate, mutateAsync: mocks.mutate, isPending: false }),
  useClearSettingValue: () => ({
    mutate: mocks.clearMutate,
    mutateAsync: mocks.clearMutate,
    isPending: false,
  }),
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
 * values, so a test can change the signed-in identity and re-render the same
 * tree — the account or profile switch a running SPA actually performs.
 */
function renderAppearance() {
  const latest: { current: Captured | null } = { current: null };
  // A fresh element each time: re-rendering the identical element reference
  // lets React bail out, which would silently skip the identity change.
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

/** Everything the given identity (`user:profile`) left behind on this browser. */
function seedAppearance(owner: string): void {
  appearanceCache.set(KEYS.THEME, "cobalt-studio", owner);
  appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", owner);
  appearanceCache.set(KEYS.UI_TEXT_WEIGHT, "strong", owner);
  appearanceCache.set(KEYS.UI_HIGH_CONTRAST, "true", owner);
  appearanceCache.set(KEYS.UI_CUSTOM_THEME_VARS, JSON.stringify({ "color-bg": "#ff0000" }), owner);
  appearanceCache.set(KEYS.UI_CUSTOM_CSS, "body { filter: invert(1); }", owner);
}

/** Everything account 1's first profile left behind on this browser. */
function seedAccountOneAppearance(): void {
  seedAppearance("1:p1");
}

function signedInAs(id: number, profileId = "p1"): void {
  mocks.useOptionalAuth.mockReturnValue({
    loading: false,
    user: { id },
    profile: { id: profileId },
  });
}

/**
 * Build a canonical effective-settings answer: each entry carries the scope it
 * resolved from, and `source: "default"` marks a value nobody stored.
 */
function effectiveAnswer(values: Record<string, { value: unknown; source?: string }>): {
  data: Record<string, { key: string; value: unknown; source: string }>;
} {
  const data: Record<string, { key: string; value: unknown; source: string }> = {};
  for (const [key, entry] of Object.entries(values)) {
    data[key] = { key, value: entry.value, source: entry.source ?? "profile" };
  }
  return { data };
}

describe("appearance cache ownership", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    mocks.useEffectiveSettings.mockReturnValue({ data: {} });
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

  it("does not apply a sibling profile's cached appearance", () => {
    seedAccountOneAppearance();
    signedInAs(1, "p2");

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe(DEFAULT_THEME);
    expect(captured.theme.textScale).toBe("default");
    expect(captured.theme.textWeight).toBe("default");
    expect(captured.theme.highContrast).toBe(false);
    expect(captured.custom.vars).toEqual({});
    expect(captured.custom.customCss).toBe("");
  });

  it("leaves the other identity's values intact instead of deleting them", () => {
    seedAccountOneAppearance();
    signedInAs(2);

    renderAppearance();

    // Profile 1:p1 signing back in must still get their warm start; the
    // previous design cleared these keys, which cost them a default-theme
    // flash on every cold start from then on.
    expect(appearanceCache.get(KEYS.THEME, "1:p1")).toBe("cobalt-studio");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p1")).toBe("large");
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "1:p1")).toBe("body { filter: invert(1); }");
  });

  it("still applies the admin default theme to an identity with no cached appearance", () => {
    seedAccountOneAppearance();
    signedInAs(2);
    mocks.useBranding.mockReturnValue({ defaultTheme: "evergreen-studio" });

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe("evergreen-studio");
  });

  it("keeps the warm start for the profile that stored it", () => {
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
    mocks.useOptionalAuth.mockReturnValue({ loading: true, user: null, profile: null });

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe("cobalt-studio");
    expect(captured.theme.textScale).toBe("large");
    expect(captured.custom.customCss).toBe("body { filter: invert(1); }");
  });

  it("keeps the warm start on the profile picker, before a profile is chosen", () => {
    seedAccountOneAppearance();
    mocks.useOptionalAuth.mockReturnValue({ loading: false, user: { id: 1 }, profile: null });

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

  it("lets the signed-in profile's own server values win over an empty local cache", () => {
    seedAccountOneAppearance();
    signedInAs(2);
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_THEME]: { value: "oxblood-noir" },
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "x-large" },
        [SETTING_KEYS.UI_CUSTOM_CSS]: { value: "body { color: blue; }" },
      }),
    );

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe("oxblood-noir");
    expect(captured.theme.textScale).toBe("x-large");
    expect(captured.custom.customCss).toBe("body { color: blue; }");
  });

  it("keeps applying the server's theme once the mirror has written it back", () => {
    signedInAs(2);
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_THEME]: { value: "oxblood-noir" },
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "x-large" },
      }),
    );

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
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_THEME]: { value: "oxblood-noir" },
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "x-large" },
        [SETTING_KEYS.UI_TEXT_WEIGHT]: { value: "strong" },
        [SETTING_KEYS.UI_HIGH_CONTRAST]: { value: true },
      }),
    );

    renderAppearance();

    // Without this the cache only ever held choices made on this device, so a
    // user who picked their theme elsewhere flashed the default on every load.
    expect(appearanceCache.get(KEYS.THEME, "2:p1")).toBe("oxblood-noir");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2:p1")).toBe("x-large");
    expect(appearanceCache.get(KEYS.UI_TEXT_WEIGHT, "2:p1")).toBe("strong");
    expect(appearanceCache.get(KEYS.UI_HIGH_CONTRAST, "2:p1")).toBe("true");
  });

  it("does not mirror a theme the user never chose, so the admin default still moves", () => {
    signedInAs(2);
    mocks.useBranding.mockReturnValue({ defaultTheme: "evergreen-studio" });

    renderAppearance();

    expect(appearanceCache.get(KEYS.THEME, "2:p1")).toBeNull();
  });

  it("does not treat a resolved contract default as the profile's own choice", () => {
    signedInAs(2);
    mocks.useBranding.mockReturnValue({ defaultTheme: "evergreen-studio" });
    // The canonical effective endpoint always answers, resolving unset keys to
    // the contract default. That answer must not shadow the admin default nor
    // be mirrored as if the profile had chosen it.
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_THEME]: { value: "midnight-cinema", source: "default" },
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "default", source: "default" },
        [SETTING_KEYS.UI_TEXT_WEIGHT]: { value: "default", source: "default" },
        [SETTING_KEYS.UI_HIGH_CONTRAST]: { value: false, source: "default" },
      }),
    );

    const { captured } = renderAppearance();

    expect(captured.theme.theme).toBe("evergreen-studio");
    expect(appearanceCache.get(KEYS.THEME, "2:p1")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2:p1")).toBeNull();
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

  it("stops painting the previous profile when switching profiles on one account", () => {
    seedAccountOneAppearance();
    signedInAs(1, "p1");

    const view = renderAppearance();
    expect(view.captured.theme.theme).toBe("cobalt-studio");

    act(() => {
      signedInAs(1, "p2");
      view.rerender();
    });

    expect(view.captured.theme.theme).toBe(DEFAULT_THEME);
    expect(view.captured.theme.textScale).toBe("default");
    expect(view.captured.theme.textWeight).toBe("default");
    expect(view.captured.theme.highContrast).toBe(false);
    expect(view.captured.custom.vars).toEqual({});
    expect(view.captured.custom.customCss).toBe("");
    // The sibling's warm start is untouched, and nothing leaked into p2's.
    expect(appearanceCache.get(KEYS.THEME, "1:p1")).toBe("cobalt-studio");
    expect(appearanceCache.get(KEYS.THEME, "1:p2")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "1:p2")).toBeNull();
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

  it("restores a profile's look when switching back to it", () => {
    seedAccountOneAppearance();
    signedInAs(1, "p2");

    const view = renderAppearance();
    expect(view.captured.theme.theme).toBe(DEFAULT_THEME);

    act(() => {
      signedInAs(1, "p1");
      view.rerender();
    });

    expect(view.captured.theme.theme).toBe("cobalt-studio");
    expect(view.captured.theme.textScale).toBe("large");
    expect(view.captured.theme.highContrast).toBe(true);
    expect(view.captured.custom.vars).toEqual({ "color-bg": "#ff0000" });
    expect(view.captured.custom.customCss).toBe("body { filter: invert(1); }");
  });

  it("clears the warm start when the server says the setting is unset", () => {
    // Another client deleted this profile's appearance choices. The effective
    // response still answers for every key, now resolving to the contract
    // default — the removal has to reach this browser, or the cached value
    // keeps painting a preference the server no longer holds.
    seedAccountOneAppearance();
    signedInAs(1, "p1");
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_THEME]: { value: DEFAULT_THEME, source: "default" },
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "default", source: "default" },
        [SETTING_KEYS.UI_TEXT_WEIGHT]: { value: "default", source: "default" },
        [SETTING_KEYS.UI_HIGH_CONTRAST]: { value: false, source: "default" },
      }),
    );

    const view = renderAppearance();

    expect(view.captured.theme.theme).toBe(DEFAULT_THEME);
    expect(view.captured.theme.textScale).toBe("default");
    expect(view.captured.theme.textWeight).toBe("default");
    expect(view.captured.theme.highContrast).toBe(false);
    for (const key of [
      KEYS.THEME,
      KEYS.UI_TEXT_SCALE,
      KEYS.UI_TEXT_WEIGHT,
      KEYS.UI_HIGH_CONTRAST,
    ]) {
      expect(appearanceCache.get(key, "1:p1")).toBeNull();
    }
  });

  it("clears only the current identity's warm start on an unset answer", () => {
    // The sibling's namespace is not ours to clear: the server answered about
    // this profile, and the other identity's warm start must survive for when
    // they sign back in.
    seedAppearance("1:p1");
    seedAppearance("2:p9");
    signedInAs(1, "p1");
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_THEME]: { value: DEFAULT_THEME, source: "default" },
      }),
    );

    renderAppearance();

    expect(appearanceCache.get(KEYS.THEME, "1:p1")).toBeNull();
    expect(appearanceCache.get(KEYS.THEME, "2:p9")).toBe("cobalt-studio");
  });

  it("does not leak one profile's mirrored server values to a sibling profile", () => {
    signedInAs(1, "p1");
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({ [SETTING_KEYS.UI_THEME]: { value: "oxblood-noir" } }),
    );

    const view = renderAppearance();
    expect(view.captured.theme.theme).toBe("oxblood-noir");

    // p2 has no stored settings of their own; the server resolves defaults.
    act(() => {
      signedInAs(1, "p2");
      mocks.useEffectiveSettings.mockReturnValue({ data: {} });
      view.rerender();
    });

    expect(view.captured.theme.theme).toBe(DEFAULT_THEME);
    expect(appearanceCache.get(KEYS.THEME, "1:p1")).toBe("oxblood-noir");
    expect(appearanceCache.get(KEYS.THEME, "1:p2")).toBeNull();
  });
});

describe("custom theme debounced writes", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    mocks.useEffectiveSettings.mockReturnValue({ data: {} });
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
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "2:p1")).toBeNull();
    expect(view.captured.custom.customCss).toBe("");
  });

  it("drops a pending write when the profile changes mid-debounce", () => {
    signedInAs(1, "p1");
    const view = renderAppearance();

    act(() => {
      view.captured.custom.setCustomCss("body { filter: invert(1); }");
    });

    // The household switches from p1 to p2 inside the 1s debounce. The timer
    // captured p1's draft; firing now would store p1's CSS as p2's preference
    // at profile scope.
    act(() => {
      signedInAs(1, "p2");
      view.rerender();
    });
    act(() => {
      vi.advanceTimersByTime(2000);
    });

    expect(mocks.mutate).not.toHaveBeenCalled();
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "1:p2")).toBeNull();
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
      key: SETTING_KEYS.UI_CUSTOM_CSS,
      value: "body { color: red; }",
      identity: { scope: "profile" },
    });
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "1:p1")).toBe("body { color: red; }");
  });
});
