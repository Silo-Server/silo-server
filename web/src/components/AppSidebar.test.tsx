// @vitest-environment jsdom

import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginSettingsInstallation } from "@/hooks/queries/pluginSettings";

import AppSidebar from "./AppSidebar";
import { groupAppNavLinks, isSidebarRailCollapsed, type AppNavLink } from "./AppSidebar.logic";

const mockLogout = vi.fn();
const mockClearProfile = vi.fn();
const mockTogglePin = vi.fn();
let mockPrimaryMenu: {
  items: Array<
    | {
        type: "builtin";
        destination: "home" | "movies" | "series" | "music" | "audiobooks";
      }
    | { type: "section"; library_id: number; section_id: string; label: string }
    | {
        type: "collection";
        library_id?: number;
        collection_id: string;
        label: string;
      }
  >;
} | null = null;
let mockLibraries = [{ id: 7, name: "Movies", type: "movies" }];
let mockRequestsEnabled = false;
let mockProfileRequestsAllowed = true;
let mockMyRequests: Array<{ id: string }> = [];

vi.mock("@/hooks/useAuth", () => {
  const useAuth = () => ({
    user: { id: 1, username: "alex", role: "admin" },
    logout: mockLogout,
    clearProfile: mockClearProfile,
  });
  return { useAuth, useOptionalAuth: useAuth };
});

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({
    profile: { name: "Alex", requests_allowed: mockProfileRequestsAllowed },
  }),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: () => ({
    data: mockLibraries,
  }),
}));

vi.mock("@/hooks/queries/sidebarPins", () => ({
  useSidebarPins: () => ({
    pins: {
      "7": [{ type: "section", id: "featured", label: "Featured" }],
    },
  }),
  useToggleSidebarPin: () => ({
    togglePin: mockTogglePin,
    canToggle: true,
  }),
}));

vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({
    primaryMenu: mockPrimaryMenu,
    shortcuts: { items: [] },
    cardPresentation: { poster_size: "standard", caption: "title_metadata" },
    isLoading: false,
  }),
}));

let mockPluginInstallations: PluginSettingsInstallation[] = [];

vi.mock("@/hooks/queries/pluginSettings", () => ({
  usePluginSettingsList: () => ({
    data: { installations: mockPluginInstallations },
  }),
}));

function pluginInstallation(
  id: number,
  pluginId: string,
  label: string,
  category?: string,
): PluginSettingsInstallation {
  return {
    id,
    plugin_id: pluginId,
    version: "1.0.0",
    user_config_schema: [],
    routes: [
      {
        id: "home",
        method: "GET",
        path: "/",
        access: "user",
        navigable: true,
        navigation_label: label,
        navigation_kind: "user",
        static_asset: false,
      },
    ],
    assets: [],
    category,
  };
}

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: () => ({
    data: { requests_enabled: mockRequestsEnabled },
  }),
  useMyMediaRequests: (_params: unknown, options: { enabled?: boolean }) => ({
    data: options.enabled ? mockMyRequests : undefined,
  }),
}));

vi.mock("@/hooks/queries/notifications", () => ({
  useUnreadNotificationCount: () => ({ data: 0 }),
}));

let mockNotificationsEnabled = true;

vi.mock("@/hooks/queries/notificationWebhooks", () => ({
  useNotificationCapability: () => ({
    data: { in_app: { enabled: mockNotificationsEnabled } },
    isError: false,
  }),
}));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => vi.fn(),
}));

vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({
    serverName: "Silo",
  }),
}));

vi.mock("@/components/ui/avatar", () => ({
  Avatar: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  AvatarFallback: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("@/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuTrigger: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuItem: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuLabel: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuSeparator: () => <hr />,
}));

function renderSidebar(entry: string, { collapsed = false }: { collapsed?: boolean } = {}) {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="*" element={<AppSidebar collapsed={collapsed} />} />
      </Routes>
    </MemoryRouter>,
  );
}

function parseMarkup(markup: string): Document {
  return new DOMParser().parseFromString(markup, "text/html");
}

const SEARCH_HREF = "/catalog?source=query";

function hrefsIn(list: Element | null | undefined) {
  return [...(list?.querySelectorAll("a") ?? [])].map((link) => link.getAttribute("href"));
}

/** Links in the list at the top of the nav, above every section header. */
function primaryMenuHrefs(document: Document) {
  return hrefsIn(document.querySelector('nav[aria-label="Main navigation"] > ul'));
}

/** Links in the list under a plain (non-collapsible) section header. */
function sectionHrefs(document: Document, title: string) {
  const heading = [...document.querySelectorAll("nav div")].find(
    (element) => element.children.length === 0 && element.textContent === title,
  );
  return hrefsIn(heading?.parentElement?.nextElementSibling);
}

describe("AppSidebar", () => {
  beforeEach(() => {
    mockLogout.mockReset();
    mockClearProfile.mockReset();
    mockTogglePin.mockReset();
    mockPluginInstallations = [];
    mockPrimaryMenu = null;
    mockLibraries = [{ id: 7, name: "Movies", type: "movies" }];
    mockNotificationsEnabled = true;
    mockRequestsEnabled = false;
    mockProfileRequestsAllowed = true;
    mockMyRequests = [];
  });

  it("links Requests for a blocked profile only while it has requests to manage", () => {
    const hasRequestsLink = () =>
      parseMarkup(renderSidebar("/")).querySelector('a[href="/requests"]') !== null;
    mockRequestsEnabled = true;
    expect(hasRequestsLink()).toBe(true);

    mockProfileRequestsAllowed = false;
    expect(hasRequestsLink()).toBe(false);

    mockMyRequests = [{ id: "r1" }];
    expect(hasRequestsLink()).toBe(true);

    mockRequestsEnabled = false;
    expect(hasRequestsLink()).toBe(false);
  });

  it("puts Search directly under Home, above the libraries", () => {
    for (const collapsed of [false, true]) {
      const document = parseMarkup(renderSidebar("/", { collapsed }));

      expect(primaryMenuHrefs(document)).toEqual(["/", SEARCH_HREF]);
      expect(document.querySelectorAll(`a[href="${SEARCH_HREF}"]`)).toHaveLength(1);
      expect(sectionHrefs(document, "Discover")).toEqual([
        "/recommendations",
        "/calendar",
        "/notifications",
      ]);
    }
  });

  it("keeps Search after a customized primary menu, in the menu's own order", () => {
    mockPrimaryMenu = {
      items: [
        { type: "collection", collection_id: "global-collection", label: "Global collection" },
        { type: "builtin", destination: "home" },
      ],
    };

    const hrefs = primaryMenuHrefs(parseMarkup(renderSidebar("/")));

    expect(hrefs).toHaveLength(3);
    expect(hrefs[0]).toContain("collection_id=global-collection");
    expect(hrefs.slice(1)).toEqual(["/", SEARCH_HREF]);
  });

  it("hides Discover when a customized primary menu leaves it nothing to show", () => {
    mockPrimaryMenu = { items: [{ type: "builtin", destination: "home" }] };

    const withNotifications = parseMarkup(renderSidebar("/"));
    expect(sectionHrefs(withNotifications, "Discover")).toEqual(["/notifications"]);

    mockNotificationsEnabled = false;
    const markup = renderSidebar("/");

    expect(markup).not.toContain(">Discover<");
    expect(primaryMenuHrefs(parseMarkup(markup))).toEqual(["/", SEARCH_HREF]);

    mockRequestsEnabled = true;
    const withRequests = parseMarkup(renderSidebar("/"));
    expect(sectionHrefs(withRequests, "Discover")).toEqual(["/requests"]);
  });

  it("drops library-scoped menu targets when their library is not visible", () => {
    mockPrimaryMenu = {
      items: [
        { type: "builtin", destination: "home" },
        {
          type: "section",
          library_id: 99,
          section_id: "hidden-section",
          label: "Hidden section",
        },
        {
          type: "collection",
          library_id: 99,
          collection_id: "hidden-collection",
          label: "Hidden collection",
        },
        {
          type: "collection",
          collection_id: "global-collection",
          label: "Global collection",
        },
      ],
    };

    const markup = renderSidebar("/");

    expect(markup).not.toContain("Hidden section");
    expect(markup).not.toContain("Hidden collection");
    expect(markup).toContain("Global collection");
  });

  it("does not reinterpret global media built-ins as the first matching library", () => {
    mockLibraries = [
      { id: 7, name: "Movies A", type: "movies" },
      { id: 8, name: "Movies B", type: "movie" },
      { id: 9, name: "TV A", type: "series" },
      { id: 10, name: "TV B", type: "shows" },
      { id: 11, name: "Books A", type: "audiobooks" },
      { id: 12, name: "Books B", type: "audiobook" },
      { id: 13, name: "Music A", type: "music" },
      { id: 14, name: "Music B", type: "music" },
    ];
    mockPrimaryMenu = {
      items: [
        { type: "builtin", destination: "home" },
        { type: "builtin", destination: "movies" },
        { type: "builtin", destination: "series" },
        { type: "builtin", destination: "audiobooks" },
        { type: "builtin", destination: "music" },
      ],
    };

    const document = parseMarkup(renderSidebar("/"));

    expect(primaryMenuHrefs(document)).toEqual(["/", SEARCH_HREF]);
    expect(document.querySelector('a[href="/library/7"]')).not.toBeNull();
    expect(document.querySelector('a[href="/library/14"]')).not.toBeNull();
  });

  it("keeps a custom section active while sort and filter params change", () => {
    mockPrimaryMenu = {
      items: [
        {
          type: "section",
          library_id: 7,
          section_id: "recent",
          label: "Custom Recently Added",
        },
      ],
    };

    const document = parseMarkup(
      renderSidebar(
        "/catalog?source=section&scope=library&library_id=7&section_id=recent&sort=title&order=asc&genre=Drama",
      ),
    );
    const link = [...document.querySelectorAll("a")].find((candidate) =>
      candidate.textContent?.includes("Custom Recently Added"),
    );

    expect(link?.getAttribute("aria-current")).toBe("page");
  });

  it("matches custom collections by source, library, and collection id", () => {
    mockPrimaryMenu = {
      items: [
        {
          type: "collection",
          library_id: 7,
          collection_id: "favorites",
          label: "Custom Favorites",
        },
      ],
    };

    const filtered = parseMarkup(
      renderSidebar(
        "/catalog?source=library_collection&library_id=7&collection_id=favorites&type=movie&sort=year&order=desc",
      ),
    );
    const otherLibrary = parseMarkup(
      renderSidebar(
        "/catalog?source=library_collection&library_id=8&collection_id=favorites&sort=year",
      ),
    );
    const findCustomLink = (document: Document) =>
      [...document.querySelectorAll("a")].find((candidate) =>
        candidate.textContent?.includes("Custom Favorites"),
      );

    expect(findCustomLink(filtered)?.getAttribute("aria-current")).toBe("page");
    expect(findCustomLink(otherLibrary)?.hasAttribute("aria-current")).toBe(false);
  });

  it("preserves the current library query when linking to the active library", () => {
    const markup = renderSidebar("/library/7?tab=library&sort=year&order=desc");

    expect(markup).toContain('href="/library/7?tab=library&amp;sort=year&amp;order=desc"');
  });

  it("keeps a flat Apps list when fewer than 2 distinct categories exist", () => {
    mockPluginInstallations = [
      pluginInstallation(1, "alpha-app", "Alpha", "Tools/Utilities"),
      pluginInstallation(2, "beta-app", "Beta", "Tools"),
    ];

    const markup = renderSidebar("/");

    expect(markup).toContain(">Apps<");
    expect(markup).toContain(">Alpha<");
    expect(markup).toContain(">Beta<");
    // Both plugins share the first category segment "Tools", so no
    // per-category sub-headers should render.
    expect(markup).not.toContain(">Tools<");
    expect(markup).not.toContain(">Other<");
  });
});

describe("sidebar collapse surface", () => {
  it("marks the surface collapsed only on a detail route", () => {
    expect(renderSidebar("/item/42", { collapsed: true })).toContain('data-collapsed="true"');
    expect(renderSidebar("/")).not.toContain("data-collapsed");
  });
});

describe("isSidebarRailCollapsed", () => {
  it("clips to the rail only when the route collapsed it and nothing holds it open", () => {
    expect(isSidebarRailCollapsed(true, false)).toBe(true);
    expect(isSidebarRailCollapsed(false, true)).toBe(false);
    // Hover-to-expand and the profile menu both re-open the clip.
    expect(isSidebarRailCollapsed(true, true)).toBe(false);
  });
});

describe("groupAppNavLinks", () => {
  const link = (id: string, category?: string): AppNavLink => ({
    id,
    basePath: `/api/v2/plugin-content/plugins/${id}`,
    label: id,
    pluginId: id,
    category,
  });

  it("treats blank or slash-only categories as uncategorized", () => {
    const groups = groupAppNavLinks([link("a", "  "), link("b", "/Tools"), link("c", "Extras")]);

    expect(groups?.map((g) => g.category)).toEqual(["Extras", "Other"]);
    expect(groups?.[1]?.links.map((l) => l.id)).toEqual(["a", "b"]);
  });
});
