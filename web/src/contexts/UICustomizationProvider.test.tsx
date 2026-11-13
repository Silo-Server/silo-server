// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useUICustomization } from "@/hooks/useUICustomization";
import { SETTING_KEYS } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  useEffectiveSettings: vi.fn(),
  useSettingsCapabilities: vi.fn(),
}));

vi.mock("@/hooks/queries/settingValues", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/settingValues")>()),
  useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
  useSettingsCapabilities: (...args: unknown[]) => mocks.useSettingsCapabilities(...args),
}));

import { UICustomizationProvider } from "./UICustomizationProvider";

function Probe() {
  const customization = useUICustomization();
  return (
    <output
      data-supported={String(customization.isSupported)}
      data-atomic={String(customization.supportsAtomicShortcuts)}
      data-loading={String(customization.isLoading)}
      data-unavailable={String(customization.isUnavailable)}
    >
      {customization.cardPresentation.poster_size}
    </output>
  );
}

function advertise(revision: number, etag: string, extra: Record<string, boolean> = {}) {
  mocks.useSettingsCapabilities.mockReturnValue({
    data: {
      api_version: 1,
      manifest_revision: revision,
      contract_etag: etag,
      supports_batched_effective: true,
      supports_idempotent_writes: true,
      ...extra,
    },
    isLoading: false,
    isError: false,
  });
}

/** Renders the provider and returns the probe's output element. */
function renderProbe() {
  render(
    <UICustomizationProvider>
      <Probe />
    </UICustomizationProvider>,
  );
  return screen.getByRole("status");
}

describe("UICustomizationProvider capability gating", () => {
  beforeEach(() => {
    mocks.useEffectiveSettings.mockReset();
    mocks.useSettingsCapabilities.mockReset();
    mocks.useEffectiveSettings.mockReturnValue({
      // Disabled TanStack queries can retain stale cache data. The provider
      // must ignore it until the connected server advertises support.
      data: {
        [SETTING_KEYS.UI_CARD_PRESENTATION]: {
          value: { poster_size: "large", caption: "artwork" },
          source: "profile_client",
        },
      },
      isLoading: false,
      isError: false,
    });
    advertise(4, "revision-four");
  });

  afterEach(cleanup);

  it("does not request revision-five settings from an older server", () => {
    const status = renderProbe();

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith({
      keys: [
        SETTING_KEYS.NAV_PRIMARY_MENU,
        SETTING_KEYS.NAV_SHORTCUTS,
        SETTING_KEYS.UI_CARD_PRESENTATION,
      ],
      enabled: false,
    });
    expect(status).toHaveAttribute("data-supported", "false");
    expect(status).toHaveAttribute("data-atomic", "false");
    expect(status).toHaveTextContent("standard");
  });

  it("enables revision-five reads but requires an explicit atomic flag", () => {
    advertise(5, "revision-five");

    const status = renderProbe();

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        enabled: true,
      }),
    );
    expect(status).toHaveAttribute("data-supported", "true");
    expect(status).toHaveAttribute("data-atomic", "false");
  });

  it("exposes atomic shortcut support only when advertised", () => {
    advertise(5, "revision-five-atomic", { supports_atomic_shortcuts: true });
    mocks.useEffectiveSettings.mockReturnValue({
      data: {
        [SETTING_KEYS.UI_CARD_PRESENTATION]: {
          value: { poster_size: "large", caption: "artwork" },
          source: "profile_client",
        },
      },
      isLoading: false,
    });

    const status = renderProbe();

    expect(status).toHaveAttribute("data-atomic", "true");
    expect(status).toHaveTextContent("large");
  });

  it("marks customization unavailable when effective settings fail to load", () => {
    advertise(5, "revision-five", { supports_atomic_shortcuts: true });
    mocks.useEffectiveSettings.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
    });

    const status = renderProbe();

    expect(status).toHaveAttribute("data-supported", "true");
    expect(status).toHaveAttribute("data-loading", "false");
    expect(status).toHaveAttribute("data-unavailable", "true");
    expect(status).toHaveTextContent("standard");
  });
});
