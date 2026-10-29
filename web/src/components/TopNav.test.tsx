// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { buildPersonalCatalogHref } from "@/pages/catalogSearchParams";
import { OPEN_GLOBAL_SEARCH_EVENT } from "./GlobalSearch";
import TopNav from "./TopNav";

const mockLogout = vi.fn();
const mockClearProfile = vi.fn();
const mockNavigate = vi.fn();

let mockPrimaryMenu: { items: Array<Record<string, unknown>> } | null = null;
let mockLibraries = [{ id: 7, name: "Movies", type: "movies" }];
let mockRequestsEnabled = true;
let mockIsAdmin = false;
/** Drives the bar's `md` breakpoint: true is the dropdown, false the sheet. */
let mockTabletUp = true;

vi.mock("@/hooks/useAuth", () => {
  const useAuth = () => ({
    user: { id: 1, username: "alex", role: "admin" },
    logout: mockLogout,
    clearProfile: mockClearProfile,
  });
  return { useAuth, useOptionalAuth: useAuth };
});

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { name: "Alex" } }),
}));

vi.mock("@/hooks/useIsActingAdmin", () => ({
  useIsActingAdmin: () => mockIsAdmin,
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: () => ({ data: mockLibraries }),
}));

vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ primaryMenu: mockPrimaryMenu }),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: () => ({ data: { requests_enabled: mockRequestsEnabled } }),
}));

vi.mock("@/hooks/queries/notificationWebhooks", () => ({
  useNotificationCapability: () => ({ data: { in_app: { enabled: true } }, isError: false }),
}));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => mockNavigate,
}));

// The real brand reads the branding document, which needs its provider; the
// shell only cares that the logo links home.
vi.mock("@/components/SiloBrand", () => ({
  SiloBrand: () => <span data-testid="brand" />,
}));

function renderTopNav() {
  return render(
    <MemoryRouter>
      <TopNav />
    </MemoryRouter>,
  );
}

describe("TopNav", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockPrimaryMenu = null;
    mockLibraries = [{ id: 7, name: "Movies", type: "movies" }];
    mockRequestsEnabled = true;
    mockIsAdmin = false;
    mockTabletUp = true;
    // jsdom has no matchMedia; the shell reads it to pick its phone layout.
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: mockTabletUp && query === "(min-width: 48rem)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("links the logo home instead of repeating Home as a link", () => {
    mockPrimaryMenu = {
      items: [
        { type: "builtin", destination: "home" },
        { type: "builtin", destination: "for_you" },
      ],
    };
    renderTopNav();

    // The logo is the only Home affordance; a second one would duplicate it.
    expect(screen.getAllByRole("link", { name: "Home" })).toHaveLength(1);
    expect(screen.getByRole("link", { name: "For You" })).toHaveAttribute(
      "href",
      "/recommendations",
    );
  });

  it("resolves the profile's library menu items into links", () => {
    mockPrimaryMenu = { items: [{ type: "library", library_id: 7, label: "Movies" }] };
    renderTopNav();

    expect(screen.getByRole("link", { name: "Movies" })).toHaveAttribute("href", "/library/7");
  });

  it("points Discover at the requests hub", () => {
    renderTopNav();
    expect(screen.getByRole("link", { name: "Discover" })).toHaveAttribute("href", "/requests");
  });

  it("drops Discover when the server has requests turned off", () => {
    mockRequestsEnabled = false;
    renderTopNav();
    expect(screen.queryByRole("link", { name: "Discover" })).not.toBeInTheDocument();
  });

  it("links the icons to the personal catalog destinations", () => {
    renderTopNav();

    expect(screen.getByRole("link", { name: "Favorites" })).toHaveAttribute(
      "href",
      buildPersonalCatalogHref("favorites"),
    );
    expect(screen.getByRole("link", { name: "Watchlist" })).toHaveAttribute(
      "href",
      buildPersonalCatalogHref("watchlist"),
    );
  });

  it("asks the existing search to open from its own button", async () => {
    const opened = vi.fn();
    document.addEventListener(OPEN_GLOBAL_SEARCH_EVENT, opened);
    renderTopNav();

    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    expect(opened).toHaveBeenCalledTimes(1);
    document.removeEventListener(OPEN_GLOBAL_SEARCH_EVENT, opened);
  });

  it("gates the Admin entry on the acting-admin flag", async () => {
    const user = userEvent.setup();
    renderTopNav();

    await user.click(screen.getByRole("button", { name: /account menu/ }));
    expect(screen.getByText("Your account")).toBeInTheDocument();
    expect(screen.getByText("Profiles")).toBeInTheDocument();
    expect(screen.getByText("Watch party")).toBeInTheDocument();
    expect(screen.queryByText("Admin")).not.toBeInTheDocument();
  });

  it("offers Admin to an acting admin", async () => {
    mockIsAdmin = true;
    const user = userEvent.setup();
    renderTopNav();

    await user.click(screen.getByRole("button", { name: /account menu/ }));

    expect(screen.getByText("Admin")).toBeInTheDocument();
  });

  it("keeps every link reachable in the phone chip row", () => {
    mockTabletUp = false;
    mockPrimaryMenu = { items: [{ type: "library", library_id: 7, label: "Movies" }] };
    renderTopNav();

    expect(screen.getByRole("link", { name: "Movies" })).toHaveAttribute("href", "/library/7");
    expect(screen.getByRole("link", { name: "Discover" })).toHaveAttribute("href", "/requests");
    expect(screen.getByRole("link", { name: "Home" })).toBeInTheDocument();
  });

  it("opens the account menu as a sheet on a phone viewport", async () => {
    mockTabletUp = false;
    const user = userEvent.setup();
    renderTopNav();

    await user.click(screen.getByRole("button", { name: /account menu/ }));

    // Both columns still arrive, just in the sheet rather than a dropdown.
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("Your account")).toBeInTheDocument();
    expect(screen.getByText("Profiles")).toBeInTheDocument();
  });
});
