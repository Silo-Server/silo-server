// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SETTING_KEYS } from "@/lib/settingsContract";
import { adminDefaultServerKey } from "@/lib/settingsAdminDefaults";
import {
  defaultWebPrimaryMenu,
  type PrimaryMenuDocument,
  type PrimaryMenuItem,
} from "@/lib/uiCustomization";

const mocks = vi.hoisted(() => ({
  useUICustomization: vi.fn(),
  useUserLibraries: vi.fn(),
  useSetSettingValue: vi.fn(),
  useClearSettingValue: vi.fn(),
  useAdminServerSettings: vi.fn(),
  useUpdateServerSetting: vi.fn(),
}));

vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: (...args: unknown[]) => mocks.useUICustomization(...args),
}));

// The theme control is device-local and has no server side, so it is mocked
// like the other hooks here rather than wrapped in a provider.
vi.mock("@/themes/appThemeContext", () => ({
  useAppTheme: () => ({ theme: "weflix", setTheme: vi.fn() }),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: (...args: unknown[]) => mocks.useUserLibraries(...args),
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useSetSettingValue: (...args: unknown[]) => mocks.useSetSettingValue(...args),
  useClearSettingValue: (...args: unknown[]) => mocks.useClearSettingValue(...args),
}));

// The menu editor writes the administrator default now, not a profile setting.
vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: (...args: unknown[]) => mocks.useAdminServerSettings(...args),
  useUpdateServerSetting: (...args: unknown[]) => mocks.useUpdateServerSetting(...args),
}));

// Shortcuts, badges, and the preview have their own tests; here it only
// matters that they render whatever state the web customization is in.
vi.mock("./CardOverlaySettings", () => ({
  CardOverlaySettings: () => <section aria-label="Card overlays" />,
}));
vi.mock("./PosterCardPreview", () => ({
  PosterCardPreview: () => <figure aria-label="Preview" />,
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import InterfaceSettings from "./InterfaceSettings";

const HOME: PrimaryMenuItem = { type: "builtin", destination: "home" };
const MOVIES: PrimaryMenuItem = { type: "library", library_id: 7, label: "Movies" };
const CALENDAR: PrimaryMenuItem = { type: "builtin", destination: "calendar" };
const MOVIES_MENU: PrimaryMenuDocument = { items: [HOME, MOVIES, CALENDAR] };

function customization(overrides: Record<string, unknown> = {}) {
  return {
    cardPresentation: { poster_size: "large" },
    cardPresentationSource: "profile_client",
    primaryMenu: defaultWebPrimaryMenu(),
    primaryMenuSource: "profile_client",
    shortcuts: { items: [] },
    isSupported: true,
    supportsAtomicShortcuts: true,
    isLoading: false,
    isUnavailable: false,
    ...overrides,
  };
}

describe("InterfaceSettings", () => {
  let clearMutateAsync: ReturnType<typeof vi.fn>;
  let setMutateAsync: ReturnType<typeof vi.fn>;
  let updateDefaultAsync: ReturnType<typeof vi.fn>;

  /** The admin settings map carrying a shared menu, as the server stores it. */
  function adminSettingsWithMenu(menu: PrimaryMenuDocument | null) {
    return {
      data: { [adminDefaultServerKey(SETTING_KEYS.NAV_PRIMARY_MENU)]: JSON.stringify(menu) },
    };
  }

  beforeEach(() => {
    mocks.useUICustomization.mockReset();
    mocks.useAdminServerSettings.mockReset();
    mocks.useUpdateServerSetting.mockReset();
    clearMutateAsync = vi.fn().mockResolvedValue(undefined);
    setMutateAsync = vi.fn().mockResolvedValue(undefined);
    updateDefaultAsync = vi.fn().mockResolvedValue(undefined);
    mocks.useUICustomization.mockReturnValue(customization());
    mocks.useUserLibraries.mockReturnValue({ data: [] });
    mocks.useSetSettingValue.mockReturnValue({ isPending: false, mutateAsync: setMutateAsync });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutateAsync: clearMutateAsync,
    });
    // No shared menu by default; a test that needs one sets its own.
    mocks.useAdminServerSettings.mockReturnValue(adminSettingsWithMenu(null));
    mocks.useUpdateServerSetting.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: updateDefaultAsync,
    });
  });

  afterEach(cleanup);

  it("offers to clear a browser override instead of editing under it", async () => {
    mocks.useUICustomization.mockReturnValue(
      customization({
        cardPresentation: { poster_size: "compact" },
        cardPresentationSource: "profile_device",
      }),
    );

    render(<InterfaceSettings />);

    expect(screen.queryByRole("button", { name: "Reset poster settings" })).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Large" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Match my other browsers" }));

    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        identity: { scope: "profile_device" },
      }),
    );
  });

  it("saves a poster size with the caption the schema still requires", async () => {
    render(<InterfaceSettings />);

    fireEvent.click(screen.getByRole("radio", { name: "Compact" }));

    await waitFor(() =>
      expect(setMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        value: { poster_size: "compact", caption: "title_metadata" },
        identity: { scope: "profile_client" },
      }),
    );
  });

  it("writes the menu as the default for every profile", async () => {
    mocks.useAdminServerSettings.mockReturnValue(adminSettingsWithMenu(MOVIES_MENU));

    render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));

    await waitFor(() =>
      expect(updateDefaultAsync).toHaveBeenCalledWith({
        key: adminDefaultServerKey(SETTING_KEYS.NAV_PRIMARY_MENU),
        value: JSON.stringify({ items: [HOME, CALENDAR] }),
      }),
    );
  });

  it("keeps a saved edit on screen until the refetch replaces the shared menu", async () => {
    let sharedMenu: PrimaryMenuDocument = MOVIES_MENU;
    mocks.useAdminServerSettings.mockImplementation(() => adminSettingsWithMenu(sharedMenu));

    const view = render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Move Calendar up" }));
    await waitFor(() => expect(updateDefaultAsync).toHaveBeenCalledTimes(1));

    // The save resolved but the settings have not refetched yet.
    view.rerender(<InterfaceSettings />);
    let labels = screen.getAllByRole("listitem").map((item) => item.textContent);
    expect(labels).toEqual(["Home", "Calendar", "Movies · Library"]);

    sharedMenu = { items: [HOME, CALENDAR, MOVIES] };
    view.rerender(<InterfaceSettings />);
    labels = screen.getAllByRole("listitem").map((item) => item.textContent);
    expect(labels).toEqual(["Home", "Calendar", "Movies · Library"]);
  });

  it("puts the menu back when a save fails", async () => {
    mocks.useAdminServerSettings.mockReturnValue(adminSettingsWithMenu(MOVIES_MENU));
    updateDefaultAsync.mockRejectedValueOnce(new Error("nope"));

    render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));

    expect(await screen.findByText("Movies · Library")).toBeInTheDocument();
  });

  it("resets the shared menu for everyone", async () => {
    mocks.useAdminServerSettings.mockReturnValue(adminSettingsWithMenu(MOVIES_MENU));

    render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Reset menu for everyone" }));

    await waitFor(() =>
      expect(updateDefaultAsync).toHaveBeenCalledWith({
        key: adminDefaultServerKey(SETTING_KEYS.NAV_PRIMARY_MENU),
        value: "null",
      }),
    );
  });

  it("keeps editors closed when effective customization could not be loaded", () => {
    mocks.useUICustomization.mockReturnValue(
      customization({
        cardPresentationSource: "default",
        primaryMenu: null,
        primaryMenuSource: "default",
        isUnavailable: true,
      }),
    );

    render(<InterfaceSettings />);

    expect(screen.getByRole("alert")).toHaveTextContent("Editing is off until they load");
    expect(screen.queryByRole("radiogroup", { name: "Size" })).not.toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Menu links" })).not.toBeInTheDocument();
    expect(setMutateAsync).not.toHaveBeenCalled();
    expect(clearMutateAsync).not.toHaveBeenCalled();
    expect(screen.getByRole("region", { name: "Card overlays" })).toBeInTheDocument();
  });
});
