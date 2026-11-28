import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useDashboardInsightsActivity } from "@/hooks/queries/admin/insights";
import { adminStatsKey, useAdminStats } from "@/hooks/queries/admin/stats";
import AdminDashboard from "./AdminDashboard";

const mocks = vi.hoisted(() => ({ request: vi.fn(), withInsightsWidget: false }));
const profileContext = {
  accessToken: "token",
  authContextVersion: 1,
  serverOrigin: "http://silo.test",
  profileId: "p1",
  profileToken: null,
  profileTokenGeneration: 0,
};
vi.mock("@/api/client", async () => ({
  ...(await vi.importActual<typeof import("@/api/client")>("@/api/client")),
  captureProfileRequestContext: () => profileContext,
  isCapturedProfileAuthorityActive: () => true,
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
const listQuery = { data: [], isStale: false, dataUpdatedAt: 1, refetch: async () => ({}) };
vi.mock("@/hooks/queries/admin/stats", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/admin/stats")>(
    "@/hooks/queries/admin/stats",
  )),
  useAdminSessions: () => listQuery,
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => listQuery,
  useScanAllLibraries: () => ({ isPending: false, mutate: vi.fn() }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({ useAdminUsers: () => listQuery }));
vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/admin/policy", () => ({ usePolicyCapability: () => ({ data: null }) }));
vi.mock("@/hooks/usePageActivity", () => ({
  usePageActivity: () => ({ canPollDashboard: true }),
}));
vi.mock("@/components/admin/dashboard/useDashboardLayout", () => ({
  useDashboardLayout: () => ({ isCustomizing: false, setCustomizing: vi.fn() }),
}));
vi.mock("@/hooks/admin/useSettingsAttentionDismissals", () => ({
  useSettingsAttentionDismissals: () => ({ hiddenCount: 0, visible: [], saving: false }),
}));
vi.mock("@/components/admin/SettingsAttention", () => ({ SettingsAttention: () => null }));
vi.mock("@/components/AdminSectionCommandDialog", () => ({
  AdminSectionCommandDialog: () => null,
}));
// The grid stands in for the stat tiles: it shows what the stats query holds,
// and, when a test asks for one, mounts an Insights-backed widget.
function InsightsWidget() {
  useDashboardInsightsActivity("week");
  return null;
}
vi.mock("@/components/admin/dashboard/DashboardGrid", () => ({
  DashboardGrid: () => {
    const stats = useAdminStats();
    return (
      <>
        <output aria-label="Total movies">{stats.data?.total_movies ?? "—"}</output>
        {mocks.withInsightsWidget ? <InsightsWidget /> : null}
      </>
    );
  },
}));

type Deferred = ReturnType<typeof deferred<{ total_movies: number }>>;

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

describe("AdminDashboard refresh ordering", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    mocks.request.mockReset();
    mocks.withInsightsWidget = false;
  });

  // Leaving the dashboard and coming back mounts a new page while the first
  // page's refresh is still waiting, and both write the same stats cache.
  it.each([
    { name: "on the same page", leaveAndReturn: false },
    { name: "after leaving the page and coming back", leaveAndReturn: true },
  ])(
    "keeps the newest stats when an older refresh answers last $name",
    async ({ leaveAndReturn }) => {
      const refreshes: Deferred[] = [];
      mocks.request.mockImplementation(
        (operation: string, args: { query?: { refresh?: boolean } }) => {
          if (operation !== "GET /api/v2/admin/stats") throw new Error(`unexpected ${operation}`);
          if (!args.query?.refresh) return Promise.resolve({ total_movies: 1 });
          const response = deferred<{ total_movies: number }>();
          refreshes.push(response);
          return response.promise;
        },
      );
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const page = (
        <QueryClientProvider client={queryClient}>
          <MemoryRouter>
            <AdminDashboard />
          </MemoryRouter>
        </QueryClientProvider>
      );
      const { unmount } = render(page);
      await screen.findByText("1");

      // The automatic refresh starts, then the admin presses Refresh before it answers.
      await act(async () => {
        vi.advanceTimersByTime(60_000);
      });
      expect(refreshes).toHaveLength(1);
      if (leaveAndReturn) {
        unmount();
        render(page);
      }
      fireEvent.click(await screen.findByRole("button", { name: /^refresh$/i }));
      await vi.waitFor(() => expect(refreshes).toHaveLength(2));
      const [older, newer] = refreshes as [Deferred, Deferred];
      const tile = screen.getByLabelText("Total movies");

      await act(async () => newer.resolve({ total_movies: 222 }));
      await vi.waitFor(() => expect(tile.textContent).toBe("222"));
      await act(async () => older.resolve({ total_movies: 111 }));
      // Let the query cache deliver whatever the older answer wrote.
      await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
      expect(tile.textContent).toBe("222");
    },
  );

  function statsTransport() {
    const ordinary: Deferred[] = [];
    const refreshes: Deferred[] = [];
    mocks.request.mockImplementation(
      (operation: string, args: { query?: { refresh?: boolean } }) => {
        if (operation !== "GET /api/v2/admin/stats") throw new Error(`unexpected ${operation}`);
        const response = deferred<{ total_movies: number }>();
        (args.query?.refresh ? refreshes : ordinary).push(response);
        return response.promise;
      },
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <AdminDashboard />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    // A realtime event invalidates the stats query, which starts its own read.
    const realtimeEvent = () =>
      act(async () => {
        void queryClient.invalidateQueries({
          queryKey: adminStatsKey(profileContext),
          exact: true,
        });
      });
    const settle = () => act(() => new Promise((resolve) => setTimeout(resolve, 0)));
    return { ordinary, refreshes, realtimeEvent, settle };
  }

  // The ordinary read is answered from the server's stats cache. When it
  // answers after a refresh, its older numbers must not replace the refreshed
  // ones (#5246). The stats are read again after the refresh instead, so the
  // event that started the read still reaches the tiles.
  it("keeps a refresh's stats when an earlier ordinary read answers after it", async () => {
    const { ordinary, refreshes, realtimeEvent, settle } = statsTransport();
    await vi.waitFor(() => expect(ordinary).toHaveLength(1));
    await act(async () => ordinary[0]!.resolve({ total_movies: 1 }));
    const tile = await screen.findByText("1");

    await realtimeEvent();
    await vi.waitFor(() => expect(ordinary).toHaveLength(2));

    fireEvent.click(await screen.findByRole("button", { name: /^refresh$/i }));
    await vi.waitFor(() => expect(refreshes).toHaveLength(1));
    await act(async () => refreshes[0]!.resolve({ total_movies: 222 }));
    await vi.waitFor(() => expect(tile.textContent).toBe("222"));

    await act(async () => ordinary[1]!.resolve({ total_movies: 111 }));
    await settle();
    expect(tile.textContent).toBe("222");

    await vi.waitFor(() => expect(ordinary).toHaveLength(3));
    await act(async () => ordinary[2]!.resolve({ total_movies: 333 }));
    await vi.waitFor(() => expect(tile.textContent).toBe("333"));
  });

  // An older refresh that answers after a newer one has written is dropped.
  // It must not cancel a read that started after the newer refresh, either.
  it("lets a superseded refresh leave a later ordinary read alone", async () => {
    const { ordinary, refreshes, realtimeEvent, settle } = statsTransport();
    await vi.waitFor(() => expect(ordinary).toHaveLength(1));
    await act(async () => ordinary[0]!.resolve({ total_movies: 1 }));
    const tile = await screen.findByText("1");

    // The automatic refresh starts, then the admin presses Refresh.
    await act(async () => {
      vi.advanceTimersByTime(60_000);
    });
    fireEvent.click(await screen.findByRole("button", { name: /^refresh$/i }));
    await vi.waitFor(() => expect(refreshes).toHaveLength(2));
    const [older, newer] = refreshes as [Deferred, Deferred];
    await act(async () => newer.resolve({ total_movies: 222 }));
    await vi.waitFor(() => expect(tile.textContent).toBe("222"));

    await realtimeEvent();
    await vi.waitFor(() => expect(ordinary).toHaveLength(2));
    await act(async () => older.resolve({ total_movies: 111 }));
    await settle();
    await act(async () => ordinary[1]!.resolve({ total_movies: 333 }));
    await vi.waitFor(() => expect(tile.textContent).toBe("333"));
  });

  // The server serves its cached Insights while it refreshes them in the
  // background; a refresh the admin asked for must ask it for fresh data.
  it("asks the server for fresh Insights when the admin presses Refresh", async () => {
    mocks.withInsightsWidget = true;
    const insightsRefresh: boolean[] = [];
    mocks.request.mockImplementation(
      (operation: string, args: { query?: { refresh?: boolean } }) => {
        if (operation === "GET /api/v2/admin/stats") return Promise.resolve({ total_movies: 1 });
        if (operation === "GET /api/v2/admin/insights/activity") {
          insightsRefresh.push(Boolean(args.query?.refresh));
          return Promise.resolve({});
        }
        throw new Error(`unexpected ${operation}`);
      },
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <AdminDashboard />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await vi.waitFor(() => expect(insightsRefresh.length).toBeGreaterThan(0));
    const before = insightsRefresh.length;
    fireEvent.click(await screen.findByRole("button", { name: /^refresh$/i }));
    await vi.waitFor(() => expect(insightsRefresh.slice(before)).toContain(true));
  });
});
