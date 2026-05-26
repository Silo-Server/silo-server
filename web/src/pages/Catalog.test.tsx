import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { Outlet } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

let appInitialEntries = ["/catalog?source=query&q=heat"];
let latestNavigateTo: string | null = null;

const mockUseCatalogWindow = vi.fn();
const mockUseCatalogFilters = vi.fn();
const mockItemGrid = vi.fn();
const mockUseCanRequest = vi.fn();
const mockUseRequestSearch = vi.fn();

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");

  return {
    ...actual,
    BrowserRouter: ({ children }: { children: ReactNode }) => (
      <actual.MemoryRouter initialEntries={appInitialEntries}>{children}</actual.MemoryRouter>
    ),
    Navigate: ({
      to,
      replace,
    }: {
      to: string | { pathname?: string; search?: string };
      replace?: boolean;
    }) => {
      latestNavigateTo = typeof to === "string" ? to : `${to.pathname ?? ""}${to.search ?? ""}`;
      return <actual.Navigate to={to} replace={replace} />;
    },
  };
});

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: (...args: unknown[]) => mockUseCatalogWindow(...args),
  useCatalogFilters: (...args: unknown[]) => mockUseCatalogFilters(...args),
  useCatalogMetadataFilters: (...args: unknown[]) => mockUseCatalogFilters(...args),
}));

vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mockUseCanRequest(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mockUseRequestSearch(...args),
}));

vi.mock("@/components/RequestToAddSection", () => ({
  RequestToAddSection: ({
    variant,
    query,
    libraryHadHits,
  }: {
    variant: string;
    query: string;
    libraryHadHits: boolean;
  }) => (
    <div data-testid="request-section">
      {`variant="${variant}" query="${query}" libraryHadHits="${String(libraryHadHits)}"`}
    </div>
  ),
}));

vi.mock("@/hooks/useAuth", () => ({
  AuthProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
  useAuth: () => ({
    user: { id: 1, username: "alex", role: "admin" },
    profile: { id: "profile-1" },
    loading: false,
    setupLoading: false,
    setupRequired: false,
    isImpersonating: false,
    endImpersonation: vi.fn(),
    logout: vi.fn(),
    clearProfile: vi.fn(),
  }),
  useOptionalAuth: () => ({
    user: { id: 1, username: "alex", role: "admin" },
    profile: { id: "profile-1" },
    loading: false,
    setupLoading: false,
    setupRequired: false,
    isImpersonating: false,
    endImpersonation: vi.fn(),
    logout: vi.fn(),
    clearProfile: vi.fn(),
  }),
}));

vi.mock("@/hooks/useTheme", () => ({
  ThemeProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("@/components/ErrorBoundary", () => ({
  ErrorBoundary: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("@/components/ui/sonner", () => ({
  Toaster: () => null,
}));

vi.mock("@/components/Layout", () => ({
  default: ({ children }: { children: ReactNode }) => <div data-kind="app-layout">{children}</div>,
}));

vi.mock("@/components/AdminLayout", () => ({
  default: () => null,
}));

vi.mock("@/components/ItemGrid", () => ({
  default: (props: {
    items?: Array<{ title: string }>;
    totalItems?: number;
    pageSize?: number;
    loading?: boolean;
    onVisibleRangeChange?: (start: number, end: number) => void;
  }) => {
    mockItemGrid(props);
    return (
      <div
        data-kind="item-grid"
        data-loading={String(Boolean(props.loading))}
        data-total={String(props.totalItems ?? 0)}
      >
        {props.items?.map((item) => item.title).join(",")}
      </div>
    );
  },
}));

function stubPage(name: string) {
  return { default: () => <div>{name}</div> };
}

vi.mock("@/pages/Home", () => stubPage("Home"));
vi.mock("@/pages/Login", () => stubPage("Login"));
vi.mock("@/pages/SetupWizard", () => stubPage("Setup"));
vi.mock("@/pages/Profiles", () => stubPage("Profiles"));
vi.mock("@/pages/LibraryPage", () => stubPage("Library"));
vi.mock("@/pages/ItemDetail/index", () => stubPage("Item detail"));
vi.mock("@/pages/Collections", () => stubPage("Collections"));
vi.mock("@/pages/CollectionEditor", () => stubPage("Collection editor"));
vi.mock("@/pages/AdminDashboard", () => stubPage("Admin dashboard"));
vi.mock("@/pages/AdminActivity", () => stubPage("Admin activity"));
vi.mock("@/pages/AdminLogs", () => stubPage("Admin logs"));
vi.mock("@/pages/AdminUsers", () => stubPage("Admin users"));
vi.mock("@/pages/AdminLibraries", () => stubPage("Admin libraries"));
vi.mock("@/pages/admin-settings/AdminSettingsLayout", () => stubPage("Admin settings"));
vi.mock("@/pages/AdminNodes", () => stubPage("Admin nodes"));
vi.mock("@/pages/AdminSections", () => stubPage("Admin sections"));
vi.mock("@/pages/AdminCollections", () => stubPage("Admin collections"));
vi.mock("@/pages/AdminCollectionEditor", () => stubPage("Admin collection editor"));
vi.mock("@/pages/AdminPlaybackHistory", () => stubPage("Admin playback history"));
vi.mock("@/pages/AdminMaintenance", () => stubPage("Admin maintenance"));
vi.mock("@/pages/AdminApiKeys", () => stubPage("Admin api keys"));
vi.mock("@/pages/AdminUserDetail", () => stubPage("Admin user detail"));
vi.mock("@/pages/AdminTasks", () => stubPage("Admin tasks"));
vi.mock("@/pages/AdminTaskDetail", () => stubPage("Admin task detail"));
vi.mock("@/pages/Recommendations", () => stubPage("Recommendations"));
vi.mock("@/pages/Signup", () => stubPage("Signup"));
vi.mock("@/pages/SettingsLayout", () => ({
  default: () => (
    <div>
      Settings
      <Outlet />
    </div>
  ),
}));
vi.mock("@/pages/settings/PlaybackSettings", () => stubPage("Playback settings"));
vi.mock("@/pages/settings/LibrarySettings", () => stubPage("Library settings"));
vi.mock("@/pages/settings/HistoryImportSettings", () => stubPage("History import settings"));
vi.mock("@/pages/settings/WebhookSyncSettings", () => stubPage("Webhook sync settings"));
vi.mock("@/pages/settings/SubtitleAppearanceSettings", () => stubPage("Subtitle appearance"));
vi.mock("@/pages/settings/HomeScreenSettings", () => stubPage("Home screen settings"));
vi.mock("@/pages/settings/PluginSettings", () => stubPage("Plugin settings"));
vi.mock("@/pages/WatchRoute", () => stubPage("Watch"));

import App from "../App";

describe("Catalog page", () => {
  beforeEach(() => {
    appInitialEntries = ["/catalog?source=query&q=heat"];
    latestNavigateTo = null;
    mockUseCatalogWindow.mockReset();
    mockUseCatalogFilters.mockReset();
    mockItemGrid.mockReset();
    mockUseCanRequest.mockReset();
    mockUseRequestSearch.mockReset();
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mockUseRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: false });

    mockUseCatalogWindow.mockReturnValue({
      data: {
        title: "Heat Search",
        totalItems: 1,
        pages: new Map([[0, [{ content_id: "movie-1", title: "Heat", type: "movie" }]]]),
      },
      isLoading: false,
    });
    mockUseCatalogFilters.mockReturnValue({
      data: { genres: ["Drama"], content_ratings: ["R"] },
      isLoading: false,
    });
  });

  it("renders catalog results from the new API route", () => {
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).toContain("Heat Search");
    expect(markup).toContain("Heat");
    expect(markup).toContain("Search movies, series...");
    expect(mockUseCatalogFilters).not.toHaveBeenCalled();
  });

  it("passes windowed paging props to the item grid for catalog search results", () => {
    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(mockUseCatalogWindow).toHaveBeenCalledWith(
      expect.objectContaining({
        source: "query",
        q: "heat",
      }),
      expect.objectContaining({
        limit: 60,
        visibleRange: [0, 59],
      }),
    );
    expect(mockItemGrid).toHaveBeenCalledWith(
      expect.objectContaining({
        totalItems: 1,
        pageSize: 60,
        onVisibleRangeChange: expect.any(Function),
      }),
    );
  });

  it("renders the search-first landing for empty query catalog routes", () => {
    appInitialEntries = ["/catalog?source=query"];

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).toContain("Search");
    expect(markup).toContain(
      "Find films, series, performances, and rediscover things you forgot you saved.",
    );
    expect(mockUseCatalogWindow).not.toHaveBeenCalled();
    expect(mockUseCatalogFilters).not.toHaveBeenCalled();
  });

  it("routes legacy search URLs through the catalog page", () => {
    appInitialEntries = ["/search?q=heat"];

    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(latestNavigateTo).toBe("/catalog?source=query&q=heat");
  });

  it("routes legacy user collection URLs through the catalog page", () => {
    appInitialEntries = ["/collections/col-7"];

    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(latestNavigateTo).toBe("/catalog?source=user_collection&collection_id=col-7");
  });

  it("routes person detail URLs to the PersonDetail page", () => {
    appInitialEntries = ["/person/117290402172239876"];

    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    // PersonDetail renders directly — no redirect
    expect(latestNavigateTo).toBeNull();
  });

  it("renders user settings inside the main app layout", () => {
    appInitialEntries = ["/settings/playback"];

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).toContain('data-kind="app-layout"');
    expect(markup).toContain("Settings");
  });

  it("redirects the retired user plugins settings route back to playback settings", () => {
    appInitialEntries = ["/settings/plugins"];

    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(latestNavigateTo).toBe("/settings/playback");
  });

  it("renders the request grid variant when source=query and library has results", () => {
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mockUseRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).toContain('data-testid="request-section"');
    expect(markup).toContain("variant=&quot;grid&quot;");
    expect(markup).toContain("libraryHadHits=&quot;true&quot;");
  });

  it("renders the request grid variant with libraryHadHits=false when library has 0 hits", () => {
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mockUseCatalogWindow.mockReturnValue({
      data: { title: 'Results for "heat"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });
    mockUseRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).toContain("libraryHadHits=&quot;false&quot;");
  });

  it("does not render the request section when source is not query", () => {
    appInitialEntries = ["/catalog?source=favorites"];
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mockUseCatalogWindow.mockReturnValue({
      data: { title: "Favorites", totalItems: 0, pages: new Map() },
      isLoading: false,
    });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).not.toContain('data-testid="request-section"');
  });

  it("does not render the request section when discovery is disabled", () => {
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).not.toContain('data-testid="request-section"');
  });

  it("passes enabled=false to useRequestSearch when discoveryEnabled is false", () => {
    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    const call = mockUseRequestSearch.mock.calls[mockUseRequestSearch.mock.calls.length - 1];
    expect(call?.[3]).toEqual({ enabled: false });
  });

  it("hides ItemGrid when library is empty and TMDB is still loading (request section will rescue)", () => {
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mockUseCatalogWindow.mockReturnValue({
      data: { title: 'Results for "heat"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });
    mockUseRequestSearch.mockReturnValue({ data: undefined, isLoading: true, isError: false });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    // Previously this case forced ItemGrid into loading=true, rendering 24 fake
    // skeletons forever above the section. Now ItemGrid is hidden entirely.
    expect(markup).not.toContain('data-kind="item-grid"');
  });

  it("hides ItemGrid when library is empty and TMDB has missing results", () => {
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mockUseCatalogWindow.mockReturnValue({
      data: { title: 'Results for "heat"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });
    mockUseRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).not.toContain('data-kind="item-grid"');
    expect(markup).toContain('data-testid="request-section"');
  });

  it("hides ItemGrid when library is empty and discovery feature status is still resolving", () => {
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: true,
      submitDisabledReason: null,
    });
    mockUseCatalogWindow.mockReturnValue({
      data: { title: 'Results for "heat"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    // Avoids the empty-state flash before the feature status resolves and TMDB
    // either rescues with results or confirms there are none.
    expect(markup).not.toContain('data-kind="item-grid"');
  });

  it("renders the normal ItemGrid empty state when both library and TMDB are empty", () => {
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mockUseCatalogWindow.mockReturnValue({
      data: { title: 'Results for "heat"', totalItems: 0, pages: new Map() },
      isLoading: false,
    });
    mockUseRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 0, results: [] },
      isLoading: false,
      isError: false,
    });

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <App />
      </QueryClientProvider>,
    );

    expect(markup).toContain('data-loading="false"');
    expect(markup).toContain('data-total="0"');
  });
});
