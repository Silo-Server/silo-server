// @vitest-environment jsdom

import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginSettingsSummary } from "@/api/types";

import AppSidebar from "./AppSidebar";
import {
  getProfileMenuSide,
  groupAppNavLinks,
  isSidebarExpanded,
  isSidebarRailCollapsed,
  libraryRowShift,
  sidebarSurfaceStyle,
  type AppNavLink,
} from "./AppSidebar.logic";

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
    profile: { name: "Alex" },
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

let mockPluginInstallations: PluginSettingsSummary[] = [];

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
): PluginSettingsSummary {
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
    data: { requests_enabled: false },
  }),
}));

vi.mock("@/hooks/queries/notifications", () => ({
  useUnreadNotificationCount: () => ({ data: 0 }),
}));

vi.mock("@/hooks/queries/notificationWebhooks", () => ({
  useNotificationCapability: () => ({ data: { in_app: { enabled: true } }, isError: false }),
}));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => vi.fn(),
}));

vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({
    serverName: "Silo",
  }),
}));

vi.mock("@/hooks/useTheme", () => ({
  useTheme: () => ({
    theme: "dark",
    activeTheme: "dark",
    setTheme: vi.fn(),
    previewTheme: vi.fn(),
    resetPreviewTheme: vi.fn(),
  }),
  // SiloBrand reads the appearance through the optional hook; null keeps it on
  // the dark built-in assets, matching the sidebar's own surface.
  useOptionalTheme: () => null,
}));

vi.mock("@/components/ThemeSwitcher", () => ({
  default: () => <div>Theme switcher</div>,
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

describe("AppSidebar", () => {
  beforeEach(() => {
    mockLogout.mockReset();
    mockClearProfile.mockReset();
    mockTogglePin.mockReset();
    mockPluginInstallations = [];
    mockPrimaryMenu = null;
    mockLibraries = [{ id: 7, name: "Movies", type: "movies" }];
  });

  it("uses the cinema highlight text color for active catalog source links", () => {
    const markup = renderSidebar("/catalog?source=query&q=heat");

    expect(markup).toContain("text-sidebar-accent-foreground bg-sidebar-accent");
    expect(markup).not.toContain("text-sidebar-primary-foreground bg-sidebar-accent");
  });

  it("renders the Silo brand mark instead of the old play glyph", () => {
    const markup = renderSidebar("/");

    expect(markup).toContain('src="/silo-wordmark-sidebar.png"');
    expect(markup).toContain('alt="Silo"');
    expect(markup).not.toContain("▶");
  });

  it("cross-fades the wordmark and the mark instead of swapping them", () => {
    // Both are always rendered and only their opacity differs; swapping the
    // variant outright popped at the start of the paired transform transition.
    const collapsed = renderSidebar("/", { collapsed: true });
    const expanded = renderSidebar("/");

    for (const markup of [collapsed, expanded]) {
      expect(markup).toContain('src="/silo-icon-1024.png"');
      expect(markup).toContain('src="/silo-wordmark-sidebar.png"');
    }
    // Collapsed: the mark is the one showing, and the wordmark is out of the
    // accessibility tree so the two images never both name the sidebar.
    const collapsedDocument = parseMarkup(collapsed);
    const expandedDocument = parseMarkup(expanded);
    const collapsedWordmark = collapsedDocument
      .querySelector('img[src="/silo-wordmark-sidebar.png"]')
      ?.closest(".sidebar-fade");
    const collapsedMark = collapsedDocument
      .querySelector('img[src="/silo-icon-1024.png"]')
      ?.closest(".sidebar-fade");
    const expandedWordmark = expandedDocument
      .querySelector('img[src="/silo-wordmark-sidebar.png"]')
      ?.closest(".sidebar-fade");
    const expandedMark = expandedDocument
      .querySelector('img[src="/silo-icon-1024.png"]')
      ?.closest(".sidebar-fade");

    expect(collapsedWordmark?.getAttribute("aria-hidden")).toBe("true");
    expect(collapsedWordmark?.classList.contains("left-5")).toBe(true);
    expect(collapsedWordmark?.classList.contains("opacity-0")).toBe(true);
    expect(collapsedMark?.getAttribute("aria-hidden")).toBe("false");
    expect(collapsedMark?.classList.contains("left-3.5")).toBe(true);
    expect(collapsedMark?.classList.contains("opacity-100")).toBe(true);
    expect(expandedWordmark?.getAttribute("aria-hidden")).toBe("false");
    expect(expandedWordmark?.classList.contains("left-5")).toBe(true);
    expect(expandedWordmark?.classList.contains("opacity-100")).toBe(true);
    expect(expandedMark?.getAttribute("aria-hidden")).toBe("true");
    expect(expandedMark?.classList.contains("left-3.5")).toBe(true);
    expect(expandedMark?.classList.contains("opacity-0")).toBe(true);
  });

  it("uses the cinema highlight text color for active pinned catalog destinations", () => {
    const markup = renderSidebar(
      "/catalog?source=section&scope=library&library_id=7&section_id=featured&title=Featured",
    );

    expect(markup).toContain("text-sidebar-accent-foreground bg-sidebar-accent");
    expect(markup).not.toContain("text-sidebar-primary-foreground bg-sidebar-accent");
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
    const primaryMenu = document.querySelector('nav[aria-label="Main navigation"] > ul');
    const primaryHrefs = [...(primaryMenu?.querySelectorAll("a") ?? [])].map((link) =>
      link.getAttribute("href"),
    );

    expect(primaryHrefs).toEqual(["/"]);
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

  it("keeps collapsed navigation rows left-anchored instead of centering icons", () => {
    const markup = renderSidebar("/item/42", { collapsed: true });

    expect(markup).toContain('href="/"');
    expect(markup).toContain('class="relative flex items-center gap-2.5 rounded-xl px-3 py-3');
  });

  it("preserves section header slots when collapsed so nav groups do not shift upward", () => {
    const markup = renderSidebar("/item/42", { collapsed: true });

    expect(markup).toContain("Libraries");
    expect(markup).toContain("Discover");
    expect(markup).toContain("Your Stuff");
  });

  it("keeps the collapsed sidebar expanded while the profile menu is open without changing menu side", () => {
    expect(isSidebarExpanded(true, false, true)).toBe(true);
    expect(getProfileMenuSide(true)).toBe("right");
  });

  it("keeps the profile trigger left-anchored and the same size in both states", () => {
    // `mx-auto` would centre the avatar in the 260px surface — far outside the
    // 64px rail — and any width swap would pop when the surface starts moving.
    const collapsed = renderSidebar("/item/42", { collapsed: true });
    const expanded = renderSidebar("/", { collapsed: false });

    for (const markup of [collapsed, expanded]) {
      expect(markup).toContain("flex w-full items-center gap-2.5 rounded-xl px-3 py-3");
      expect(markup).not.toContain("mx-auto h-10 w-10 justify-center px-0");
    }
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

  it("groups Apps entries by first category segment with Other last when 2+ categories exist", () => {
    mockPluginInstallations = [
      pluginInstallation(1, "alpha-app", "Alpha", "Tools/Utilities"),
      pluginInstallation(2, "beta-app", "Beta", "Extras"),
      pluginInstallation(3, "gamma-app", "Gamma"),
    ];

    const markup = renderSidebar("/");

    expect(markup).toContain(">Apps<");
    expect(markup).toContain(">Extras<");
    expect(markup).toContain(">Tools<");
    expect(markup).toContain(">Other<");
    // Alphabetical category order with the uncategorized bucket last.
    const extrasIndex = markup.indexOf(">Extras<");
    const toolsIndex = markup.indexOf(">Tools<");
    const otherIndex = markup.indexOf(">Other<");
    expect(extrasIndex).toBeGreaterThan(-1);
    expect(toolsIndex).toBeGreaterThan(extrasIndex);
    expect(otherIndex).toBeGreaterThan(toolsIndex);
  });

  it("hides Apps group headers the same way as other section headers when collapsed", () => {
    mockPluginInstallations = [
      pluginInstallation(1, "alpha-app", "Alpha", "Tools"),
      pluginInstallation(2, "beta-app", "Beta", "Extras"),
    ];

    const markup = renderSidebar("/", { collapsed: true });

    // Group headers reuse SidebarSectionHeader, so the label slot stays in
    // the layout (preventing shifts) but is visually hidden when collapsed.
    expect(markup).toContain(">Tools<");
    expect(markup).toContain(">Extras<");
    const hiddenHeaderCount = (markup.match(/aria-hidden="true" class="[^"]*opacity-0/g) ?? [])
      .length;
    expect(hiddenHeaderCount).toBeGreaterThan(0);
  });
});

describe("sidebar collapse surface", () => {
  it("keeps the surface 260px wide on a detail route and everywhere else", () => {
    // Nothing about the sidebar's box changes between states — the frame just
    // slides, so there is never a width to interpolate.
    for (const markup of [renderSidebar("/"), renderSidebar("/item/42", { collapsed: true })]) {
      const aside = markup.match(/<aside[^>]*class="([^"]*)"/)?.[1] ?? "";
      expect(aside).toContain("w-[260px]");
      expect(aside).toContain("overflow-hidden");
      expect(aside).not.toContain("w-16");
    }
  });

  it("puts the blur on the counter-translated layer, not the sliding frame", () => {
    // The inner layer is screen-static, so its 40px backdrop is sampled from a
    // region that never moves; on the frame it would be re-blurred per frame.
    const markup = renderSidebar("/item/42", { collapsed: true });
    const aside = markup.match(/<aside[^>]*class="([^"]*)"/)?.[1] ?? "";

    expect(aside).not.toContain("backdrop-blur");
    expect(markup).toContain("bg-sidebar/88 sidebar-inner");
    expect(markup).toContain("backdrop-blur-2xl");
  });

  it("keeps the rail border on the frame so it rides along to x=64", () => {
    const aside =
      renderSidebar("/item/42", { collapsed: true }).match(/<aside[^>]*class="([^"]*)"/)?.[1] ?? "";
    expect(aside).toContain("border-sidebar-border/70");
    expect(aside).toContain("border-r");
  });

  it("marks the surface collapsed only on a detail route", () => {
    expect(renderSidebar("/item/42", { collapsed: true })).toContain('data-collapsed="true"');
    expect(renderSidebar("/")).not.toContain("data-collapsed");
  });

  it("keeps labels at their full layout box so the nav never reflows", () => {
    const collapsed = renderSidebar("/item/42", { collapsed: true });

    expect(collapsed).toContain("sidebar-fade max-w-[180px] truncate opacity-0");
    // The old max-width animation relaid out the whole nav subtree per frame.
    expect(collapsed).not.toContain("max-w-0");
    expect(collapsed).not.toContain("transition-[opacity,max-width]");
  });

  it("reserves the library chevron slot in both states and shifts the row instead", () => {
    const collapsed = renderSidebar("/item/42", { collapsed: true });
    const expanded = renderSidebar("/");

    for (const markup of [collapsed, expanded]) {
      expect(markup).toContain("sidebar-row-shift flex flex-1 items-center");
      // Mocked library 7 has a pin, so its slot is the wider chevron button.
      expect(markup).toContain("--sidebar-row-shift:-18px");
    }
  });
});

describe("libraryRowShift", () => {
  it("only shifts rows whose chevron slot pushes the icon out of the column", () => {
    // A pinned library reserves a 30px chevron button; every other nav row
    // starts its icon 12px in, so the row has to come back 18px.
    expect(libraryRowShift(true)).toBe("-18px");
    // The unpinned spacer is `w-3` with `pl-3` — border-box, so 12px total.
    // It already lines up, and shifting it would overshoot to the left.
    expect(libraryRowShift(false)).toBe("0px");
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

describe("sidebarSurfaceStyle", () => {
  it("floats hover expansion above the page instead of resizing it", () => {
    const style = sidebarSurfaceStyle({ collapsed: true, sidebarExpanded: true });

    expect(style?.zIndex).toBe(45);
    expect(style?.boxShadow).toContain("0 25px 50px -12px");
  });

  it("does not raise or shadow the surface in either resting state", () => {
    expect(sidebarSurfaceStyle({ collapsed: true, sidebarExpanded: false })).toBeUndefined();
    expect(sidebarSurfaceStyle({ collapsed: false, sidebarExpanded: true })).toBeUndefined();
  });
});

describe("groupAppNavLinks", () => {
  const link = (id: string, category?: string): AppNavLink => ({
    id,
    basePath: `/api/v1/plugins/${id}`,
    label: id,
    pluginId: id,
    category,
  });

  it("returns null for an empty list", () => {
    expect(groupAppNavLinks([])).toBeNull();
  });

  it("returns null when all links are uncategorized", () => {
    expect(groupAppNavLinks([link("a"), link("b")])).toBeNull();
  });

  it("returns null when all links share the same first category segment", () => {
    expect(groupAppNavLinks([link("a", "Tools/Utilities"), link("b", "Tools/Extras")])).toBeNull();
  });

  it("groups by first segment, sorts alphabetically, and puts Other last", () => {
    const groups = groupAppNavLinks([
      link("z", "Extras"),
      link("a", "Tools/Utilities"),
      link("m"),
      link("b", "Tools"),
    ]);

    expect(groups).not.toBeNull();
    expect(groups?.map((g) => g.category)).toEqual(["Extras", "Tools", "Other"]);
    // Input order preserved within a group.
    expect(groups?.[1]?.links.map((l) => l.id)).toEqual(["a", "b"]);
    expect(groups?.[2]?.links.map((l) => l.id)).toEqual(["m"]);
  });

  it("treats blank or slash-only categories as uncategorized", () => {
    const groups = groupAppNavLinks([link("a", "  "), link("b", "/Tools"), link("c", "Extras")]);

    expect(groups?.map((g) => g.category)).toEqual(["Extras", "Other"]);
    expect(groups?.[1]?.links.map((l) => l.id)).toEqual(["a", "b"]);
  });
});
