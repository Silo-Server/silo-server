import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AdminSettingsLayout from "./AdminSettingsLayout";

const mocks = vi.hoisted(() => ({
  useAdminServerStatus: vi.fn(),
}));

// The layout only needs the active tab's component to render; a loading form
// keeps every settings page on its skeleton state so no other hooks fire.
vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: () => ({
    isLoading: true,
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
});
