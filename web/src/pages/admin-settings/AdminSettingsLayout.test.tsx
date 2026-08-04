import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AdminSettingsLayout from "./AdminSettingsLayout";

const mocks = vi.hoisted(() => ({
  useAdminServerStatus: vi.fn(),
}));

// The layout only needs the active tab's component to render; a loading form
// keeps every settings page on its skeleton state so no other hooks fire.
vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: () => ({
    isLoading: true,
    dirtyCount: 0,
    getValue: () => "",
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
  }),
}));

vi.mock("@/hooks/queries/admin/settings", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/admin/settings")>()),
  useAdminServerStatus: (...args: unknown[]) => mocks.useAdminServerStatus(...args),
}));

beforeEach(() => {
  mocks.useAdminServerStatus.mockReturnValue({ data: { restart_required: false } });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderLayout(search = "") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/admin/settings${search}`]}>
        <AdminSettingsLayout />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function renderInteractiveLayout(search = "") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/admin/settings${search}`]}>
        <AdminSettingsLayout />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("AdminSettingsLayout", () => {
  it("renders the grouped navigation sections", () => {
    const markup = renderLayout();

    for (const group of ["Server", "Media", "Connections", "Data"]) {
      expect(markup).toContain(`>${group}<`);
    }
  });

  it("renders desktop category jump links with section counts", () => {
    renderInteractiveLayout();

    const categoryNavigation = screen.getByRole("navigation", {
      name: "Admin settings sections categories",
    });

    expect(categoryNavigation).toContainElement(
      screen.getByRole("link", { name: "Server, 4 settings sections" }),
    );
    expect(screen.getByRole("link", { name: "Media, 7 settings sections" })).toHaveAttribute(
      "href",
      "#admin-settings-index-media",
    );
    expect(screen.getByRole("link", { name: "Connections, 6 settings sections" })).toHaveAttribute(
      "href",
      "#admin-settings-index-connections",
    );
    expect(screen.getByRole("link", { name: "Data, 3 settings sections" })).toHaveAttribute(
      "href",
      "#admin-settings-index-data",
    );
  });

  it("uses one desktop grid and card geometry for every settings group", () => {
    const markup = renderLayout();

    expect(markup.match(/2xl:grid-cols-4/g)).toHaveLength(4);
    expect(markup).not.toContain("2xl:grid-cols-3");
    expect(markup.match(/lg:h-28/g)).toHaveLength(20);
    expect(markup.match(/lg:line-clamp-3/g)).toHaveLength(20);
  });

  it("marks a selected category jump link as the current location", async () => {
    renderInteractiveLayout();

    const server = screen.getByRole("link", { name: "Server, 4 settings sections" });
    const data = screen.getByRole("link", { name: "Data, 3 settings sections" });

    expect(server).toHaveAttribute("aria-current", "location");
    await userEvent.click(data);

    expect(server).not.toHaveAttribute("aria-current");
    expect(data).toHaveAttribute("aria-current", "location");
  });

  it("renders every settings tab", () => {
    const markup = renderLayout();

    for (const label of [
      "General",
      "Branding",
      "Theming",
      "Card Overlays",
      "Scanner &amp; Matcher",
      "Search",
      "Intro Markers",
      "Subtitles",
      "AI Services",
      "Playback",
      "Downloads",
      "Watch Providers",
      "Integrations",
      "Email",
      "Notifications",
      "Compatibility Proxies",
      "Rate Limiting",
      "Database",
      "Storage",
      "Log Retention",
    ]) {
      expect(markup).toContain(label);
    }
  });

  it("renders the settings index at the root and preserves tab deep links", () => {
    renderInteractiveLayout();

    expect(screen.getByRole("link", { name: /General.*Authentication/ })).toHaveAttribute(
      "href",
      "/admin/settings?tab=general",
    );
    expect(screen.queryByRole("link", { name: "All settings" })).not.toBeInTheDocument();

    const detail = renderLayout("?tab=general");
    expect(detail).toContain('aria-current="page"');
    expect(detail).toContain('href="/admin/settings"');
  });

  it("focuses the detail heading and resets scroll when an overview link opens", async () => {
    const scrollTo = vi.fn();
    vi.stubGlobal("scrollTo", scrollTo);
    renderInteractiveLayout();

    await userEvent.click(screen.getByRole("link", { name: /Database.*Postgres/ }));

    const detailRegion = await screen.findByRole("region", { name: "Database settings" });
    expect(scrollTo).toHaveBeenCalledWith(0, 0);
    expect(detailRegion).toHaveFocus();
  });

  it("adds a mobile detail heading when the settings component has none", () => {
    vi.stubGlobal("scrollTo", vi.fn());

    renderInteractiveLayout("?tab=branding");

    expect(screen.getByRole("heading", { name: "Branding", level: 2 })).toHaveFocus();
  });

  it("resets the scrolling detail pane when switching admin tabs", async () => {
    vi.stubGlobal("scrollTo", vi.fn());
    renderInteractiveLayout("?tab=general");

    const generalRegion = screen.getByRole("region", { name: "General settings" });
    generalRegion.scrollTop = 400;

    await userEvent.click(screen.getByRole("button", { name: "Database" }));

    const databaseRegion = await screen.findByRole("region", { name: "Database settings" });
    expect(databaseRegion.scrollTop).toBe(0);
    expect(databaseRegion).toHaveFocus();
  });

  it("surfaces durable restart-required state above the active tab", () => {
    mocks.useAdminServerStatus.mockReturnValue({ data: { restart_required: true } });

    const markup = renderLayout();

    expect(markup).toContain("Server restart required for saved settings to take effect.");
  });

  it("resolves the legacy jellyfin tab alias to Compatibility Proxies", () => {
    const withAlias = renderLayout("?tab=jellyfin");
    const direct = renderLayout("?tab=compatibility-proxies");

    expect(withAlias).toBe(direct);
  });

  it("filters admin settings sections from the search box", async () => {
    renderInteractiveLayout();

    await userEvent.type(screen.getByRole("searchbox", { name: "Search settings" }), "redis");

    expect(screen.getAllByRole("link", { name: /Database/ })).toHaveLength(1);
    expect(screen.queryByRole("link", { name: /Playback/ })).not.toBeInTheDocument();
    expect(screen.getByText("1 match")).toBeInTheDocument();
  });

  it("matches individual admin setting labels", async () => {
    renderInteractiveLayout();

    await userEvent.type(
      screen.getByRole("searchbox", { name: "Search settings" }),
      "pool max open",
    );

    expect(screen.getAllByRole("link", { name: /Database/ })).toHaveLength(1);
    expect(screen.queryByRole("link", { name: /General/ })).not.toBeInTheDocument();
  });

  it("focuses admin settings search with Cmd+K", () => {
    renderInteractiveLayout();

    const searchBox = screen.getByRole("searchbox", { name: "Search settings" });
    fireEvent.keyDown(document, { key: "k", metaKey: true });

    expect(searchBox).toHaveFocus();
  });

  it("focuses admin settings search with Ctrl+K", () => {
    renderInteractiveLayout();

    const searchBox = screen.getByRole("searchbox", { name: "Search settings" });
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });

    expect(searchBox).toHaveFocus();
  });

  it("does not consume Cmd+K when the admin detail search is hidden", () => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({ matches: false })),
    );
    vi.stubGlobal("scrollTo", vi.fn());
    renderInteractiveLayout("?tab=general");

    const event = new KeyboardEvent("keydown", {
      key: "k",
      metaKey: true,
      cancelable: true,
    });

    expect(document.dispatchEvent(event)).toBe(true);
    expect(event.defaultPrevented).toBe(false);
  });
});
