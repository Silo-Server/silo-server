// @vitest-environment node

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
  type AppNavLink,
} from "./AppSidebar.logic";

const mockLogout = vi.fn();
const mockClearProfile = vi.fn();
const mockTogglePin = vi.fn();

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
    data: [{ id: 7, name: "Movies", type: "movies" }],
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
    setTheme: vi.fn(),
    previewTheme: vi.fn(),
    resetPreviewTheme: vi.fn(),
  }),
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

describe("AppSidebar", () => {
  beforeEach(() => {
    mockLogout.mockReset();
    mockClearProfile.mockReset();
    mockTogglePin.mockReset();
    mockPluginInstallations = [];
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

  it("uses the icon-only mark when the sidebar is collapsed", () => {
    const markup = renderSidebar("/", { collapsed: true });

    expect(markup).toContain('src="/silo-icon-1024.png"');
    expect(markup).not.toContain('src="/silo-wordmark-sidebar.png"');
    expect(markup).toContain("sidebar-logo flex items-center py-6 justify-center px-2");
  });

  it("uses the cinema highlight text color for active pinned catalog destinations", () => {
    const markup = renderSidebar(
      "/catalog?source=section&scope=library&library_id=7&section_id=featured&title=Featured",
    );

    expect(markup).toContain("text-sidebar-accent-foreground bg-sidebar-accent");
    expect(markup).not.toContain("text-sidebar-primary-foreground bg-sidebar-accent");
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

  it("centers the profile trigger when the sidebar is collapsed", () => {
    const markup = renderSidebar("/item/42", { collapsed: true });

    expect(markup).toContain("mx-auto h-10 w-10 justify-center px-0");
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
