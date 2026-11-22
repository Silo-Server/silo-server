import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminJob, Library, ScanRun } from "@/api/types";

const mocks = vi.hoisted(() => ({
  libraries: [] as Library[],
  activeScans: [] as ScanRun[],
  refreshJobs: [] as AdminJob[] | undefined,
  refreshJobsPending: false,
  refreshJobsError: false,
  refreshJobsHasNextPage: false,
  refreshJobsFetchNextPage: vi.fn(),
  refreshJobsRestart: vi.fn(),
  unmatchedTotal: 0 as number | undefined,
  unmatchedError: false,
  unmatchedRefetch: vi.fn(),
  matcherTotal: 0 as number | undefined,
  matcherPending: false,
  matcherError: false,
  matcherRefetch: vi.fn(),
  rootsCalls: [] as Array<{ enabled?: boolean }>,
  skippedCalls: [] as Array<{ enabled?: boolean }>,
  staleCalls: [] as Array<{ enabled?: boolean }>,
  scanMutate: vi.fn(),
  cancelScansMutate: vi.fn(),
}));

function makeLibrary(overrides: Partial<Library> = {}): Library {
  return {
    id: 1,
    name: "Movies",
    type: "movies",
    paths: ["/media/movies-a", "/media/movies-b"],
    enabled: true,
    metadata_language: "en",
    auto_translate_metadata: false,
    chapter_thumbnails_enabled: false,
    chapter_thumbnails_supported: true,
    intro_detection_enabled: false,
    trailer_kinds: [],
    realtime_monitoring: true,
    sort_order: 0,
    last_scanned_at: new Date(Date.now() - 2 * 3600_000).toISOString(),
    ...overrides,
  };
}

function makeScan(overrides: Partial<ScanRun> = {}): ScanRun {
  return {
    id: "scan-101",
    library_id: 2,
    mode: "subtree",
    path: "/media/tv/Show/Season 01",
    status: "running",
    trigger: "autoscan",
    started_at: new Date(Date.now() - 30_000).toISOString(),
    result: {
      new: 0,
      updated: 0,
      unchanged: 0,
      missing: 0,
      files_deleted: 0,
      memberships_removed: 0,
      items_deleted: 0,
      matched_files: 0,
      retried_items: 0,
      still_unmatched_warnings: 0,
      skipped: 0,
      errors: 0,
      message: "Reconciling scan state",
      files_processed: 5,
      total_files: 10,
    },
    ...overrides,
  };
}

vi.mock("@/components/realtimeEventsContext", () => ({
  useEventChannel: () => {},
}));

vi.mock("@/hooks/queries/admin/scans", () => ({
  useActiveScans: () => ({ data: mocks.activeScans }),
}));

vi.mock("@/hooks/queries/admin/trickplay", () => ({
  useAdminTrickplayLibraries: () => ({ data: [] }),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  UNMATCHED_PAGE_SIZE: 10,
  useAdminLibraries: () => ({ data: mocks.libraries, isLoading: false, isFetching: false }),
  useLibraryCapabilities: () => ({ data: { trickplay: false } }),
  useLibraryRealtimeMonitoring: () => ({ data: { libraries: [] } }),
  useLibraryRefreshJobs: () => ({
    data: mocks.refreshJobs,
    isPending: mocks.refreshJobsPending,
    isError: mocks.refreshJobsError,
    hasNextPage: mocks.refreshJobsHasNextPage,
    isFetchingNextPage: false,
    fetchNextPage: mocks.refreshJobsFetchNextPage,
    restart: mocks.refreshJobsRestart,
  }),
  useReorderLibraries: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteLibrary: () => ({ mutate: vi.fn(), isPending: false }),
  useScanAllLibraries: () => ({ mutate: vi.fn(), isPending: false }),
  useScanLibrary: () => ({ mutate: mocks.scanMutate, isPending: false, variables: undefined }),
  useCancelLibraryScans: () => ({
    mutate: mocks.cancelScansMutate,
    isPending: false,
    variables: undefined,
  }),
  useCancelAdminJob: () => ({ mutate: vi.fn(), isPending: false, variables: undefined }),
  useCheckLibraryMount: () => ({ mutate: vi.fn(), isPending: false, variables: undefined }),
  useConfirmEmptyRootCleanup: () => ({ mutate: vi.fn(), isPending: false, variables: undefined }),
  useRefreshLibraryMetadata: () => ({ mutate: vi.fn(), isPending: false, variables: undefined }),
  useUnmatchedLibraryItems: () => ({
    data:
      mocks.unmatchedTotal !== undefined
        ? { pages: [{ items: [], total: mocks.unmatchedTotal, nextCursor: undefined }] }
        : undefined,
    hasNextPage: false,
    fetchNextPage: vi.fn(),
    isFetching: false,
    isError: mocks.unmatchedError,
    refetch: mocks.unmatchedRefetch,
  }),
  useSetUnmatchedItemIgnored: () => ({ mutate: vi.fn(), isPending: false, variables: undefined }),
  useLibraryMetadataMatchQueues: () => ({
    data:
      mocks.matcherTotal === undefined
        ? undefined
        : mocks.matcherTotal > 0
          ? [
              {
                library_id: 1,
                pending_count: mocks.matcherTotal,
                parked_count: 0,
                total_count: mocks.matcherTotal,
              },
            ]
          : [],
    isPending: mocks.matcherPending,
    isError: mocks.matcherError,
    refetch: mocks.matcherRefetch,
  }),
  useLibraryMetadataMatchQueueDetail: () => ({
    data: undefined,
    isFetching: false,
    hasNextPage: false,
    fetchNextPage: vi.fn(),
  }),
  useRetryLibraryMetadataMatchQueue: () => ({
    mutate: vi.fn(),
    isPending: false,
    variables: undefined,
  }),
  useLibraryRoots: (
    _id: number | undefined,
    _state: string | undefined,
    opts?: { enabled?: boolean },
  ) => {
    mocks.rootsCalls.push({ enabled: opts?.enabled });
    return {
      data: opts?.enabled ? { pages: [{ roots: [], total: 0, nextCursor: undefined }] } : undefined,
      isError: false,
      hasNextPage: false,
      fetchNextPage: vi.fn(),
      isFetchingNextPage: false,
    };
  },
  flattenLibraryRoots: () => [],
  useUpsertLibraryRootOverride: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteLibraryRootOverride: () => ({ mutate: vi.fn(), isPending: false }),
  useSkippedLibraryRoots: (opts?: { enabled?: boolean }) => {
    mocks.skippedCalls.push({ enabled: opts?.enabled });
    return {
      data: opts?.enabled ? { pages: [{ roots: [], total: 0, nextCursor: undefined }] } : undefined,
      isError: false,
      isLoading: false,
      hasNextPage: false,
      fetchNextPage: vi.fn(),
      isFetchingNextPage: false,
    };
  },
  useStaleMediaIDs: (opts?: { enabled?: boolean }) => {
    mocks.staleCalls.push({ enabled: opts?.enabled });
    return {
      data: opts?.enabled ? { pages: [{ items: [], total: 0, nextCursor: undefined }] } : undefined,
      isError: false,
      isLoading: false,
      hasNextPage: false,
      fetchNextPage: vi.fn(),
      isFetchingNextPage: false,
    };
  },
  flattenStaleMediaIDs: () => [],
}));

vi.mock("@/components/admin/libraries/LibraryEditorDialog", () => ({
  LibraryEditorDialog: ({
    open,
    library,
    initialSection,
  }: {
    open: boolean;
    library: Library | null;
    initialSection?: string;
  }) =>
    open ? (
      <div
        data-testid="library-editor-dialog"
        data-library-id={library?.id ?? "new"}
        data-initial-section={initialSection ?? "general"}
      />
    ) : null,
}));

import AdminLibraries from "@/pages/AdminLibraries";

function renderPage(initialEntry = "/admin/libraries") {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/admin/libraries" element={<AdminLibraries />} />
        <Route path="/admin/autoscan" element={<div>Autoscan target</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("AdminLibraries layout", () => {
  beforeEach(() => {
    mocks.libraries = [
      makeLibrary({ id: 1, name: "Movies", type: "movies" }),
      makeLibrary({ id: 2, name: "TV Shows", type: "series", paths: ["/media/tv"] }),
    ];
    mocks.activeScans = [
      makeScan({
        id: "scan-101",
        library_id: 2,
        status: "running",
        path: "/media/tv/Show/Season 01",
      }),
      makeScan({
        id: "scan-102",
        library_id: 2,
        status: "accepted",
        path: "/media/tv/Show/Season 02",
      }),
    ];
    mocks.refreshJobs = [];
    mocks.refreshJobsPending = false;
    mocks.refreshJobsError = false;
    mocks.refreshJobsHasNextPage = false;
    mocks.refreshJobsFetchNextPage.mockReset();
    mocks.refreshJobsRestart.mockReset();
    mocks.unmatchedTotal = 12;
    mocks.unmatchedError = false;
    mocks.unmatchedRefetch.mockReset();
    mocks.matcherTotal = 3;
    mocks.matcherPending = false;
    mocks.matcherError = false;
    mocks.matcherRefetch.mockReset();
    mocks.rootsCalls = [];
    mocks.skippedCalls = [];
    mocks.staleCalls = [];
    mocks.scanMutate.mockReset();
    mocks.cancelScansMutate.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it("shows labeled row actions, omits redundant Enabled pills, and jumps from a row scan chip into Scan Activity", () => {
    renderPage();

    expect(screen.queryByText("Enabled")).not.toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /Libraries/i })).toHaveTextContent("2");
    expect(screen.getByRole("tab", { name: /Scan Activity/i })).toHaveTextContent("2 active");
    expect(screen.getByRole("tab", { name: /Catalog Health/i })).toHaveTextContent("15");

    const moviesRow = screen.getByRole("button", { name: "Edit Movies" }).closest("tr")!;
    expect(within(moviesRow).getByRole("button", { name: "Scan Library" })).toHaveTextContent(
      "Scan",
    );
    expect(within(moviesRow).getByRole("button", { name: "Edit Movies" })).toHaveTextContent(
      "Edit",
    );

    const tvRow = screen.getByRole("button", { name: "Edit TV Shows" }).closest("tr")!;
    expect(within(tvRow).getByRole("button", { name: "Stop Library Scans" })).toHaveTextContent(
      "Stop",
    );

    // Clicking the library's live scan chip switches directly to Scan Activity for that library.
    fireEvent.click(
      within(tvRow).getByRole("button", {
        name: /1 running.*1 queued.*View active scans for TV Shows/i,
      }),
    );
    expect(screen.getByRole("tab", { name: /Scan Activity/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("Season 01")).toBeInTheDocument();
    expect(screen.getByText(/Season 02/)).toBeInTheDocument();
  });

  it("keeps heavy Catalog Health endpoints lazy until their tile is selected", () => {
    renderPage("/admin/libraries?tab=health");

    expect(screen.getByRole("tab", { name: /Catalog Health/i })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(mocks.rootsCalls).toHaveLength(0);
    expect(mocks.skippedCalls).toHaveLength(0);
    expect(mocks.staleCalls).toHaveLength(0);

    const ambiguousTab = screen.getByRole("tab", { name: /Ambiguous Roots/i });
    fireEvent.mouseDown(ambiguousTab);
    fireEvent.click(ambiguousTab);
    expect(mocks.rootsCalls.some((call) => call.enabled === true)).toBe(true);
    expect(mocks.skippedCalls).toHaveLength(0);
    // With multiple libraries, the tile stays on "Inspect" rather than showing
    // one library's count as a catalog-wide total.
    expect(ambiguousTab).toHaveTextContent("Inspect");

    const staleTab = screen.getByRole("tab", { name: /Stale External IDs/i });
    fireEvent.mouseDown(staleTab);
    fireEvent.click(staleTab);
    expect(mocks.staleCalls.some((call) => call.enabled === true)).toBe(true);
  });

  it("preserves the legacy ?tab=autoscan redirect", () => {
    renderPage("/admin/libraries?tab=autoscan");
    expect(screen.getByText("Autoscan target")).toBeInTheDocument();
  });

  it("attaches a library's scan warning directly beneath its own row", () => {
    mocks.libraries = [
      makeLibrary({
        id: 1,
        name: "Movies",
        scan_warning_code: "empty_root",
        scan_warning_message: "Mount returned 0 files",
      }),
      makeLibrary({ id: 2, name: "TV Shows", type: "series" }),
    ];
    mocks.activeScans = [];
    renderPage();

    const moviesRow = screen.getByRole("button", { name: "Edit Movies" }).closest("tr")!;
    const nextRow = moviesRow.nextElementSibling as HTMLElement | null;
    expect(nextRow).not.toBeNull();
    expect(within(nextRow!).getByText("Mount returned 0 files")).toBeInTheDocument();
  });

  it("opens the library editor directly to the Folders section when clicking the folders text", () => {
    renderPage();

    const moviesRow = screen.getByRole("button", { name: "Edit Movies" }).closest("tr")!;
    fireEvent.click(within(moviesRow).getByRole("button", { name: "2 folders" }));

    const dialog = screen.getByTestId("library-editor-dialog");
    expect(dialog).toHaveAttribute("data-library-id", "1");
    expect(dialog).toHaveAttribute("data-initial-section", "folders");

    // Clicking Edit opens on the default General section.
    fireEvent.click(within(moviesRow).getByRole("button", { name: "Edit Movies" }));
    expect(screen.getByTestId("library-editor-dialog")).toHaveAttribute(
      "data-initial-section",
      "general",
    );
  });

  it("waits for refresh jobs before showing the empty Scan Activity state and distinguishes queued refreshes", () => {
    mocks.activeScans = [];
    mocks.refreshJobs = undefined;
    mocks.refreshJobsPending = true;
    const { rerender } = renderPage("/admin/libraries?tab=activity");
    expect(screen.getByText(/Loading/i)).toBeInTheDocument();
    expect(screen.queryByText("No scans running")).not.toBeInTheDocument();

    mocks.refreshJobsPending = false;
    mocks.refreshJobsError = true;
    rerender(
      <MemoryRouter initialEntries={["/admin/libraries?tab=activity"]}>
        <Routes>
          <Route path="/admin/libraries" element={<AdminLibraries />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.queryByText("No scans running")).not.toBeInTheDocument();

    mocks.refreshJobsError = false;
    mocks.refreshJobs = [];
    mocks.refreshJobsHasNextPage = true;
    cleanup();
    renderPage("/admin/libraries?tab=activity");
    fireEvent.click(screen.getByRole("button", { name: "Load older refresh jobs" }));
    expect(mocks.refreshJobsFetchNextPage).toHaveBeenCalledTimes(1);

    mocks.refreshJobsHasNextPage = false;
    mocks.refreshJobs = [
      {
        id: "job-queued-1",
        job_type: "library_metadata_refresh",
        status: "queued",
        created_by_user_id: 1,
        request_payload: { library_id: 1 },
        result_payload: {},
        message: "",
        progress_current: 0,
        progress_total: 0,
        artifact_size_bytes: 0,
        requested_at: new Date().toISOString(),
      },
    ];
    cleanup();
    renderPage("/admin/libraries");
    expect(screen.getAllByText(/Refresh queued/i).length).toBeGreaterThan(0);
    expect(screen.queryByText(/Refreshing metadata/i)).not.toBeInTheDocument();
  });

  it("shows error states with retry for Unmatched Items and Metadata Matcher when queries fail", () => {
    mocks.unmatchedTotal = undefined;
    mocks.unmatchedError = true;
    mocks.matcherTotal = undefined;
    mocks.matcherError = true;
    renderPage("/admin/libraries?tab=health");

    expect(screen.getByRole("alert")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(mocks.unmatchedRefetch).toHaveBeenCalledTimes(1);

    const matcherTab = screen.getByRole("tab", { name: /Metadata Matcher/i });
    fireEvent.mouseDown(matcherTab);
    fireEvent.click(matcherTab);
    expect(screen.getByRole("alert")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(mocks.matcherRefetch).toHaveBeenCalledTimes(1);
  });
});
