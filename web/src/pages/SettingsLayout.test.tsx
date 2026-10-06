import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
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
