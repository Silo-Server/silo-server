// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useUICustomization } from "@/hooks/useUICustomization";
import { SETTING_KEYS } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  useEffectiveSettings: vi.fn(),
  useSettingsCapabilities: vi.fn(),
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
  useSettingsCapabilities: (...args: unknown[]) => mocks.useSettingsCapabilities(...args),
  settingsCapabilitiesSupportKey: (
    capabilities:
      | {
          api_version: number;
          revision: number;
          supports_batched_effective?: boolean;
          supports_idempotent_writes?: boolean;
        }
      | undefined,
  ) =>
    capabilities?.api_version === 1 &&
    capabilities.revision >= 5 &&
    capabilities.supports_batched_effective === true &&
    capabilities.supports_idempotent_writes === true,
  settingsCapabilitiesSupportAtomicShortcuts: (
    capabilities:
      | {
          api_version: number;
          revision: number;
          supports_batched_effective?: boolean;
          supports_idempotent_writes?: boolean;
          supports_atomic_shortcuts?: boolean;
        }
      | undefined,
  ) =>
    capabilities?.api_version === 1 &&
    capabilities.revision >= 5 &&
    capabilities.supports_batched_effective === true &&
    capabilities.supports_idempotent_writes === true &&
    capabilities.supports_atomic_shortcuts === true,
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
    mocks.useSettingsCapabilities.mockReturnValue({
      data: {
        api_version: 1,
        revision: 4,
        contract_etag: "revision-four",
        supports_batched_effective: true,
        supports_idempotent_writes: true,
      },
      isLoading: false,
      isError: false,
    });
  });

  afterEach(cleanup);

  it("does not request revision-five settings from an older server", () => {
    render(
      <UICustomizationProvider>
        <Probe />
      </UICustomizationProvider>,
    );

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith({
      keys: [
        SETTING_KEYS.NAV_PRIMARY_MENU,
        SETTING_KEYS.NAV_SHORTCUTS,
        SETTING_KEYS.UI_CARD_PRESENTATION,
      ],
      enabled: false,
    });
    expect(screen.getByRole("status")).toHaveAttribute("data-supported", "false");
    expect(screen.getByRole("status")).toHaveAttribute("data-atomic", "false");
    expect(screen.getByRole("status")).toHaveTextContent("standard");
  });

  it("enables revision-five reads but requires an explicit atomic flag", () => {
    mocks.useSettingsCapabilities.mockReturnValue({
      data: {
        api_version: 1,
        revision: 5,
        contract_etag: "revision-five",
        supports_batched_effective: true,
        supports_idempotent_writes: true,
      },
      isLoading: false,
      isError: false,
    });

    render(
      <UICustomizationProvider>
        <Probe />
      </UICustomizationProvider>,
    );

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        enabled: true,
      }),
    );
    expect(screen.getByRole("status")).toHaveAttribute("data-supported", "true");
    expect(screen.getByRole("status")).toHaveAttribute("data-atomic", "false");
  });

  it("fails closed without batched reads and idempotent replay", () => {
    mocks.useSettingsCapabilities.mockReturnValue({
      data: {
        api_version: 1,
        revision: 5,
        contract_etag: "revision-five-incomplete",
        supports_idempotent_writes: true,
        supports_atomic_shortcuts: true,
      },
      isLoading: false,
      isError: false,
    });

    render(
      <UICustomizationProvider>
        <Probe />
      </UICustomizationProvider>,
    );

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        enabled: false,
      }),
    );
    expect(screen.getByRole("status")).toHaveAttribute("data-supported", "false");
    expect(screen.getByRole("status")).toHaveAttribute("data-atomic", "false");
  });

  it("exposes atomic shortcut support only when advertised", () => {
    mocks.useSettingsCapabilities.mockReturnValue({
      data: {
        api_version: 1,
        revision: 5,
        contract_etag: "revision-five-atomic",
        supports_batched_effective: true,
        supports_idempotent_writes: true,
        supports_atomic_shortcuts: true,
      },
      isLoading: false,
      isError: false,
    });
    mocks.useEffectiveSettings.mockReturnValue({
      data: {
        [SETTING_KEYS.UI_CARD_PRESENTATION]: {
          value: { poster_size: "large", caption: "artwork" },
          source: "profile_client",
        },
      },
      isLoading: false,
    });

    render(
      <UICustomizationProvider>
        <Probe />
      </UICustomizationProvider>,
    );

    expect(screen.getByRole("status")).toHaveAttribute("data-atomic", "true");
    expect(screen.getByRole("status")).toHaveTextContent("large");
  });

  it("marks customization unavailable when effective settings fail to load", () => {
    mocks.useSettingsCapabilities.mockReturnValue({
      data: {
        api_version: 1,
        revision: 5,
        contract_etag: "revision-five",
        supports_batched_effective: true,
        supports_idempotent_writes: true,
        supports_atomic_shortcuts: true,
      },
      isLoading: false,
      isError: false,
    });
    mocks.useEffectiveSettings.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
    });

    render(
      <UICustomizationProvider>
        <Probe />
      </UICustomizationProvider>,
    );

    expect(screen.getByRole("status")).toHaveAttribute("data-supported", "true");
    expect(screen.getByRole("status")).toHaveAttribute("data-loading", "false");
    expect(screen.getByRole("status")).toHaveAttribute("data-unavailable", "true");
    expect(screen.getByRole("status")).toHaveTextContent("standard");
  });
});
