import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
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
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("includes a PageBack control at the top of the page", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).toContain('aria-label="Go back"');
  });

  it("renders a grouped settings index at the root route", () => {
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(screen.getByRole("heading", { name: "Settings" })).toBeInTheDocument();
    for (const group of ["Playback", "Appearance", "Home & Discovery", "Connections", "Account"]) {
      expect(screen.getByRole("heading", { name: group })).toBeInTheDocument();
    }
    expect(
      screen.getByRole("link", { name: /Playback.*Quality, languages, skipping/ }),
    ).toHaveAttribute("href", "/settings/playback");
    expect(screen.getByRole("link", { name: /Connect Apps.*Sign-in details/ })).toBeInTheDocument();
  });

  it("names each settings group exactly once", () => {
    render(
      <MemoryRouter initialEntries={["/settings"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    // The category jump bar used to repeat every group name and count directly
    // above the headings that already carry them.
    expect(
      screen.queryByRole("navigation", { name: "Settings sections categories" }),
    ).not.toBeInTheDocument();
    for (const group of ["Playback", "Appearance", "Home & Discovery", "Connections", "Account"]) {
      expect(screen.getAllByRole("heading", { name: group })).toHaveLength(1);
      expect(
        screen.queryByRole("link", { name: new RegExp(`^${group}, \\d+ settings`) }),
      ).toBeNull();
    }
  });

  it("uses one desktop grid and card geometry for every settings group", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    // Five groups, seventeen sections — every card the same height so no group
    // is visually ranked above another.
    expect(markup.match(/2xl:grid-cols-4/g)).toHaveLength(5);
    expect(markup.match(/lg:h-28/g)).toHaveLength(17);
    expect(markup).not.toContain("max-w-5xl");
  });

  it("keeps each settings section in exactly one group", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    for (const path of ["devices", "libraries", "watch-providers", "profiles"]) {
      expect(markup.match(new RegExp(`href="/settings/${path}"`, "g"))).toHaveLength(1);
    }
  });

  it("offers a clear return to the settings index from detail pages", () => {
    render(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(screen.getByRole("link", { name: "All settings" })).toHaveAttribute("href", "/settings");
  });

  it("does not include a plugins section in personal settings", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("/settings/plugins");
    expect(markup).not.toContain(">Plugins<");
  });

  it("includes the profiles section in personal settings", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/profiles"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).toContain("/settings/profiles");
    expect(markup).toContain(">Profiles<");
  });

  it("includes the Webhook Sync section in personal settings", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/settings/webhook-sync"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    expect(markup).toContain("/settings/webhook-sync");
    expect(markup).toContain(">Webhook Sync<");
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

  it("matches individual personal setting labels", async () => {
    render(
      <MemoryRouter initialEntries={["/settings/playback"]}>
        <SettingsLayout />
      </MemoryRouter>,
    );

    await userEvent.type(screen.getByRole("searchbox", { name: "Search settings" }), "font family");

    expect(screen.getAllByRole("link", { name: /Subtitles/ })).toHaveLength(1);
    expect(screen.queryByRole("link", { name: /Playback/ })).not.toBeInTheDocument();
  });

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
});
