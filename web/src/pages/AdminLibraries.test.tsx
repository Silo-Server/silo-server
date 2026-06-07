import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

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

describe("AdminLibraries", () => {
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
      data: [],
      isLoading: false,
    });
    mocks.useStaleMediaIDs.mockReturnValue({
      data: [],
      isLoading: false,
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
    mocks.useUnmatchedLibraryItems.mockReturnValue({
      data: { items: [], total: 0 },
      isLoading: false,
    });
    mocks.useCancelLibraryScans.mockReturnValue(queryState);
    mocks.useCancelAdminJob.mockReturnValue(queryState);
    mocks.useLibraryRoots.mockReturnValue({ data: [], isLoading: false });
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

  it("renders the collapsed Ambiguous Roots section with a populated count", () => {
    mocks.useLibraryRoots.mockReturnValue({
      data: [
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
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Ambiguous Roots");
    expect(markup).toContain("Scanner roots that stay visible");
  });

  it("does not show metadata matcher queue counts in the library status", () => {
    mocks.useLibraryMetadataMatchQueues.mockReturnValue({
      data: [
        {
          library_id: 1,
          movie_count: 1,
          series_count: 2,
          raw_file_count: 0,
          total_count: 3,
        },
      ],
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Enabled");
    expect(markup).not.toContain("3 matching");
    expect(markup).not.toContain("View backlog");
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
    expect(markup).toContain("Full library scan · Entire library");
  });

  it("renders the collapsed Ambiguous Roots section when no roots exist", () => {
    // Default useLibraryRoots mock returns { data: [], isLoading: false }. The
    // section itself still renders because it is gated on libraries.length.
    const markup = renderPage();

    expect(markup).toContain("Ambiguous Roots");
    expect(markup).toContain("Scanner roots that stay visible");
  });

  it("renders Stale External IDs collapsed by default", () => {
    mocks.useStaleMediaIDs.mockReturnValue({
      data: [
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
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Stale External IDs");
    expect(markup).not.toContain("Re-match");
  });

  it("renders an unmatched items section collapsed by default when unmatched items exist", () => {
    mocks.useUnmatchedLibraryItems.mockReturnValue({
      data: {
        items: [
          {
            content_id: "movie-99",
            title: "Unknown Film",
            year: 0,
            content_type: "movie",
            library_id: 1,
            library_name: "Movies",
            status: "unmatched",
          },
        ],
        total: 1,
      },
      isLoading: false,
    });

    const markup = renderPage();

    expect(markup).toContain("Unmatched Items");
    expect(markup).toContain("Items that could not be matched to any metadata provider.");
  });

  it("renders the Troubleshooting section collapsed by default when skipped roots exist", () => {
    mocks.useSkippedLibraryRoots.mockReturnValue({
      data: [
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
