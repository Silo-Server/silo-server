import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { Outlet } from "react-router";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

let appInitialEntries = ["/catalog?source=favorites"];
let latestNavigateTo: string | null = null;
let appProfile: { id: string; is_primary?: boolean } | null = { id: "profile-1" };
let appUser: { id: number; username: string; role: string } = {
  id: 1,
  username: "alex",
  role: "admin",
};

const mockUseCatalogWindow = vi.fn();
const mockUseCatalogFilters = vi.fn();
const mockItemGrid = vi.fn();

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");

  return {
    ...actual,
    // App builds a data router from the real history; point it at the entry
    // under test instead.
    createBrowserRouter: ((routes: Parameters<typeof actual.createMemoryRouter>[0]) =>
      actual.createMemoryRouter(routes, {
        initialEntries: appInitialEntries,
      })) as typeof actual.createBrowserRouter,
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

// Collection pages read the collection to decide what to offer; every other
// request answers like an unreachable server.
vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: (...args: unknown[]) => mockUseCatalogWindow(...args),
  useCatalogFilters: (...args: unknown[]) => mockUseCatalogFilters(...args),
  useCatalogMetadataFilters: (...args: unknown[]) => mockUseCatalogFilters(...args),
}));

vi.mock("@/hooks/queries/shuffles", () => ({
  useStartShuffle: () => ({ startShuffle: vi.fn(), isStarting: false }),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestFeatureStatus: () => ({ data: undefined }),
}));

vi.mock("@/hooks/queries/watchlistTitles", () => ({
  useWatchlistTitles: () => ({ data: undefined, isLoading: false, isError: false }),
}));

vi.mock("@/hooks/useAuth", () => {
  const auth = () => ({
    user: appUser,
    profile: appProfile,
    loading: false,
    setupLoading: false,
    setupRequired: false,
    isImpersonating: false,
    endImpersonation: vi.fn(),
    logout: vi.fn(),
    clearProfile: vi.fn(),
  });
  return {
    AuthProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
    useAuth: auth,
    useOptionalAuth: auth,
  };
});

vi.mock("@/hooks/useTheme", () => ({
  ThemeProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("@/components/ErrorBoundary", () => ({
  ErrorBoundary: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("@/components/ui/sonner", () => ({
  Toaster: () => null,
}));

// The playback chrome reads its snapshot store through useSyncExternalStore
// with no server snapshot, so a static render of the app shell can't include
// it. Nothing on the catalog page depends on it.
vi.mock("@/playback/WatchPlaybackChrome", () => ({
  WatchPlaybackProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
  WatchPlaybackHost: () => null,
  WatchPlaybackBar: () => null,
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
    narrowPosterActions?: boolean;
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
vi.mock("@/pages/CollectionEditorPage", () => stubPage("Collection editor"));
vi.mock("@/pages/AdminDashboard", () => stubPage("Admin dashboard"));
vi.mock("@/pages/AdminActivity", () => stubPage("Admin activity"));
vi.mock("@/pages/AdminLogs", () => stubPage("Admin logs"));
vi.mock("@/pages/AdminUsers", () => stubPage("Admin users"));
vi.mock("@/pages/AdminLibraries", () => stubPage("Admin libraries"));
vi.mock("@/pages/admin-settings/AdminSettingsLayout", () => stubPage("Admin settings"));
vi.mock("@/pages/AdminNodes", () => stubPage("Admin nodes"));
vi.mock("@/pages/AdminHomeRows", () => stubPage("Admin home rows"));
vi.mock("@/pages/AdminCollections", () => stubPage("Admin collections"));
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
vi.mock("@/pages/settings/AccountSettings", () => stubPage("Account settings"));
vi.mock("@/pages/settings/LibrarySettings", () => stubPage("Library settings"));
vi.mock("@/pages/settings/HistoryImportSettings", () => stubPage("History import settings"));
vi.mock("@/pages/settings/WebhookSyncSettings", () => stubPage("Webhook sync settings"));
vi.mock("@/pages/settings/SubtitleAppearanceSettings", () => stubPage("Subtitle appearance"));
vi.mock("@/pages/settings/HomeScreenSettings", () => stubPage("Home screen settings"));
vi.mock("@/pages/WatchRoute", () => stubPage("Watch"));

import App from "../App";
import { PERSONAL_SCOPE, SERVER_SCOPE } from "@/lib/collections/scope";
import { queryClient as appQueryClient } from "@/lib/query-client";
import { v2Recorder } from "@/test/v2Recorder";
import getAdminCollectionOk from "../../../contracts/api/v2/fixtures/get_admin_collection_ok.json";
import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import listProfilesOk from "../../../contracts/api/v2/fixtures/list_profiles_ok.json";
import {
  buildLibraryCollectionCatalogHref,
  buildUserCollectionCatalogHref,
} from "./catalogSearchParams";

function appTree(client = new QueryClient()) {
  return (
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>
  );
}

const staticApp = () => renderToStaticMarkup(appTree());

function resetCatalogMocks() {
  appInitialEntries = ["/catalog?source=favorites"];
  latestNavigateTo = null;
  appProfile = { id: "profile-1" };
  appUser = { id: 1, username: "alex", role: "admin" };
  v2Recorder.reset();
  mockUseCatalogWindow.mockReset();
  mockUseCatalogFilters.mockReset();
  mockItemGrid.mockReset();

  mockUseCatalogWindow.mockReturnValue({
    data: {
      title: "Heat Search",
      totalItems: 1,
      pages: new Map([[0, [{ content_id: "movie-1", title: "Heat", type: "movie" }]]]),
    },
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  });
  mockUseCatalogFilters.mockReturnValue({
    data: { genres: ["Drama"], content_ratings: ["R"] },
    isLoading: false,
  });
}

describe("Catalog page", () => {
  beforeAll(async () => {
    // Catalog is a lazy route, and a static render cannot wait for its chunk.
    // Import the page first so the slow module load happens once, outside any
    // render. The first render below then starts React.lazy on a cached
    // module, and the next one shows the page rather than the route fallback.
    await import("@/pages/Catalog");
    resetCatalogMocks();
    staticApp();
    await vi.waitFor(() => expect(staticApp()).not.toContain("Loading page"), {
      timeout: 5_000,
    });
  }, 15_000);

  beforeEach(() => {
    resetCatalogMocks();
  });

  it("browses an explicit library without requiring search text", () => {
    appInitialEntries = ["/catalog?library_id=17&sort=title&order=asc"];
    const markup = staticApp();
    expect(mockUseCatalogWindow).toHaveBeenCalledWith(
      expect.objectContaining({ source: "query", library_id: 17, q: undefined }),
      expect.objectContaining({ includeTotal: true }),
    );
    expect(mockItemGrid).toHaveBeenCalled();
    expect(markup).not.toContain("Find films, series, performances");
  });

  it("lists a filtered query across libraries without search text", () => {
    appInitialEntries = [
      "/catalog?source=query&type=movie&groups%5B0%5D%5Brules%5D%5B0%5D%5Bfield%5D=studio&groups%5B0%5D%5Brules%5D%5B0%5D%5Bop%5D=is&groups%5B0%5D%5Brules%5D%5B0%5D%5Bvalue%5D=Pixar",
    ];
    const markup = staticApp();
    expect(mockUseCatalogWindow).toHaveBeenCalled();
    expect(markup).not.toContain("Find films, series, performances");
  });

  it("renders the search-first landing for empty query catalog routes", () => {
    appInitialEntries = ["/catalog?source=query"];

    const markup = staticApp();

    expect(markup).toContain("Search");
    expect(markup).toContain(
      "Find films, series, performances, and rediscover things you forgot you saved.",
    );
    expect(mockUseCatalogWindow).not.toHaveBeenCalled();
    expect(mockUseCatalogFilters).not.toHaveBeenCalled();
  });

  it("lets an administrator without an active profile open account settings", async () => {
    appInitialEntries = ["/settings/account"];
    appProfile = null;

    render(appTree());

    expect(await screen.findByText("Account settings")).toBeInTheDocument();
    expect(latestNavigateTo).toBeNull();
    expect(screen.getByText("Settings")).toBeInTheDocument();
    expect(screen.getByText("Account settings").closest('[data-kind="app-layout"]')).not.toBeNull();
  });

  // After the account settings test: a static render shows a lazy settings
  // route only once a render has loaded it.
  it.each([
    [
      "routes legacy search URLs through the catalog page",
      "/search?q=heat",
      "/catalog?source=query&q=heat",
    ],
    [
      "routes legacy user collection URLs through the catalog page",
      "/collections/col-7",
      "/catalog?source=user_collection&collection_id=col-7",
    ],
    [
      "keeps other personal settings behind profile selection",
      "/settings/playback",
      "/profiles?redirect=%2Fsettings%2Fplayback",
      true,
    ],
    [
      "redirects the retired user plugins settings route back to playback settings",
      "/settings/plugins",
      "/settings/playback",
    ],
  ])("%s", (_name, entry, to, withoutProfile?: boolean) => {
    appInitialEntries = [entry];
    if (withoutProfile) appProfile = null;

    staticApp();

    expect(latestNavigateTo).toBe(to);
  });
});

describe("Catalog collection page actions", () => {
  const OWNER = { ...listProfilesOk.items[0], id: "profile-1", name: "Alex", is_primary: true };
  const MAYA = { ...OWNER, id: "p-maya", name: "Maya", is_primary: false };

  beforeAll(async () => {
    await import("@/pages/Catalog");
  });

  beforeEach(() => {
    resetCatalogMocks();
    // App keeps one query client for the page's life; start each test empty.
    appQueryClient.clear();
    mockUseCatalogWindow.mockReturnValue({
      data: { title: "Rainy days", totalItems: 0, pages: new Map() },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    });
    v2Recorder.answer("GET /api/v2/profiles", { ...listProfilesOk, items: [OWNER, MAYA] });
  });

  function renderApp() {
    render(appTree(new QueryClient({ defaultOptions: { queries: { retry: false } } })));
  }

  function personalCollectionBy(creatorProfileId: string) {
    v2Recorder.answer("GET /api/v2/collections/{id}", {
      ...getCollectionOk,
      profile_id: creatorProfileId,
      creator_profile_id: creatorProfileId,
      is_shared: creatorProfileId !== OWNER.id,
    });
  }

  it("gives the acting admin Edit and more actions on a server collection", async () => {
    appProfile = { id: OWNER.id, is_primary: true };
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", getAdminCollectionOk);
    appInitialEntries = [buildLibraryCollectionCatalogHref("c1", "Original", 1)];

    renderApp();

    const edit = await screen.findByRole("link", { name: "Edit" });
    expect(edit).toHaveAttribute("href", SERVER_SCOPE.paths.edit("c1", { libraryId: 1 }));
    expect(screen.getByRole("button", { name: "More actions" })).toBeInTheDocument();
  });

  it("shows no Edit on a server collection to a non-primary profile on an admin account", async () => {
    appProfile = { id: MAYA.id, is_primary: false };
    appInitialEntries = [buildLibraryCollectionCatalogHref("c1", "Original", 1)];

    renderApp();

    expect(await screen.findByRole("heading", { level: 1, name: "Rainy days" })).toBeVisible();
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
    // A viewer's actions only: the collection's own menu needs the admin read.
    expect(screen.getByRole("link", { name: "Add to my Home" })).toBeVisible();
    expect(v2Recorder.callsOf("GET /api/v2/admin/collections/{id}")).toEqual([]);
  });

  it("offers a regular account Add to my Home on a server collection, not Edit", async () => {
    appUser = { id: 2, username: "sam", role: "user" };
    appProfile = { id: OWNER.id, is_primary: true };
    appInitialEntries = [buildLibraryCollectionCatalogHref("c1", "Original", 1)];

    renderApp();

    expect(await screen.findByRole("heading", { level: 1, name: "Rainy days" })).toBeVisible();
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add to my Home" })).toHaveAttribute(
      "href",
      "/settings/home-screen?page=home&add=collection%3Alibrary%3Ac1",
    );
    expect(v2Recorder.callsOf("GET /api/v2/admin/collections/{id}")).toEqual([]);
  });

  it("gives the owner Edit on their own collection", async () => {
    personalCollectionBy(OWNER.id);
    appInitialEntries = [buildUserCollectionCatalogHref("c1", "Rainy days")];

    renderApp();

    const edit = await screen.findByRole("link", { name: "Edit" });
    expect(edit).toHaveAttribute("href", PERSONAL_SCOPE.paths.edit("c1"));
    expect(screen.queryByText("Read-only")).not.toBeInTheDocument();
  });

  it("shows a shared-with-me collection read-only, with its owner, Add to my Home and no Edit", async () => {
    personalCollectionBy(MAYA.id);
    appInitialEntries = [buildUserCollectionCatalogHref("c1", "Rainy days")];

    renderApp();

    expect(await screen.findByText("Read-only")).toBeVisible();
    expect(screen.getByText("Maya")).toBeVisible();
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add to my Home" })).toHaveAttribute(
      "href",
      "/settings/home-screen?page=home&add=collection%3Auser%3Ac1",
    );
  });

  it("explains the redirect from another profile's editor link and names the owner", async () => {
    personalCollectionBy(MAYA.id);
    appInitialEntries = [`${buildUserCollectionCatalogHref("c1", "Rainy days")}&notice=read-only`];

    renderApp();

    const callout = await screen.findByRole("note");
    expect(callout).toHaveTextContent(
      "Only Maya can change Rainy days, so you're on its page instead. You can still watch it and add it to your Home.",
    );
  });
});
