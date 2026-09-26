import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useAdminLibraries: vi.fn(),
  useLibraryRefreshJobs: vi.fn(),
  useSkippedLibraryRoots: vi.fn(),
  useStaleMediaIDs: vi.fn(),
  useRematchStaleMediaID: vi.fn(),
  useCheckLibraryMount: vi.fn(),
  useCreateLibrary: vi.fn(),
  useUpdateLibrary: vi.fn(),
  useDeleteLibrary: vi.fn(),
  useScanLibrary: vi.fn(),
  useScanAllLibraries: vi.fn(),
  useRefreshLibraryMetadata: vi.fn(),
  useLibraryMetadataMatchQueues: vi.fn(),
  useLibraryMetadataMatchQueueDetail: vi.fn(),
  useRetryLibraryMetadataMatchQueue: vi.fn(),
  useCancelLibraryMetadataMatchQueue: vi.fn(),
  useConfirmEmptyRootCleanup: vi.fn(),
  useLibraryProviders: vi.fn(),
  useSetLibraryProviders: vi.fn(),
  useReorderLibraries: vi.fn(),
  useUploadLibraryPoster: vi.fn(),
  useDeleteLibraryPoster: vi.fn(),
  useUnmatchedLibraryItems: vi.fn(),
  useAdminPlugins: vi.fn(),
  useCancelLibraryScans: vi.fn(),
  useCancelAdminJob: vi.fn(),
  useLibraryRoots: vi.fn(),
  useUpsertLibraryRootOverride: vi.fn(),
  useDeleteLibraryRootOverride: vi.fn(),
  useActiveScans: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: (...args: unknown[]) => mocks.useAdminLibraries(...args),
  useLibraryRefreshJobs: (...args: unknown[]) => mocks.useLibraryRefreshJobs(...args),
  useSkippedLibraryRoots: (...args: unknown[]) => mocks.useSkippedLibraryRoots(...args),
  useStaleMediaIDs: (...args: unknown[]) => mocks.useStaleMediaIDs(...args),
  flattenStaleMediaIDs: (data?: { pages: { staleIDs: unknown[] }[] }) =>
    data?.pages.flatMap((page) => page.staleIDs) ?? [],
  useRematchStaleMediaID: (...args: unknown[]) => mocks.useRematchStaleMediaID(...args),
  useCheckLibraryMount: (...args: unknown[]) => mocks.useCheckLibraryMount(...args),
  useCreateLibrary: (...args: unknown[]) => mocks.useCreateLibrary(...args),
  useUpdateLibrary: (...args: unknown[]) => mocks.useUpdateLibrary(...args),
  useDeleteLibrary: (...args: unknown[]) => mocks.useDeleteLibrary(...args),
  useScanLibrary: (...args: unknown[]) => mocks.useScanLibrary(...args),
  useScanAllLibraries: (...args: unknown[]) => mocks.useScanAllLibraries(...args),
  useRefreshLibraryMetadata: (...args: unknown[]) => mocks.useRefreshLibraryMetadata(...args),
  useLibraryMetadataMatchQueues: (...args: unknown[]) =>
    mocks.useLibraryMetadataMatchQueues(...args),
  useLibraryMetadataMatchQueueDetail: (...args: unknown[]) =>
    mocks.useLibraryMetadataMatchQueueDetail(...args),
  useRetryLibraryMetadataMatchQueue: (...args: unknown[]) =>
    mocks.useRetryLibraryMetadataMatchQueue(...args),
  useCancelLibraryMetadataMatchQueue: (...args: unknown[]) =>
    mocks.useCancelLibraryMetadataMatchQueue(...args),
  useConfirmEmptyRootCleanup: (...args: unknown[]) => mocks.useConfirmEmptyRootCleanup(...args),
  useLibraryProviders: (...args: unknown[]) => mocks.useLibraryProviders(...args),
  useSetLibraryProviders: (...args: unknown[]) => mocks.useSetLibraryProviders(...args),
  useReorderLibraries: (...args: unknown[]) => mocks.useReorderLibraries(...args),
  useUploadLibraryPoster: (...args: unknown[]) => mocks.useUploadLibraryPoster(...args),
  useDeleteLibraryPoster: (...args: unknown[]) => mocks.useDeleteLibraryPoster(...args),
  useUnmatchedLibraryItems: (...args: unknown[]) => mocks.useUnmatchedLibraryItems(...args),
  useCancelLibraryScans: (...args: unknown[]) => mocks.useCancelLibraryScans(...args),
  useCancelAdminJob: (...args: unknown[]) => mocks.useCancelAdminJob(...args),
  useLibraryRoots: (...args: unknown[]) => mocks.useLibraryRoots(...args),
  flattenLibraryRoots: (data?: { pages: { roots: unknown[] }[] }) =>
    data?.pages.flatMap((page) => page.roots) ?? [],
  useUpsertLibraryRootOverride: (...args: unknown[]) => mocks.useUpsertLibraryRootOverride(...args),
  useDeleteLibraryRootOverride: (...args: unknown[]) => mocks.useDeleteLibraryRootOverride(...args),
  UNMATCHED_PAGE_SIZE: 10,
}));

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPlugins: (...args: unknown[]) => mocks.useAdminPlugins(...args),
}));

vi.mock("@/hooks/queries/admin/scans", () => ({
  useActiveScans: (...args: unknown[]) => mocks.useActiveScans(...args),
}));

import AdminLibraries from "./AdminLibraries";

// unmatchedItemsResult shapes the infinite-query result the unmatched-items
// section reads: one loaded page with no further cursor.
const unmatchedItemsResult = (items: unknown[]) => ({
  data: { pages: [{ items, total: items.length, nextCursor: undefined }], pageParams: [undefined] },
  hasNextPage: false,
  fetchNextPage: vi.fn(),
  isFetching: false,
  isLoading: false,
});

const staleID = (id: string, title: string) => ({
  content_id: id,
  library_id: 1,
  library_name: "Movies",
  title,
  year: 2000,
  content_type: "movie",
  provider: "tmdb",
  provider_id: id,
  first_seen_at: "2026-03-23T20:00:00Z",
  last_seen_at: "2026-03-23T21:00:00Z",
});

// renderPage wraps the page in the providers it needs at runtime: a
// QueryClientProvider for the (mocked) TanStack hooks, and a MemoryRouter for
// the <Link>s inside AdminLibraries. Without QueryClientProvider, even fully
// mocked useQuery hooks throw "No QueryClient set" during render.
const renderPage = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AdminLibraries />
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

const renderInteractivePage = () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AdminLibraries />
      </MemoryRouter>
    </QueryClientProvider>,
  );
};

// Radix Select opens through pointer capture, which jsdom lacks.
if (typeof window !== "undefined" && !window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

const ambiguousRoot = (libraryId: number) => ({
  library_id: libraryId,
  library_name: "Movies",
  root_path: "/media/movies/Inception (2010)",
  state: "ambiguous",
  inferred_type: "movie",
  type_confidence: "low",
  title: "Inception",
  year: 2010,
  observed_file_count: 1,
  sample_file_path: "/media/movies/Inception (2010)/Inception (2010).mkv",
  first_seen_at: "2026-03-23T20:00:00Z",
  last_seen_at: "2026-03-23T21:00:00Z",
});

const libraryRootsResult = (roots: unknown[]) => ({
  data: { pageParams: [undefined], pages: [{ total: roots.length, nextCursor: undefined, roots }] },
  isError: false,
  hasNextPage: false,
  isFetchingNextPage: false,
  fetchNextPage: vi.fn(),
});

// The Ambiguous Roots header: its first child holds the severity icon, and
// the rest of its text is the title, description, and count indicator.
const ambiguousRootsHeader = () => screen.getByRole("button", { name: /Ambiguous Roots/ });
const ambiguousRootsIconBox = () => ambiguousRootsHeader().firstElementChild as HTMLElement;

describe("AdminLibraries", () => {
  afterEach(cleanup);
  beforeEach(() => {
    const mutate = vi.fn();
    const queryState = {
      mutate,
      isPending: false,
      variables: undefined,
    };

    mocks.useAdminLibraries.mockReturnValue({
      data: [
        {
          id: 1,
          name: "Movies",
          paths: ["/media/movies"],
          type: "movies",
          enabled: true,
          last_scanned_at: null,
          scan_warning_code: "empty_root",
          scan_warning_at: null,
          scan_warning_message: null,
        },
      ],
      isLoading: false,
    });
    mocks.useCheckLibraryMount.mockReturnValue(queryState);
    mocks.useLibraryRefreshJobs.mockReturnValue({
      data: [],
      isLoading: false,
    });
    mocks.useSkippedLibraryRoots.mockReturnValue({
      data: { pages: [{ roots: [] }] },
      isLoading: false,
    });
    mocks.useStaleMediaIDs.mockReturnValue({
      data: undefined,
      isFetched: false,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    });
    mocks.useRematchStaleMediaID.mockReturnValue(queryState);
    mocks.useCreateLibrary.mockReturnValue(queryState);
    mocks.useUpdateLibrary.mockReturnValue(queryState);
    mocks.useDeleteLibrary.mockReturnValue(queryState);
    mocks.useScanLibrary.mockReturnValue(queryState);
    mocks.useScanAllLibraries.mockReturnValue(queryState);
    mocks.useRefreshLibraryMetadata.mockReturnValue(queryState);
    mocks.useLibraryMetadataMatchQueues.mockReturnValue({
      data: [],
      isLoading: false,
    });
    mocks.useLibraryMetadataMatchQueueDetail.mockReturnValue({
      data: null,
      isLoading: false,
    });
    mocks.useRetryLibraryMetadataMatchQueue.mockReturnValue(queryState);
    mocks.useCancelLibraryMetadataMatchQueue.mockReturnValue(queryState);
    mocks.useConfirmEmptyRootCleanup.mockReturnValue(queryState);
    mocks.useLibraryProviders.mockReturnValue({
      data: { levels: {} },
      isLoading: false,
    });
    mocks.useSetLibraryProviders.mockReturnValue(queryState);
    mocks.useReorderLibraries.mockReturnValue(queryState);
    mocks.useUploadLibraryPoster.mockReturnValue(queryState);
    mocks.useDeleteLibraryPoster.mockReturnValue(queryState);
    mocks.useAdminPlugins.mockReturnValue({
      installations: [],
      catalog: [],
      repositories: [],
      isLoading: false,
    });
    mocks.useUnmatchedLibraryItems.mockReturnValue(unmatchedItemsResult([]));
    mocks.useCancelLibraryScans.mockReturnValue(queryState);
    mocks.useCancelAdminJob.mockReturnValue(queryState);
    mocks.useLibraryRoots.mockReturnValue({
      data: undefined,
      isLoading: false,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    });
    mocks.useUpsertLibraryRootOverride.mockReturnValue(queryState);
    mocks.useDeleteLibraryRootOverride.mockReturnValue(queryState);
    mocks.useActiveScans.mockReturnValue({ data: [], isLoading: false });
  });

  it("uses scan language instead of metadata refresh language on the admin libraries page", () => {
    const markup = renderPage();

    expect(markup).toContain(
      "Manage library roots and scans. Catalog import/export now lives under Maintenance.",
    );
    expect(markup).toContain('title="Scan Library"');
    expect(markup).toContain("Scan All");
    expect(markup).toContain('title="Rescan Metadata"');
    expect(markup).toContain(
      "Run another scan after storage returns, or confirm deletion before the next empty-root scan.",
    );
  });

  it("shows the partial-walk failure count without offering cleanup", () => {
    mocks.useAdminLibraries.mockReturnValue({
      data: [
        {
          id: 2,
          name: "Ebooks",
          paths: ["/media/ebooks"],
          type: "ebooks",
          enabled: true,
          last_scanned_at: null,
          scan_warning_code: "partial_walk",
          scan_warning_at: null,
          scan_warning_message:
            "Scan could not read or resolve 3 paths; some files were not scanned.",
        },
      ],
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Partial scan");
    expect(markup).toContain("Scan could not read or resolve 3 paths");
    expect(markup).not.toContain("Confirm Cleanup");
    expect(markup).not.toContain("Confirm Empty-Root Cleanup");
  });

  it("offers confirmed cleanup for a suspect-empty dead-root warning", () => {
    mocks.useAdminLibraries.mockReturnValue({
      data: [
        {
          id: 2,
          name: "Audiobooks",
          paths: ["/media/audiobooks"],
          type: "audiobooks",
          enabled: true,
          last_scanned_at: null,
          scan_warning_code: "dead_root",
          scan_warning_at: null,
          scan_warning_message: "Root is reachable but empty",
        },
      ],
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain('title="Confirm cleanup for missing or empty roots"');
  });

  it("renders the collapsed Ambiguous Roots section with a populated count", () => {
    mocks.useLibraryRoots.mockReturnValue({
      data: {
        pageParams: [undefined],
        pages: [
          {
            total: 1,
            nextCursor: undefined,
            roots: [
              {
                library_id: 1,
                library_name: "Movies",
                root_path: "/media/movies/Inception (2010)",
                state: "ambiguous",
                inferred_type: "movie",
                type_confidence: "low",
                title: "Inception",
                year: 2010,
                observed_file_count: 1,
                sample_file_path: "/media/movies/Inception (2010)/Inception (2010).mkv",
                first_seen_at: "2026-03-23T20:00:00Z",
                last_seen_at: "2026-03-23T21:00:00Z",
              },
            ],
          },
        ],
      },
      isLoading: false,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    });

    renderInteractivePage();

    expect(ambiguousRootsHeader()).toHaveTextContent("1");
    expect(ambiguousRootsIconBox()).toHaveClass("bg-amber-500/10");
    expect(ambiguousRootsIconBox().querySelector("svg")).toHaveClass("text-amber-500");
  });

  it("shows metadata matcher pending and parked counts", () => {
    mocks.useLibraryMetadataMatchQueues.mockReturnValue({
      data: [
        {
          library_id: 1,
          movie_count: 1,
          series_count: 2,
          raw_file_count: 0,
          total_count: 3,
          pending_count: 2,
          parked_count: 1,
        },
      ],
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Metadata Matcher");
    expect(markup).toContain("Pending and parked items that still need a provider match.");
    // The total renders as element text (">3<"); a bare "3" would also match
    // Tailwind class names like p-3 and prove nothing.
    expect(markup).toMatch(/>\s*3\s*</);
  });

  it("shows active scan progress in the library task status row", () => {
    mocks.useActiveScans.mockReturnValue({
      data: [
        {
          id: "scan-1",
          library_id: 1,
          mode: "library",
          trigger: "manual",
          status: "running",
          started_at: "2026-03-23T20:00:00Z",
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
            message: "Processing files",
            total_files: 20,
            files_processed: 10,
          },
        },
      ],
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Processing files · 10 / 20 (50%)");
    // Mode and target render as separate compact-row elements.
    expect(markup).toContain("Full library scan");
    expect(markup).toContain("Entire library");
  });

  it("shows an unknown count, not a zero, before Ambiguous Roots has loaded", () => {
    // The roots query is disabled while the section is collapsed, so the
    // default mock has no data. The section itself still renders because it
    // is gated on libraries.length, and the missing page is not a confirmed zero.
    renderInteractivePage();

    expect(mocks.useLibraryRoots).toHaveBeenLastCalledWith(1, "ambiguous", {
      enabled: false,
      search: "",
    });
    expect(ambiguousRootsHeader()).toHaveTextContent("Count not loaded");
    expect(ambiguousRootsHeader()).not.toHaveTextContent(/\b0\b/);
    expect(ambiguousRootsIconBox()).toHaveClass("bg-muted/50");
  });

  it("renders a loading row, not an empty result, while Ambiguous Roots loads", () => {
    renderInteractivePage();

    fireEvent.click(ambiguousRootsHeader());

    expect(mocks.useLibraryRoots).toHaveBeenLastCalledWith(1, "ambiguous", {
      enabled: true,
      search: "",
    });
    expect(screen.getByText("Loading ambiguous roots for this library.")).toBeInTheDocument();
    expect(screen.queryByText("No ambiguous roots for this library.")).toBeNull();
  });

  it("renders a confirmed empty Ambiguous Roots result with neutral styling", () => {
    mocks.useLibraryRoots.mockReturnValue(libraryRootsResult([]));

    renderInteractivePage();

    expect(ambiguousRootsHeader()).toHaveTextContent("0");
    expect(ambiguousRootsHeader()).not.toHaveTextContent("Count not loaded");
    expect(ambiguousRootsIconBox()).toHaveClass("bg-muted/50");
    expect(ambiguousRootsIconBox().querySelector("svg")).not.toHaveClass("text-amber-500");

    fireEvent.click(ambiguousRootsHeader());

    expect(screen.getByText("No ambiguous roots for this library.")).toBeInTheDocument();
  });

  it("renders an error state when Ambiguous Roots fails to load", () => {
    mocks.useLibraryRoots.mockReturnValue({
      ...libraryRootsResult([]),
      data: undefined,
      isError: true,
    });

    renderInteractivePage();

    expect(ambiguousRootsHeader()).toHaveTextContent("Error loading count");
    expect(ambiguousRootsIconBox()).toHaveClass("bg-destructive/10");

    fireEvent.click(ambiguousRootsHeader());

    expect(
      screen.getByText("Failed to load ambiguous roots for this library."),
    ).toBeInTheDocument();
  });

  it("keeps loaded Ambiguous Roots visible when a later page fails", () => {
    mocks.useLibraryRoots.mockReturnValue({
      ...libraryRootsResult([ambiguousRoot(1)]),
      isError: true,
    });

    renderInteractivePage();
    fireEvent.click(ambiguousRootsHeader());

    expect(ambiguousRootsHeader()).toHaveTextContent("1");
    expect(screen.getByText("/media/movies/Inception (2010)")).toBeInTheDocument();
    expect(screen.queryByText("Failed to load ambiguous roots for this library.")).toBeNull();
  });

  it("queries and styles Ambiguous Roots per selected library", async () => {
    mocks.useAdminLibraries.mockReturnValue({
      data: [
        {
          id: 1,
          name: "Movies",
          paths: ["/media/movies"],
          type: "movies",
          enabled: true,
          last_scanned_at: null,
          scan_warning_code: null,
          scan_warning_at: null,
          scan_warning_message: null,
        },
        {
          id: 42,
          name: "Television",
          paths: ["/media/tv"],
          type: "tv",
          enabled: true,
          last_scanned_at: null,
          scan_warning_code: null,
          scan_warning_at: null,
          scan_warning_message: null,
        },
      ],
      isLoading: false,
    });
    mocks.useLibraryRoots.mockImplementation((libraryId: number) =>
      libraryId === 42 ? libraryRootsResult([ambiguousRoot(42)]) : libraryRootsResult([]),
    );

    renderInteractivePage();
    fireEvent.click(ambiguousRootsHeader());

    expect(ambiguousRootsIconBox()).toHaveClass("bg-muted/50");
    expect(screen.getByText("No ambiguous roots for this library.")).toBeInTheDocument();

    const section = ambiguousRootsHeader().closest("section") as HTMLElement;
    await userEvent.click(within(section).getByRole("combobox"));
    await userEvent.click(await screen.findByRole("option", { name: "Television" }));

    expect(mocks.useLibraryRoots).toHaveBeenLastCalledWith(42, "ambiguous", {
      enabled: true,
      search: "",
    });
    expect(ambiguousRootsIconBox()).toHaveClass("bg-amber-500/10");
    expect(screen.getByText("/media/movies/Inception (2010)")).toBeInTheDocument();
  });

  it("renders Stale External IDs collapsed by default and loads the first page on demand", () => {
    mocks.useStaleMediaIDs.mockReturnValue({
      data: {
        pages: [
          {
            staleIDs: [
              {
                content_id: "movie-1",
                library_id: 1,
                library_name: "Movies",
                title: "Inception",
                year: 2010,
                content_type: "movie",
                provider: "tmdb",
                provider_id: "27205",
                first_seen_at: "2026-03-23T20:00:00Z",
                last_seen_at: "2026-03-23T21:00:00Z",
              },
            ],
            nextCursor: undefined,
          },
        ],
      },
      isFetched: true,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    });

    const markup = renderPage();

    expect(markup).toContain("Stale External IDs");
    expect(markup).not.toContain("Re-match");
    // The section is collapsed on mount, so the first page is not requested yet.
    expect(mocks.useStaleMediaIDs).toHaveBeenCalledWith({ enabled: false, search: "" });
  });

  describe("diagnostic section header counts", () => {
    const infinite = (data: unknown, extra: Record<string, unknown> = {}) => ({
      data,
      isLoading: false,
      isError: false,
      isFetched: data !== undefined,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
      ...extra,
    });
    const skippedRoot = {
      library_id: 1,
      library_name: "Movies",
      root_path: "/media/movies/Unknown Movie",
      reason: "missing_provider_ids",
      file_count: 2,
      sample_file_path: "/media/movies/Unknown Movie/movie.mkv",
      first_seen_at: "2026-03-23T20:00:00Z",
      last_seen_at: "2026-03-23T21:00:00Z",
    };
    const header = (name: RegExp) => screen.getByRole("button", { name });
    const renderInteractive = () => {
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      return render(
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <AdminLibraries />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    };

    it("shows an unknown count instead of 0 while the sections are collapsed and unloaded", () => {
      mocks.useSkippedLibraryRoots.mockReturnValue(infinite(undefined));
      mocks.useStaleMediaIDs.mockReturnValue(infinite(undefined));
      renderInteractive();

      for (const name of [/Troubleshooting/, /Stale External IDs/]) {
        expect(within(header(name)).getByText("Count not loaded")).toBeDefined();
        expect(within(header(name)).queryByText("0")).toBeNull();
      }
    });

    it("keeps the count unknown while the first page loads after opening", () => {
      mocks.useSkippedLibraryRoots.mockReturnValue(infinite(undefined, { isLoading: true }));
      mocks.useStaleMediaIDs.mockReturnValue(infinite(undefined, { isLoading: true }));
      renderInteractive();

      fireEvent.click(header(/Troubleshooting/));
      fireEvent.click(header(/Stale External IDs/));
      expect(mocks.useSkippedLibraryRoots).toHaveBeenLastCalledWith({ enabled: true, search: "" });
      expect(mocks.useStaleMediaIDs).toHaveBeenLastCalledWith({ enabled: true, search: "" });
      for (const name of [/Troubleshooting/, /Stale External IDs/]) {
        expect(within(header(name)).getByText("Count not loaded")).toBeDefined();
      }
    });

    it("shows 0 once the server confirms an empty result", () => {
      mocks.useSkippedLibraryRoots.mockReturnValue(
        infinite({ pages: [{ roots: [], nextCursor: undefined, total: 0 }] }),
      );
      mocks.useStaleMediaIDs.mockReturnValue(
        infinite({ pages: [{ staleIDs: [], nextCursor: undefined, total: 0 }] }),
      );
      renderInteractive();

      for (const name of [/Troubleshooting/, /Stale External IDs/]) {
        expect(within(header(name)).getByText("0")).toBeDefined();
        expect(within(header(name)).queryByText("Count not loaded")).toBeNull();
      }
    });

    it("shows the server total rather than the rows loaded so far", () => {
      mocks.useSkippedLibraryRoots.mockReturnValue(
        infinite({ pages: [{ roots: [skippedRoot], nextCursor: "next", total: 1 }] }),
      );
      mocks.useStaleMediaIDs.mockReturnValue(
        infinite(
          {
            pages: [
              {
                staleIDs: [staleID("a", "Alpha"), staleID("b", "Bravo")],
                nextCursor: "next",
                total: 120,
              },
              { staleIDs: [staleID("c", "Charlie")], nextCursor: "later", total: 120 },
            ],
          },
          { hasNextPage: true },
        ),
      );
      renderInteractive();

      expect(within(header(/Troubleshooting/)).getByText("1")).toBeDefined();
      expect(within(header(/Stale External IDs/)).getByText("120")).toBeDefined();
      fireEvent.click(header(/Stale External IDs/));
      expect(within(header(/Stale External IDs/)).getByText("120")).toBeDefined();
      expect(within(header(/Stale External IDs/)).queryByText("3")).toBeNull();
    });

    it("keeps the count unknown when the listing fails to load", () => {
      mocks.useSkippedLibraryRoots.mockReturnValue(infinite(undefined, { isError: true }));
      mocks.useStaleMediaIDs.mockReturnValue(infinite(undefined, { isError: true }));
      renderInteractive();

      fireEvent.click(header(/Troubleshooting/));
      fireEvent.click(header(/Stale External IDs/));
      for (const name of [/Troubleshooting/, /Stale External IDs/]) {
        expect(within(header(name)).getByText("Count not loaded")).toBeDefined();
        expect(within(header(name)).queryByText("0")).toBeNull();
      }
    });
  });

  it("reopens Stale External IDs after an empty result to show newly discovered IDs", () => {
    const emptyResult = {
      data: { pages: [{ staleIDs: [], nextCursor: undefined }] },
      isFetched: true,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    };
    mocks.useStaleMediaIDs.mockReturnValue({ ...emptyResult, data: undefined, isFetched: false });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const page = () => (
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <AdminLibraries />
        </MemoryRouter>
      </QueryClientProvider>
    );
    const view = render(page());
    fireEvent.click(screen.getByRole("button", { name: /Stale External IDs/ }));
    expect(mocks.useStaleMediaIDs).toHaveBeenLastCalledWith({ enabled: true, search: "" });
    mocks.useStaleMediaIDs.mockReturnValue(emptyResult);
    view.rerender(page());
    fireEvent.click(screen.getByRole("button", { name: /Stale External IDs/ }));
    const reopen = screen.getByRole("button", { name: /Stale External IDs/ });
    expect(reopen.getAttribute("aria-expanded")).toBe("false");
    mocks.useStaleMediaIDs.mockReturnValue({
      ...emptyResult,
      data: { pages: [{ staleIDs: [staleID("new", "Newly discovered")], nextCursor: undefined }] },
    });
    fireEvent.click(reopen);
    expect(mocks.useStaleMediaIDs).toHaveBeenLastCalledWith({ enabled: true, search: "" });
    expect(screen.getByText("Newly discovered")).toBeDefined();
  });

  it("keeps stale IDs in server page order without offering partial-result sort controls", () => {
    const firstPage = {
      staleIDs: [staleID("newest", "Zulu"), staleID("middle", "Middle")],
      nextCursor: "older",
    };
    const result = {
      data: { pages: [firstPage] },
      isFetched: true,
      hasNextPage: true,
      isFetchingNextPage: false,
      fetchNextPage: vi.fn(),
    };
    mocks.useStaleMediaIDs.mockImplementation(() => result);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const page = () => (
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <AdminLibraries />
        </MemoryRouter>
      </QueryClientProvider>
    );
    const view = render(page());
    fireEvent.click(screen.getByRole("button", { name: /Stale External IDs/ }));
    const table = screen.getByRole("table", { name: "Stale external IDs" });
    for (const label of ["Title", "Year", "Library", "Provider", "First seen", "Last seen"]) {
      expect(within(table).queryByRole("button", { name: label })).toBeNull();
    }
    const titles = () =>
      within(table)
        .getAllByRole("row")
        .slice(1)
        .map((row) => within(row).getAllByRole("cell")[0]?.textContent);
    expect(titles()).toEqual(["Zulu", "Middle"]);
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(result.fetchNextPage).toHaveBeenCalledTimes(1);
    result.data = {
      pages: [firstPage, { staleIDs: [staleID("oldest", "Alpha")], nextCursor: "last" }],
    };
    view.rerender(page());
    expect(titles()).toEqual(["Zulu", "Middle", "Alpha"]);
  });

  it.each(["search", "first", "previous"])(
    "cancels a pending Last traversal after %s",
    async (action) => {
      const item = {
        content_id: "movie:fixture",
        library_id: 1,
        title: "Fixture",
        year: 1995,
        content_type: "movie",
        library_name: "Movies",
        status: "unmatched",
      };
      const pages = [0, 1].map((n) => ({
        items: [{ ...item, content_id: `movie:${n}`, title: `Page ${n}` }],
        total: 40,
        nextCursor: `cursor-${n}`,
      }));
      type Result = { data: { pages: typeof pages }; hasNextPage: boolean };
      let complete!: (result: Result) => void;
      const pending = new Promise<Result>((resolve) => {
        complete = resolve;
      });
      const fetchNextPage = vi
        .fn()
        .mockReturnValueOnce(pending)
        .mockResolvedValue({ data: { pages: [...pages, ...pages] }, hasNextPage: false });
      mocks.useUnmatchedLibraryItems.mockReturnValue({
        data: { pages },
        hasNextPage: true,
        isFetching: false,
        fetchNextPage,
      });
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <AdminLibraries />
          </MemoryRouter>
        </QueryClientProvider>,
      );
      fireEvent.click(screen.getByText("Unmatched Items"));
      fireEvent.click(screen.getByTitle("Next page"));
      expect(screen.getByText("Page 1")).toBeDefined();
      fireEvent.click(screen.getByTitle("Last page"));
      expect(fetchNextPage).toHaveBeenCalledTimes(1);
      if (action === "search") {
        fireEvent.change(
          screen.getByPlaceholderText("Search all unmatched items by title, library, or type..."),
          { target: { value: "new search" } },
        );
      } else {
        fireEvent.click(screen.getByTitle(action === "first" ? "First page" : "Previous page"));
      }
      await act(async () => {
        complete({ data: { pages: pages.concat(pages.slice(0, 1)) }, hasNextPage: true });
        await pending;
      });
      expect(fetchNextPage).toHaveBeenCalledTimes(1);
      expect(screen.getByText("Page 0")).toBeDefined();
    },
  );

  it("renders an unmatched items section collapsed by default when unmatched items exist", () => {
    mocks.useUnmatchedLibraryItems.mockReturnValue(
      unmatchedItemsResult([
        {
          content_id: "movie-99",
          title: "Unknown Film",
          year: 0,
          content_type: "movie",
          library_id: 1,
          library_name: "Movies",
          status: "unmatched",
        },
      ]),
    );

    const markup = renderPage();

    expect(markup).toContain("Unmatched Items");
    expect(markup).toContain("Items that could not be matched to any metadata provider.");
  });

  it("renders the Troubleshooting section collapsed by default when skipped roots exist", () => {
    mocks.useSkippedLibraryRoots.mockReturnValue({
      data: {
        pages: [
          {
            roots: [
              {
                library_id: 1,
                library_name: "Movies",
                root_path: "/media/movies/Unknown Movie",
                reason: "missing_provider_ids",
                file_count: 2,
                sample_file_path: "/media/movies/Unknown Movie/movie.mkv",
                first_seen_at: "2026-03-23T20:00:00Z",
                last_seen_at: "2026-03-23T21:00:00Z",
              },
            ],
          },
        ],
      },
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Troubleshooting");
    expect(markup).toContain(
      "Roots where the inferred canonical folder lacks embedded provider IDs.",
    );
    expect(markup).not.toContain("Filter by path, library, or reason");
    expect(markup).not.toContain("Unknown Movie");
  });

  it("hides unmatched items section when no unmatched items exist", () => {
    const markup = renderPage();

    expect(markup).not.toContain("Unmatched Items");
  });
});
