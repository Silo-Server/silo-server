// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SETTING_KEYS } from "@/lib/settingsContract";
import { defaultWebPrimaryMenu } from "@/lib/uiCustomization";

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

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import InterfaceSettings from "./InterfaceSettings";

describe("InterfaceSettings card resets", () => {
  let clearMutateAsync: ReturnType<typeof vi.fn>;
  let setMutateAsync: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mocks.useUICustomization.mockReset();
    mocks.useUserLibraries.mockReset();
    mocks.useSetSettingValue.mockReset();
    mocks.useClearSettingValue.mockReset();
    clearMutateAsync = vi.fn().mockResolvedValue(undefined);
    setMutateAsync = vi.fn().mockResolvedValue(undefined);

    mocks.useUICustomization.mockReturnValue({
      cardPresentation: { poster_size: "large", caption: "title" },
      cardPresentationSource: "profile_client",
      primaryMenu: defaultWebPrimaryMenu(),
      primaryMenuSource: "profile_client",
      shortcuts: { items: [] },
      isSupported: true,
      supportsAtomicShortcuts: true,
      isLoading: false,
      isUnavailable: false,
    });
    mocks.useUserLibraries.mockReturnValue({ data: [] });
    mocks.useSetSettingValue.mockReturnValue({ isPending: false, mutateAsync: setMutateAsync });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutateAsync: clearMutateAsync,
    });
  });

  afterEach(cleanup);

  it("resets the web-family card preference at profile_client scope", async () => {
    render(<InterfaceSettings />);

    const reset = screen.getByRole("button", { name: "Reset web-family card layout" });
    expect(reset).toHaveAccessibleDescription(
      "Remove the layout shared by web browsers and inherit the profile or app default.",
    );
    fireEvent.click(reset);

    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        identity: { scope: "profile_client" },
      }),
    );
  });

  it("keeps the device reset distinct from the web-family reset", async () => {
    mocks.useUICustomization.mockReturnValue({
      cardPresentation: { poster_size: "compact", caption: "artwork" },
      cardPresentationSource: "profile_device",
      primaryMenu: defaultWebPrimaryMenu(),
      primaryMenuSource: "profile_client",
      shortcuts: { items: [] },
      isSupported: true,
      supportsAtomicShortcuts: true,
      isLoading: false,
    });

    render(<InterfaceSettings />);

    expect(
      screen.queryByRole("button", { name: "Reset web-family card layout" }),
    ).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Use web-family layout" }));

    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        identity: { scope: "profile_device" },
      }),
    );
  });

  it("follows the effective menu after clearing a web-family override", async () => {
    const effectiveMenu = {
      items: [
        { type: "builtin" as const, destination: "home" as const },
        { type: "library" as const, library_id: 7, label: "Movies" },
      ],
    };
    let source = "profile_client";
    mocks.useUICustomization.mockImplementation(() => ({
      cardPresentation: { poster_size: "large", caption: "title" },
      cardPresentationSource: "profile_client",
      primaryMenu: effectiveMenu,
      primaryMenuSource: source,
      shortcuts: { items: [] },
      isSupported: true,
      supportsAtomicShortcuts: true,
      isLoading: false,
    }));
    clearMutateAsync.mockImplementation(async () => {
      source = "profile";
    });

    render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));
    expect(screen.queryByText("Movies · Library")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Reset to default" }));

    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.NAV_PRIMARY_MENU,
        identity: { scope: "profile_client" },
      }),
    );
    expect(await screen.findByText("Movies · Library")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save menu" })).toBeDisabled();
  });

  it("keeps a rapid post-save edit when the saved baseline refetch arrives", async () => {
    let effectiveMenu = {
      items: [
        { type: "builtin" as const, destination: "home" as const },
        { type: "library" as const, library_id: 7, label: "Movies" },
        { type: "builtin" as const, destination: "calendar" as const },
      ],
    };
    mocks.useUICustomization.mockImplementation(() => ({
      cardPresentation: { poster_size: "large", caption: "title" },
      cardPresentationSource: "profile_client",
      primaryMenu: effectiveMenu,
      primaryMenuSource: "profile_client",
      shortcuts: { items: [] },
      isSupported: true,
      supportsAtomicShortcuts: true,
      isLoading: false,
    }));

    const view = render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));
    fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
    await waitFor(() => expect(setMutateAsync).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save menu" })).toBeDisabled());

    fireEvent.click(screen.getByRole("button", { name: "Remove Calendar" }));
    effectiveMenu = {
      items: [
        { type: "builtin" as const, destination: "home" as const },
        { type: "builtin" as const, destination: "calendar" as const },
      ],
    };
    view.rerender(<InterfaceSettings />);

    expect(screen.queryByText("Calendar")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save menu" })).toBeEnabled();
  });

  it("clears a just-saved override before its effective refetch arrives", async () => {
    mocks.useUICustomization.mockReturnValue({
      cardPresentation: { poster_size: "large", caption: "title" },
      cardPresentationSource: "profile_client",
      primaryMenu: {
        items: [
          { type: "builtin" as const, destination: "home" as const },
          { type: "library" as const, library_id: 7, label: "Movies" },
        ],
      },
      primaryMenuSource: "profile",
      shortcuts: { items: [] },
      isSupported: true,
      supportsAtomicShortcuts: true,
      isLoading: false,
    });

    render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));
    fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
    await waitFor(() => expect(setMutateAsync).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "Save menu" })).toBeDisabled());

    fireEvent.click(screen.getByRole("button", { name: "Reset to default" }));

    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.NAV_PRIMARY_MENU,
        identity: { scope: "profile_client" },
      }),
    );
  });

  it("discards an unsaved draft locally when no web-family override exists", async () => {
    const effectiveMenu = {
      items: [
        { type: "builtin" as const, destination: "home" as const },
        { type: "library" as const, library_id: 7, label: "Movies" },
      ],
    };
    mocks.useUICustomization.mockReturnValue({
      cardPresentation: { poster_size: "large", caption: "title" },
      cardPresentationSource: "profile_client",
      primaryMenu: effectiveMenu,
      primaryMenuSource: "profile",
      shortcuts: { items: [] },
      isSupported: true,
      supportsAtomicShortcuts: true,
      isLoading: false,
    });

    render(<InterfaceSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Remove Movies · Library" }));
    fireEvent.click(screen.getByRole("button", { name: "Reset to default" }));

    expect(clearMutateAsync).not.toHaveBeenCalled();
    expect(await screen.findByText("Movies · Library")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save menu" })).toBeDisabled();
  });

  it("hides revision-five controls when the server does not support them", () => {
    mocks.useUICustomization.mockReturnValue({
      cardPresentation: { poster_size: "standard", caption: "title_metadata" },
      cardPresentationSource: "default",
      primaryMenu: null,
      primaryMenuSource: "default",
      shortcuts: { items: [] },
      isSupported: false,
      supportsAtomicShortcuts: false,
      isLoading: false,
      isUnavailable: false,
    });

    render(<InterfaceSettings />);

    expect(screen.getByRole("alert")).toHaveTextContent("Server upgrade required");
    expect(screen.queryByRole("radiogroup", { name: "Poster size" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save menu" })).not.toBeInTheDocument();
  });

  it("keeps editors closed when effective customization could not be loaded", () => {
    mocks.useUICustomization.mockReturnValue({
      cardPresentation: { poster_size: "standard", caption: "title_metadata" },
      cardPresentationSource: "default",
      primaryMenu: null,
      primaryMenuSource: "default",
      shortcuts: { items: [] },
      isSupported: true,
      supportsAtomicShortcuts: true,
      isLoading: false,
      isUnavailable: true,
    });

    render(<InterfaceSettings />);

    expect(screen.getByRole("alert")).toHaveTextContent("Customization unavailable");
    expect(screen.getByRole("alert")).toHaveTextContent("Editing stays disabled");
    expect(screen.queryByRole("radiogroup", { name: "Poster size" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save menu" })).not.toBeInTheDocument();
    expect(setMutateAsync).not.toHaveBeenCalled();
    expect(clearMutateAsync).not.toHaveBeenCalled();
  });
});
