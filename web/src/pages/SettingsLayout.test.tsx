import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
  requestStatus: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: () => mocks.requestStatus(),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: (...args: unknown[]) => mocks.useAuth(...args),
  useOptionalAuth: (...args: unknown[]) => mocks.useAuth(...args),
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: mocks.useAuth()?.profile ?? null }),
}));

import UserSettingsSidebar from "@/components/settings/UserSettingsSidebar";

import SettingsLayout from "./SettingsLayout";

// The settings route as the app mounts it: `Layout` renders the settings
// sidebar in the app sidebar's place, beside the routed settings page.
function SettingsShell() {
  return (
    <>
      <UserSettingsSidebar backTo="/movies" />
      <SettingsLayout />
    </>
  );
}

function renderSettings(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/settings/*" element={<SettingsShell />}>
          <Route path="*" element={<div data-testid="settings-page" />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

function stubDesktop(matches: boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => ({ matches, addEventListener: vi.fn(), removeEventListener: vi.fn() })),
  );
}

describe("SettingsLayout", () => {
  beforeEach(() => {
    mocks.useAuth.mockReset();
    mocks.useAuth.mockReturnValue({
      user: { role: "admin" },
    });
    mocks.requestStatus.mockReturnValue({ data: { requests_enabled: true } });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists Requests only while the server has requests on", () => {
    const renderIndex = () =>
      renderToStaticMarkup(
        <MemoryRouter initialEntries={["/settings"]}>
          <SettingsLayout />
        </MemoryRouter>,
      );

    expect(renderIndex()).toContain('href="/settings/requests"');
    mocks.requestStatus.mockReturnValue({ data: { requests_enabled: false } });
    expect(renderIndex()).not.toContain('href="/settings/requests"');
    mocks.requestStatus.mockReturnValue({ data: undefined });
    expect(renderIndex()).not.toContain('href="/settings/requests"');
  });

  it("hides the profiles section for non-admin users without a primary profile", () => {
    mocks.useAuth.mockReturnValue({
      user: { role: "user" },
      profile: { is_primary: false },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsShell />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("/settings/profiles");
    expect(markup).not.toContain(">Profiles<");
    expect(markup).not.toContain("/settings/account");
    expect(markup).not.toContain("/settings/sessions");
  });

  it("shows the profiles section for non-admin users on their primary profile", () => {
    mocks.useAuth.mockReturnValue({
      user: { role: "user" },
      profile: { is_primary: true },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/profiles"]}>
        <SettingsShell />
      </MemoryRouter>,
    );

    expect(markup).toContain("/settings/profiles");
    expect(markup).toContain(">Profiles<");
    expect(markup).toContain("/settings/account");
    expect(markup).toContain("/settings/sessions");
  });

  it("filters personal settings sections from the search box", async () => {
    renderSettings("/settings/playback");

    await userEvent.type(screen.getByRole("searchbox", { name: "Search settings" }), "pin");

    // "pin" hits Profiles (where PINs are set), Connect Apps (where the
    // password#PIN format is explained), and Navigation & Cards (where
    // libraries are pinned to the primary menu).
    expect(screen.getAllByRole("link", { name: /Profiles/ })).toHaveLength(1);
    expect(screen.getAllByRole("link", { name: /Connect Apps/ })).toHaveLength(1);
    expect(screen.getAllByRole("link", { name: /Navigation & Cards/ })).toHaveLength(1);
    expect(screen.queryByRole("link", { name: /Playback/ })).not.toBeInTheDocument();
    expect(screen.getByText("3 matches")).toBeInTheDocument();
  });

  it("finds Home Screen when searching for home rows", async () => {
    renderSettings("/settings/playback");

    await userEvent.type(screen.getByRole("searchbox", { name: "Search settings" }), "home rows");

    expect(screen.getAllByRole("link", { name: /Home Screen/ })).toHaveLength(1);
  });

  it.each(["hide watched items", "export layout", "import layout", "reset home"])(
    "finds Home Screen when searching for %s",
    async (query) => {
      renderSettings("/settings/playback");

      await userEvent.type(screen.getByRole("searchbox", { name: "Search settings" }), query);

      expect(screen.getAllByRole("link", { name: /Home Screen/ })).toHaveLength(1);
    },
  );

  it("focuses personal settings search with Cmd+K", () => {
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    const searchBox = screen.getByRole("searchbox", { name: "Search settings" });
    fireEvent.keyDown(document, { key: "k", metaKey: true });

    expect(searchBox).toHaveFocus();
  });

  it("focuses personal settings search with Ctrl+K", () => {
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    const searchBox = screen.getByRole("searchbox", { name: "Search settings" });
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });

    expect(searchBox).toHaveFocus();
  });

  it("does not consume Cmd+K while the settings sidebar is hidden", () => {
    stubDesktop(false);
    renderSettings("/settings/playback");

    const event = new KeyboardEvent("keydown", {
      key: "k",
      metaKey: true,
      cancelable: true,
    });

    expect(document.dispatchEvent(event)).toBe(true);
    expect(event.defaultPrevented).toBe(false);
  });
  it("opens the first settings page at the root on desktop", () => {
    stubDesktop(true);
    renderSettings("/settings");

    expect(screen.getByTestId("settings-page")).toBeInTheDocument();
    const sidebar = screen.getByRole("navigation", { name: "Settings" });
    expect(within(sidebar).getByRole("link", { name: "Playback" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("lists every settings page at the root on a phone", () => {
    stubDesktop(false);
    renderSettings("/settings");

    const index = screen.getByRole("navigation", { name: "Settings pages" });
    expect(within(index).getByRole("link", { name: /^Playback/ })).toHaveAttribute(
      "href",
      "/settings/playback",
    );
    expect(screen.queryByTestId("settings-page")).not.toBeInTheDocument();
  });

  it("leads back to the page settings were opened from", () => {
    renderSettings("/settings/playback");

    expect(screen.getByRole("link", { name: "Back to App" })).toHaveAttribute("href", "/movies");
  });
});
