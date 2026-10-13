// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SETTING_KEYS } from "@/lib/settingsContract";
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
}));

vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: (...args: unknown[]) => mocks.useUICustomization(...args),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: (...args: unknown[]) => mocks.useUserLibraries(...args),
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useSetSettingValue: (...args: unknown[]) => mocks.useSetSettingValue(...args),
  useClearSettingValue: (...args: unknown[]) => mocks.useClearSettingValue(...args),
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
    cardPresentation: { poster_size: "large", caption: "title" },
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

  beforeEach(() => {
    mocks.useUICustomization.mockReset();
    clearMutateAsync = vi.fn().mockResolvedValue(undefined);
    setMutateAsync = vi.fn().mockResolvedValue(undefined);
    mocks.useUICustomization.mockReturnValue(customization());
    mocks.useUserLibraries.mockReturnValue({ data: [] });
    mocks.useSetSettingValue.mockReturnValue({ isPending: false, mutateAsync: setMutateAsync });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutateAsync: clearMutateAsync,
    });
  });

  afterEach(cleanup);

  it("saves poster size and caption the moment they are picked", async () => {
    render(<InterfaceSettings />);

    expect(screen.getByRole("radio", { name: "Large" })).toHaveAttribute("aria-checked", "true");
    fireEvent.click(screen.getByRole("radio", { name: "Compact" }));
    await waitFor(() =>
      expect(setMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        value: { poster_size: "compact", caption: "title" },
        identity: { scope: "profile_client" },
      }),
    );

    fireEvent.click(screen.getByRole("radio", { name: "Nothing" }));
    await waitFor(() =>
      expect(setMutateAsync).toHaveBeenLastCalledWith({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        value: { poster_size: "large", caption: "artwork" },
        identity: { scope: "profile_client" },
      }),
    );
  });

  it("resets poster settings at profile_client scope", async () => {
    render(<InterfaceSettings />);

    fireEvent.click(screen.getByRole("button", { name: "Reset poster settings" }));

    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        identity: { scope: "profile_client" },
      }),
    );
  });

  it("offers to clear a browser override instead of editing under it", async () => {
    mocks.useUICustomization.mockReturnValue(
      customization({
        cardPresentation: { poster_size: "compact", caption: "artwork" },
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

  it("saves the menu on every edit, with no save button", async () => {
    mocks.useUICustomization.mockReturnValue(customization({ primaryMenu: MOVIES_MENU }));

    render(<InterfaceSettings />);
    expect(screen.queryByRole("button", { name: /save/i })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));

    expect(screen.queryByText("Movies · Library")).not.toBeInTheDocument();
    await waitFor(() =>
      expect(setMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.NAV_PRIMARY_MENU,
        value: {
          items: [
            { type: "builtin", destination: "home" },
            { type: "builtin", destination: "calendar" },
          ],
        },
        identity: { scope: "profile_client" },
      }),
    );
  });

  it("keeps a saved edit on screen until the refetch replaces the menu", async () => {
    let effectiveMenu = MOVIES_MENU;
    mocks.useUICustomization.mockImplementation(() =>
      customization({ primaryMenu: effectiveMenu }),
    );

    const view = render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Move Calendar up" }));
    await waitFor(() => expect(setMutateAsync).toHaveBeenCalledTimes(1));

    // The save resolved but the effective menu has not refetched yet.
    view.rerender(<InterfaceSettings />);
    let labels = screen.getAllByRole("listitem").map((item) => item.textContent);
    expect(labels).toEqual(["Home", "Calendar", "Movies · Library"]);

    effectiveMenu = { items: [HOME, CALENDAR, MOVIES] };
    view.rerender(<InterfaceSettings />);
    labels = screen.getAllByRole("listitem").map((item) => item.textContent);
    expect(labels).toEqual(["Home", "Calendar", "Movies · Library"]);
  });

  it("puts the menu back when a save fails", async () => {
    mocks.useUICustomization.mockReturnValue(customization({ primaryMenu: MOVIES_MENU }));
    setMutateAsync.mockRejectedValueOnce(new Error("nope"));

    render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));

    expect(await screen.findByText("Movies · Library")).toBeInTheDocument();
  });

  it("can reset a just-saved menu before its effective refetch arrives", async () => {
    mocks.useUICustomization.mockReturnValue(
      customization({ primaryMenu: MOVIES_MENU, primaryMenuSource: "profile" }),
    );

    render(<InterfaceSettings />);
    expect(screen.queryByRole("button", { name: "Reset menu" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));
    await waitFor(() => expect(setMutateAsync).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByRole("button", { name: "Reset menu" }));

    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.NAV_PRIMARY_MENU,
        identity: { scope: "profile_client" },
      }),
    );
    expect(await screen.findByText("Movies · Library")).toBeInTheDocument();
  });

  it("explains an old server once and still shows shortcuts and badges", () => {
    mocks.useUICustomization.mockReturnValue(
      customization({
        cardPresentationSource: "default",
        primaryMenu: null,
        primaryMenuSource: "default",
        isSupported: false,
        supportsAtomicShortcuts: false,
      }),
    );

    render(<InterfaceSettings />);

    expect(screen.getByRole("alert")).toHaveTextContent("Your server needs an update");
    expect(screen.queryByRole("radiogroup", { name: "Size" })).not.toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Menu links" })).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Card overlays" })).toBeInTheDocument();
    expect(screen.getByRole("figure", { name: "Preview" })).toBeInTheDocument();
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
