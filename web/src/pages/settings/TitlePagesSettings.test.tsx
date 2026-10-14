// @vitest-environment jsdom

import { render, screen, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Radix Select reads element sizes via ResizeObserver and opens through
// pointer capture; jsdom provides neither.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}
if (typeof window !== "undefined" && !window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

import type {
  EffectiveSetting,
  EffectiveSettingsMap,
  SettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  useEffectiveSettings: vi.fn(),
  useSetSettingValue: vi.fn(),
  useClearSettingValue: vi.fn(),
  capabilities: {
    api_version: 1,
    manifest_revision: 7,
    contract_etag: "revision-seven",
    supports_batched_effective: true,
    supports_idempotent_writes: true,
  } as SettingsCapabilities | undefined,
  /** Whether the capability check has answered; false covers pending and failed. */
  capabilitiesSettled: true,
  /** A failed capability request, which reads as "unknown", never as "old". */
  capabilitiesError: undefined as Error | undefined,
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("sonner", () => ({ toast: mocks.toast }));

vi.mock("@/hooks/queries/settingValues", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/settingValues")>(
    "@/hooks/queries/settingValues",
  );

  return {
    ...actual,
    useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
    useSetSettingValue: (...args: unknown[]) => mocks.useSetSettingValue(...args),
    useClearSettingValue: (...args: unknown[]) => mocks.useClearSettingValue(...args),
    useSettingsCapabilities: () => ({
      data: mocks.capabilities,
      isLoading: false,
      isPending: !mocks.capabilitiesSettled && !mocks.capabilitiesError,
      isSuccess: mocks.capabilitiesSettled,
      error: mocks.capabilitiesError,
    }),
  };
});

// Title art has its own suite and reads hooks this page's tests do not mock.
vi.mock("@/pages/settings/TitleArtSettingsGroup", () => ({ TitleArtSettingsGroup: () => null }));

import TitlePagesSettings from "./TitlePagesSettings";

function capabilitiesAtRevision(revision: number): SettingsCapabilities {
  return {
    api_version: 1,
    manifest_revision: revision,
    contract_etag: `revision-${revision}`,
    supports_batched_effective: true,
    supports_idempotent_writes: true,
  };
}

function resolved(
  key: SettingKey,
  value: unknown,
  source: EffectiveSetting["source"],
): EffectiveSettingsMap {
  return { [key]: { key, value, source } };
}

describe("TitlePagesSettings", () => {
  let mutate: ReturnType<typeof vi.fn>;
  let mutateAsync: ReturnType<typeof vi.fn>;
  let clearMutateAsync: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mocks.useEffectiveSettings.mockReset();
    mocks.useSetSettingValue.mockReset();
    mocks.useClearSettingValue.mockReset();
    mocks.capabilities = capabilitiesAtRevision(7);
    mocks.capabilitiesSettled = true;
    mocks.capabilitiesError = undefined;
    mocks.toast.error.mockReset();
    mocks.toast.success.mockReset();
    // The environment's localStorage global is inert; legacy audiobook
    // intervals are read through it, so give each test a fresh in-memory one.
    const memory = new Map<string, string>();
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: {
        getItem: (key: string) => memory.get(key) ?? null,
        setItem: (key: string, value: string) => memory.set(key, value),
        removeItem: (key: string) => memory.delete(key),
      },
    });
    mutate = vi.fn();
    mutateAsync = vi.fn().mockResolvedValue(undefined);
    clearMutateAsync = vi.fn().mockResolvedValue(undefined);

    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useSetSettingValue.mockReturnValue({ isPending: false, mutate, mutateAsync });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: clearMutateAsync,
    });
  });

  afterEach(cleanup);

  it.each([
    ["older server", capabilitiesAtRevision(10)],
    ["unknown capabilities", undefined],
    ["unsupported settings API", { ...capabilitiesAtRevision(11), api_version: 2 }],
    ["no batched settings", { ...capabilitiesAtRevision(11), supports_batched_effective: false }],
  ])("does not request or offer theme settings with %s", (_scenario, capabilities) => {
    mocks.capabilities = capabilities as SettingsCapabilities | undefined;
    mocks.capabilitiesSettled = capabilities !== undefined;

    render(<TitlePagesSettings />);

    expect(screen.queryByRole("switch", { name: "Theme music" })).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Loop theme music" })).not.toBeInTheDocument();
    for (const [options] of mocks.useEffectiveSettings.mock.calls) {
      expect(options.keys ?? []).not.toContain(SETTING_KEYS.UI_THEME_MUSIC_ENABLED);
      expect(options.keys ?? []).not.toContain(SETTING_KEYS.UI_THEME_MUSIC_LOOP);
    }
    expect(mutateAsync).not.toHaveBeenCalled();
  });

  it("offers supported theme settings and saves them at profile scope", async () => {
    mocks.capabilities = capabilitiesAtRevision(11);
    render(<TitlePagesSettings />);

    const requested = mocks.useEffectiveSettings.mock.calls.flatMap(
      ([options]) => options.keys ?? [],
    );
    expect(requested).toContain(SETTING_KEYS.UI_THEME_MUSIC_ENABLED);
    expect(requested).toContain(SETTING_KEYS.UI_THEME_MUSIC_LOOP);
    const enabled = screen.getByRole("switch", { name: "Theme music" });
    const loop = screen.getByRole("switch", { name: "Loop theme music" });
    expect(enabled).not.toBeChecked();
    expect(loop).not.toBeChecked();
    await userEvent.click(enabled);
    await userEvent.click(loop);
    expect(mutateAsync).toHaveBeenCalledWith({
      key: SETTING_KEYS.UI_THEME_MUSIC_ENABLED,
      value: true,
      identity: { scope: "profile" },
    });
    expect(mutateAsync).toHaveBeenCalledWith({
      key: SETTING_KEYS.UI_THEME_MUSIC_LOOP,
      value: true,
      identity: { scope: "profile" },
    });
  });

  it("updates theme controls and requested keys when server support changes", () => {
    mocks.capabilities = undefined;
    mocks.capabilitiesSettled = false;
    const { rerender } = render(<TitlePagesSettings />);
    expect(screen.queryByRole("switch", { name: "Theme music" })).not.toBeInTheDocument();

    mocks.capabilities = capabilitiesAtRevision(11);
    mocks.capabilitiesSettled = true;
    rerender(<TitlePagesSettings />);
    expect(screen.getByRole("switch", { name: "Theme music" })).toBeInTheDocument();

    mocks.useEffectiveSettings.mockClear();
    mocks.capabilities = capabilitiesAtRevision(10);
    rerender(<TitlePagesSettings />);
    expect(screen.queryByRole("switch", { name: "Theme music" })).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Loop theme music" })).not.toBeInTheDocument();
    for (const [options] of mocks.useEffectiveSettings.mock.calls) {
      expect(options.keys ?? []).not.toContain(SETTING_KEYS.UI_THEME_MUSIC_ENABLED);
      expect(options.keys ?? []).not.toContain(SETTING_KEYS.UI_THEME_MUSIC_LOOP);
    }
  });

  it.each(["pending", "error", "refetch error"] as const)(
    "blocks theme writes while effective settings are %s and recovers after loading",
    async (state) => {
      mocks.capabilities = capabilitiesAtRevision(11);
      const values = {
        ...resolved(SETTING_KEYS.UI_THEME_MUSIC_ENABLED, true, "profile"),
        ...resolved(SETTING_KEYS.UI_THEME_MUSIC_LOOP, true, "profile"),
      };
      mocks.useEffectiveSettings.mockReturnValue({
        data: state === "refetch error" ? values : undefined,
        isPending: state === "pending",
        isError: state !== "pending",
      });
      const { rerender } = render(<TitlePagesSettings />);
      const enabled = screen.getByRole("switch", { name: "Theme music" });
      const loop = screen.getByRole("switch", { name: "Loop theme music" });
      expect(enabled).toBeDisabled();
      expect(loop).toBeDisabled();
      await userEvent.click(enabled);
      await userEvent.click(loop);
      expect(mutateAsync).not.toHaveBeenCalled();
      expect(clearMutateAsync).not.toHaveBeenCalled();

      mocks.useEffectiveSettings.mockReturnValue({
        data: values,
        isPending: false,
        isError: false,
      });
      rerender(<TitlePagesSettings />);
      expect(enabled).toBeEnabled();
      expect(loop).toBeEnabled();
      expect(enabled).toBeChecked();
      expect(loop).toBeChecked();
      await userEvent.click(enabled);
      await userEvent.click(loop);
      for (const key of [SETTING_KEYS.UI_THEME_MUSIC_ENABLED, SETTING_KEYS.UI_THEME_MUSIC_LOOP]) {
        expect(mutateAsync).toHaveBeenCalledWith({
          key,
          value: false,
          identity: { scope: "profile" },
        });
      }
    },
  );

  it("saves the spoiler switches and the advisory age at profile scope", async () => {
    // Revision 17 introduced the spoiler keys.
    mocks.capabilities = capabilitiesAtRevision(17);
    render(<TitlePagesSettings />);

    await userEvent.click(screen.getByRole("switch", { name: "Blur unwatched episode images" }));
    await userEvent.click(
      screen.getByRole("switch", { name: "Hide unwatched episode descriptions" }),
    );
    await userEvent.click(screen.getByRole("switch", { name: "Show advisory age" }));

    for (const key of [
      SETTING_KEYS.CATALOG_HIDE_UNWATCHED_EPISODE_IMAGES,
      SETTING_KEYS.CATALOG_HIDE_UNWATCHED_EPISODE_OVERVIEWS,
      SETTING_KEYS.CATALOG_SHOW_ADVISORY_AGE,
    ]) {
      expect(mutateAsync).toHaveBeenCalledWith({
        key,
        value: true,
        identity: { scope: "profile" },
      });
    }
  });

  it("leaves the spoiler switches off a server that cannot store them", () => {
    mocks.capabilities = capabilitiesAtRevision(16);
    render(<TitlePagesSettings />);

    expect(
      screen.queryByRole("switch", { name: "Blur unwatched episode images" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Show advisory age" })).toBeInTheDocument();
  });
});
