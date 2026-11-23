import { act, render, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { v2Problem } from "@/api/v2/problems.test-support";
import { appearanceCache, storage } from "@/utils/storage";
import { SETTING_KEYS } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  useOptionalAuth: vi.fn(),
  useEffectiveSettings: vi.fn(),
  mutate: vi.fn(),
  clearMutate: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => mocks.useOptionalAuth(),
}));

vi.mock("@/hooks/queries/settingValues", async (importOriginal) => ({
  // The real failure classification, which decides how a failed save settles.
  isDefinitiveSettingMutationRejection: (
    await importOriginal<typeof import("@/hooks/queries/settingValues")>()
  ).isDefinitiveSettingMutationRejection,
  useEffectiveSettings: (options?: { keys?: readonly string[]; enabled?: boolean }) =>
    mocks.useEffectiveSettings(options),
  useSetSettingValue: () => ({ mutate: mocks.mutate, mutateAsync: mocks.mutate, isPending: false }),
  useClearSettingValue: () => ({
    mutate: mocks.clearMutate,
    mutateAsync: mocks.clearMutate,
    isPending: false,
  }),
}));

import { ThemeProvider, useTheme } from "./useTheme";

const KEYS = storage.KEYS;

type Captured = ReturnType<typeof useTheme>;

function Probe({ onRender }: { onRender: (captured: Captured) => void }) {
  onRender(useTheme());
  return null;
}

/**
 * Renders the appearance provider and keeps returning the latest captured
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
  appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", owner);
  appearanceCache.set(KEYS.UI_TEXT_WEIGHT, "strong", owner);
  appearanceCache.set(KEYS.UI_HIGH_CONTRAST, "true", owner);
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
  fetchStatus: "idle";
} {
  const data: Record<string, { key: string; value: unknown; source: string }> = {};
  for (const [key, entry] of Object.entries(values)) {
    data[key] = { key, value: entry.value, source: entry.source ?? "profile" };
  }
  // A settled read, as the query reports once it has an answer.
  return { data, fetchStatus: "idle" };
}

function expectDefaults(captured: Captured): void {
  expect(captured.textScale).toBe("default");
  expect(captured.textWeight).toBe("default");
  expect(captured.highContrast).toBe(false);
}

function expectSeeded(captured: Captured): void {
  expect(captured.textScale).toBe("large");
  expect(captured.textWeight).toBe("strong");
  expect(captured.highContrast).toBe(true);
}

describe("appearance cache ownership", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    mocks.useEffectiveSettings.mockReturnValue({ data: {} });
  });

  it("does not apply another account's cached appearance", () => {
    seedAccountOneAppearance();
    signedInAs(2);

    expectDefaults(renderAppearance().captured);
  });

  it("leaves the other identity's values intact instead of deleting them", () => {
    seedAccountOneAppearance();
    signedInAs(2);

    renderAppearance();

    // Profile 1:p1 signing back in must still get their warm start; the
    // previous design cleared these keys, which cost them a default-look
    // flash on every cold start from then on.
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p1")).toBe("large");
    expect(appearanceCache.get(KEYS.UI_HIGH_CONTRAST, "1:p1")).toBe("true");
  });

  it("keeps the warm start for the profile that stored it", () => {
    seedAccountOneAppearance();
    signedInAs(1);

    expectSeeded(renderAppearance().captured);
  });

  it.each([
    { user: null, profile: null },
    { user: { id: 2 }, profile: { id: "p2" } },
  ])("keeps the warm start while auth is still bootstrapping: %j", (identity) => {
    seedAccountOneAppearance();
    mocks.useOptionalAuth.mockReturnValue({ loading: true, ...identity });

    expectSeeded(renderAppearance().captured);
    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: false }),
    );
  });

  it("keeps the warm start on the profile picker, before a profile is chosen", () => {
    seedAccountOneAppearance();
    mocks.useOptionalAuth.mockReturnValue({ loading: false, user: { id: 1 }, profile: null });

    expectSeeded(renderAppearance().captured);
  });

  it("lets the signed-in profile's own server values win over an empty local cache", () => {
    seedAccountOneAppearance();
    signedInAs(2);
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "x-large" },
        [SETTING_KEYS.UI_HIGH_CONTRAST]: { value: true },
      }),
    );

    const { captured } = renderAppearance();

    expect(captured.textScale).toBe("x-large");
    expect(captured.highContrast).toBe(true);
  });

  it("keeps applying the server's value once the mirror has written it back", () => {
    signedInAs(2);
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({ [SETTING_KEYS.UI_TEXT_SCALE]: { value: "x-large" } }),
    );

    const view = renderAppearance();
    expect(view.captured.textScale).toBe("x-large");

    // The mirror effect writes the server's value into the same namespace the
    // resolver reads. A resolver that compared the two would see them agree
    // here and fall back to the default from the second render on, so this
    // re-renders rather than trusting the first paint.
    act(() => {
      view.rerender();
    });
    act(() => {
      view.rerender();
    });

    expect(view.captured.textScale).toBe("x-large");
    expect(document.documentElement.getAttribute("data-text-scale")).toBe("x-large");
  });

  it("mirrors the server's appearance so the next cold start paints it", () => {
    signedInAs(2);
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "x-large" },
        [SETTING_KEYS.UI_TEXT_WEIGHT]: { value: "strong" },
        [SETTING_KEYS.UI_HIGH_CONTRAST]: { value: true },
      }),
    );

    renderAppearance();

    // Without this the cache only ever held choices made on this device, so a
    // user who chose elsewhere flashed the default on every load.
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2:p1")).toBe("x-large");
    expect(appearanceCache.get(KEYS.UI_TEXT_WEIGHT, "2:p1")).toBe("strong");
    expect(appearanceCache.get(KEYS.UI_HIGH_CONTRAST, "2:p1")).toBe("true");
  });

  it("does not treat a resolved contract default as the profile's own choice", () => {
    signedInAs(2);
    // The canonical effective endpoint always answers, resolving unset keys to
    // the contract default. That answer must not be mirrored as if the profile
    // had chosen it.
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "default", source: "default" },
        [SETTING_KEYS.UI_TEXT_WEIGHT]: { value: "default", source: "default" },
        [SETTING_KEYS.UI_HIGH_CONTRAST]: { value: false, source: "default" },
      }),
    );

    renderAppearance();

    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2:p1")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_HIGH_CONTRAST, "2:p1")).toBeNull();
  });

  it("stops painting the previous account when the signed-in account changes", () => {
    seedAccountOneAppearance();
    signedInAs(1);

    const view = renderAppearance();
    expectSeeded(view.captured);

    act(() => {
      signedInAs(2);
      view.rerender();
    });

    expectDefaults(view.captured);
  });

  it("stops painting the previous profile when switching profiles on one account", () => {
    seedAccountOneAppearance();
    signedInAs(1, "p1");

    const view = renderAppearance();
    expectSeeded(view.captured);

    act(() => {
      signedInAs(1, "p2");
      view.rerender();
    });

    expectDefaults(view.captured);
    // The sibling's warm start is untouched, and nothing leaked into p2's.
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p1")).toBe("large");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p2")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_HIGH_CONTRAST, "1:p2")).toBeNull();
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
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "default", source: "default" },
        [SETTING_KEYS.UI_TEXT_WEIGHT]: { value: "default", source: "default" },
        [SETTING_KEYS.UI_HIGH_CONTRAST]: { value: false, source: "default" },
      }),
    );

    const view = renderAppearance();

    expectDefaults(view.captured);
    for (const key of [KEYS.UI_TEXT_SCALE, KEYS.UI_TEXT_WEIGHT, KEYS.UI_HIGH_CONTRAST]) {
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
        [SETTING_KEYS.UI_TEXT_SCALE]: { value: "default", source: "default" },
      }),
    );

    renderAppearance();

    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p1")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2:p9")).toBe("large");
  });

  it("does not leak one profile's mirrored server values to a sibling profile", () => {
    signedInAs(1, "p1");
    mocks.useEffectiveSettings.mockReturnValue(
      effectiveAnswer({ [SETTING_KEYS.UI_TEXT_SCALE]: { value: "x-large" } }),
    );

    const view = renderAppearance();
    expect(view.captured.textScale).toBe("x-large");

    // p2 has no stored settings of their own; the server resolves defaults.
    act(() => {
      signedInAs(1, "p2");
      mocks.useEffectiveSettings.mockReturnValue({ data: {} });
      view.rerender();
    });

    expect(view.captured.textScale).toBe("default");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p1")).toBe("x-large");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p2")).toBeNull();
  });

  // A 502 may or may not have stored the change, and the settings re-read that
  // settles it can come back unchanged; the page then shows and caches that.
  it.each([
    ["a stored value", { value: "large" }, "large", "large"],
    ["no stored value", { value: "default", source: "default" }, "default", null],
  ])(
    "shows what the server holds after a save that may have stored it fails, given %s",
    async (_label, stored, shown, cached) => {
      signedInAs(1);
      mocks.useEffectiveSettings.mockReturnValue(
        effectiveAnswer({ [SETTING_KEYS.UI_TEXT_SCALE]: stored }),
      );
      mocks.mutate.mockRejectedValue(v2Problem(502, "bad_gateway", "Bad gateway"));
      const view = renderAppearance();

      act(() => {
        view.captured.setTextScale("x-large");
      });
      expect(document.documentElement.getAttribute("data-text-scale")).toBe("x-large");

      await waitFor(() =>
        expect(document.documentElement.getAttribute("data-text-scale")).toBe(shown),
      );
      expect(view.captured.textScale).toBe(shown);
      expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p1")).toBe(cached);
    },
  );

  // When the re-read fails too, or is paused offline, the cached answer is no
  // newer than the change, which may have been stored: the page waits for a
  // read that succeeds.
  it.each([
    ["fails", { isError: true, fetchStatus: "idle" }],
    ["is paused", { isError: false, fetchStatus: "paused" }],
  ])("waits for a successful read when the re-read %s", async (_label, reread) => {
    signedInAs(1);
    const answer = effectiveAnswer({ [SETTING_KEYS.UI_TEXT_SCALE]: { value: "large" } });
    const read = (state: object) =>
      mocks.useEffectiveSettings.mockReturnValue({ ...answer, ...state });
    read({ isError: false, fetchStatus: "idle" });
    let failSave: (error: unknown) => void = () => {};
    mocks.mutate.mockReturnValue(new Promise((_resolve, reject) => (failSave = reject)));
    const view = renderAppearance();

    act(() => {
      view.captured.setTextScale("x-large");
    });
    // The save settles once its re-read has failed or paused.
    read(reread);
    await act(async () => failSave(v2Problem(502, "bad_gateway", "Bad gateway")));
    expect(document.documentElement.getAttribute("data-text-scale")).toBe("x-large");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p1")).toBe("x-large");

    // A later read succeeds with the same answer.
    read({ isError: false, fetchStatus: "idle" });
    act(() => {
      view.rerender();
    });
    await waitFor(() =>
      expect(document.documentElement.getAttribute("data-text-scale")).toBe("large"),
    );
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1:p1")).toBe("large");
  });
});
