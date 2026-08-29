import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SettingsOverviewModel } from "@/hooks/admin/useSettingsOverview";
import { ADMIN_SETTINGS_NAV, LEGACY_ADMIN_SETTINGS_PAGE_ALIASES } from "@/lib/adminSettingsSearch";

import AdminSettingsLayout from "./AdminSettingsLayout";

const mocks = vi.hoisted(() => ({
  useAdminServerStatus: vi.fn(),
  useSettingsOverview: vi.fn(),
  // How many staged edits the open page claims; drives the shell's
  // unsaved-changes guard.
  dirtyCount: 0,
}));

// The layout only needs the active page's component to render; a loading form
// keeps every settings page on its skeleton state so no other hooks fire. The
// dirty registration is the one real behavior kept, because the shell's
// navigation guard reads it.
vi.mock("@/hooks/useSettingsForm", async () => {
  const { useReportUnsavedChanges } = await import("@/hooks/useUnsavedChanges");

  return {
    useSettingsForm: () => {
      useReportUnsavedChanges(mocks.dirtyCount > 0);

      return {
        isLoading: true,
        getValue: () => "",
        setValue: () => {},
        resetValue: () => {},
        dirtyCount: mocks.dirtyCount,
        dirtyKeys: [],
        isDirty: () => false,
        save: () => {},
        discard: () => {},
        isSaving: false,
        restartRequired: false,
        sensitiveConfigured: [],
        sensitiveManagedByEnv: [],
        sensitiveStatusReady: false,
        sensitiveStatusError: null,
        buildConnectionCheckRequest: () => ({}),
      };
    },
  };
});

vi.mock("@/hooks/queries/admin/settings", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/admin/settings")>()),
  useAdminServerStatus: (...args: unknown[]) => mocks.useAdminServerStatus(...args),
}));

vi.mock("@/hooks/admin/useSettingsOverview", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/admin/useSettingsOverview")>()),
  useSettingsOverview: () => mocks.useSettingsOverview(),
}));

function overviewModel(): SettingsOverviewModel {
  return {
    isLoading: false,
    tiles: [],
    cards: [],
  };
}

beforeEach(() => {
  mocks.useAdminServerStatus.mockReturnValue({ data: { restart_required: false } });
  mocks.useSettingsOverview.mockReturnValue(overviewModel());
  mocks.dirtyCount = 0;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// A data router, like the app itself mounts: the shell blocks navigation with
// `useBlocker`, which the declarative `<MemoryRouter>` cannot serve. The second
// route stands in for the rest of the admin area (the sidebar's targets).
function renderInteractiveLayout(suffix = "") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter(
    [
      { path: "/admin/settings/*", element: <AdminSettingsLayout /> },
      { path: "/admin/users", element: <h1>Admin users</h1> },
    ],
    { initialEntries: [`/admin/settings${suffix}`] },
  );

  return {
    router,
    ...render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    ),
  };
}

describe("AdminSettingsLayout", () => {
  it("lands on the overview at the settings index", () => {
    renderInteractiveLayout();

    expect(screen.getByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "All settings" })).not.toBeInTheDocument();
  });

  it("renders a settings category with the page rail beside it", () => {
    vi.stubGlobal("scrollTo", vi.fn());
    renderInteractiveLayout("/general");

    expect(screen.getByRole("region", { name: "General settings" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "All settings" })).toHaveAttribute(
      "href",
      "/admin/settings",
    );

    // The rail lists every settings page and marks the open one.
    const rail = screen.getByRole("navigation", { name: "Settings pages" });
    for (const item of ADMIN_SETTINGS_NAV) {
      const link = within(rail).getByRole("link", { name: item.label });
      expect(link).toHaveAttribute("href", `/admin/settings/${item.id}`);
    }
    expect(within(rail).getByRole("link", { name: "General" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("mounts every settings page at its own route", () => {
    vi.stubGlobal("scrollTo", vi.fn());

    for (const item of ADMIN_SETTINGS_NAV) {
      const { unmount } = renderInteractiveLayout(`/${item.id}`);

      expect(screen.getByRole("region", { name: `${item.label} settings` })).toBeInTheDocument();
      unmount();
    }
  });

  it("focuses the settings page and resets document scroll when it opens", async () => {
    const scrollTo = vi.fn();
    vi.stubGlobal("scrollTo", scrollTo);
    renderInteractiveLayout("/general");

    const region = await screen.findByRole("region", { name: "General settings" });
    expect(scrollTo).toHaveBeenCalledWith(0, 0);
    expect(region).toHaveFocus();
  });

  it("leaves the restart prompt to the admin shell", () => {
    vi.stubGlobal("scrollTo", vi.fn());
    mocks.useAdminServerStatus.mockReturnValue({ data: { restart_required: true } });

    renderInteractiveLayout("/general");

    // AdminLayout renders one banner above every admin page, so a restart owed
    // while settings is open must not add a second one here.
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();
  });

  it("redirects legacy query-string tabs to their canonical pages", async () => {
    vi.stubGlobal("scrollTo", vi.fn());

    for (const [legacy, current] of Object.entries(LEGACY_ADMIN_SETTINGS_PAGE_ALIASES)) {
      const label = ADMIN_SETTINGS_NAV.find((item) => item.id === current)?.label;
      expect(label).toBeDefined();

      const { unmount } = renderInteractiveLayout(`?tab=${legacy}`);
      expect(await screen.findByRole("region", { name: `${label} settings` })).toBeInTheDocument();
      unmount();
    }
  });

  it("redirects a retired page route to the page that absorbed it", async () => {
    vi.stubGlobal("scrollTo", vi.fn());
    renderInteractiveLayout("/jellyfin");

    expect(
      await screen.findByRole("region", { name: "Compatibility settings" }),
    ).toBeInTheDocument();
  });

  it("redirects an unknown settings page to the overview", async () => {
    vi.stubGlobal("scrollTo", vi.fn());
    renderInteractiveLayout("/not-a-page");

    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
  });

  it("keeps the rail filter inside the rail, clear of the fixed admin header controls", () => {
    vi.stubGlobal("scrollTo", vi.fn());
    renderInteractiveLayout("/general");

    const box = screen.getByRole("searchbox", { name: "Search settings" });
    const rail = screen.getByRole("navigation", { name: "Settings pages" });
    const aside = rail.closest("aside");

    expect(aside).not.toBeNull();
    expect(aside).toContainElement(box);
    // AdminLayout floats its own Search ⌘K control over the top-right corner,
    // so the back-link row has to stay free of a second search input.
    expect(screen.getByRole("link", { name: "All settings" }).parentElement).not.toContainElement(
      box,
    );
  });

  it("filters the page rail from the settings search box", async () => {
    vi.stubGlobal("scrollTo", vi.fn());
    renderInteractiveLayout("/general");

    const box = screen.getByRole("searchbox", { name: "Search settings" });
    await userEvent.type(box, "transcode");

    const rail = screen.getByRole("navigation", { name: "Settings pages" });
    expect(within(rail).getByRole("link", { name: "Playback" })).toBeInTheDocument();
    expect(within(rail).queryByRole("link", { name: "General" })).not.toBeInTheDocument();

    await userEvent.clear(box);
    expect(within(rail).getByRole("link", { name: "General" })).toBeInTheDocument();
  });

  it("keeps `ai` pointing at the AI Services page rather than an alias", () => {
    vi.stubGlobal("scrollTo", vi.fn());
    renderInteractiveLayout("/ai");

    expect(screen.getByRole("region", { name: "AI Services settings" })).toBeInTheDocument();
  });
});

describe("AdminSettingsLayout unsaved-changes guard", () => {
  // Radix marks the page behind an open modal inert and aria-hidden, so while
  // the prompt is up the page under it is only reachable by text.
  const user = userEvent.setup({ pointerEventsCheck: 0 });

  beforeEach(() => {
    vi.stubGlobal("scrollTo", vi.fn());
  });

  it("lets a clean page move through the rail untouched", async () => {
    renderInteractiveLayout("/general");

    const rail = screen.getByRole("navigation", { name: "Settings pages" });
    await user.click(within(rail).getByRole("link", { name: "Playback" }));

    expect(screen.getByRole("region", { name: "Playback settings" })).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("holds a rail navigation until staged edits are discarded", async () => {
    mocks.dirtyCount = 1;
    const { router } = renderInteractiveLayout("/general");

    const rail = screen.getByRole("navigation", { name: "Settings pages" });
    await user.click(within(rail).getByRole("link", { name: "Playback" }));

    expect(
      await screen.findByRole("alertdialog", { name: "Discard unsaved changes?" }),
    ).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/settings/general");

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("region", { name: "General settings" })).toBeInTheDocument();

    await user.click(within(rail).getByRole("link", { name: "Playback" }));
    await user.click(await screen.findByRole("button", { name: "Discard" }));

    expect(await screen.findByRole("region", { name: "Playback settings" })).toBeInTheDocument();
  });

  it("guards the back link out of the settings shell", async () => {
    mocks.dirtyCount = 2;
    renderInteractiveLayout("/general");

    await user.click(screen.getByRole("link", { name: "All settings" }));

    expect(await screen.findByRole("alertdialog")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Discard" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
  });

  it("guards a navigation that leaves the settings area entirely", async () => {
    mocks.dirtyCount = 1;
    const { router } = renderInteractiveLayout("/general");

    // Stands in for the admin sidebar and for browser back: both reach the
    // router the same way.
    await act(async () => {
      await router.navigate("/admin/users");
    });

    expect(await screen.findByRole("alertdialog")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("region", { name: "General settings" })).toBeInTheDocument();
  });
});
