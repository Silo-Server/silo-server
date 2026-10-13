import { fireEvent, render, screen } from "@testing-library/react";
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

import SettingsLayout from "./SettingsLayout";

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
        <SettingsLayout />
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
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).toContain("/settings/profiles");
    expect(markup).toContain(">Profiles<");
    expect(markup).toContain("/settings/account");
    expect(markup).toContain("/settings/sessions");
  });

  it("filters personal settings sections from the search box", async () => {
    render(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

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
    render(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    await userEvent.type(screen.getByRole("searchbox", { name: "Search settings" }), "home rows");

    expect(screen.getAllByRole("link", { name: /Home Screen/ })).toHaveLength(1);
  });

  it.each(["hide watched items", "export layout", "import layout", "reset home"])(
    "finds Home Screen when searching for %s",
    async (query) => {
      render(
        <MemoryRouter initialEntries={["/settings/playback"]}>
          <SettingsLayout />
        </MemoryRouter>,
      );

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

  it("does not consume Cmd+K when the detail search is hidden", () => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({ matches: false })),
    );

    render(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    const event = new KeyboardEvent("keydown", {
      key: "k",
      metaKey: true,
      cancelable: true,
    });

    expect(document.dispatchEvent(event)).toBe(true);
    expect(event.defaultPrevented).toBe(false);
  });
  // jsdom can't measure layout; the real check is a 390px browser walk. The
  // app shell, the page shell and this pane each pad a phone by 16px, which
  // leaves Home Screen's rows too narrow to read their titles.
  it("lets Home Screen use the pane's full width on a phone", () => {
    function paneAt(path: string) {
      const view = render(
        <MemoryRouter initialEntries={[`/settings/${path}`]}>
          <Routes>
            <Route path="/settings/*" element={<SettingsLayout />}>
              <Route path="*" element={<div data-testid="settings-page" />} />
            </Route>
          </Routes>
        </MemoryRouter>,
      );
      const pane = screen.getByTestId("settings-page").parentElement?.parentElement;
      const classes = pane?.className ?? "";
      view.unmount();
      return classes;
    }

    expect(paneAt("home-screen")).toContain("max-sm:px-0");
    expect(paneAt("playback")).not.toContain("max-sm:px-0");
  });
});
