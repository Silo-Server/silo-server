import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AdminArtworkStorage, { formatArtworkBytes } from "./AdminArtworkStorage";

const mockState = vi.hoisted(() => ({
  storeHealth: "healthy",
  libraries: [
    {
      library_id: 7,
      referenced_bytes: 2048,
      exclusive_bytes: 1024,
      shared_bytes: 1024,
      reclaimable_bytes: 128,
      protected_bytes: 0,
      object_count: 4,
      revision_count: 1,
    },
  ] as Array<Record<string, number>> | null,
}));

vi.mock("@/hooks/queries/admin/artworkStorage", () => ({
  useArtworkStorage: () => ({
    isLoading: false,
    isError: false,
    data: {
      snapshot_at: "2026-08-25T12:00:00Z",
      backend: "local",
      resolved_path: "/var/lib/silo/artwork",
      store_health: mockState.storeHealth,
      complete: false,
      known_bytes: 4096,
      total: {
        physical_bytes: 4096,
        pending_gc_bytes: 512,
        protected_bytes: 1024,
        reclaimable_bytes: 256,
        object_count: 8,
        revision_count: 2,
      },
      libraries: mockState.libraries,
      coverage_limited: true,
      coverage_limit_reason: "Untracked user artwork",
      failure_count: 0,
      untracked_user_artwork: true,
      seed: {
        bytes: 0,
        expired_bytes: 0,
        revisions: 0,
        retained_unverifiable_bytes: 0,
        retained_unverifiable_revisions: 0,
      },
    },
  }),
  useRefreshArtworkStorage: () => ({ mutate: vi.fn(), isPending: false }),
  useImportArtworkStorage: () => ({ mutate: vi.fn(), isPending: false }),
  useRebuildArtworkStorage: () => ({ mutate: vi.fn(), isPending: false }),
  usePurgeArtworkStorage: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [{ id: 7, name: "Movies" }] }),
  useAllAdminJobs: () => ({ data: [] }),
}));

describe("AdminArtworkStorage", () => {
  beforeEach(() => {
    mockState.storeHealth = "healthy";
    mockState.libraries = [
      {
        library_id: 7,
        referenced_bytes: 2048,
        exclusive_bytes: 1024,
        shared_bytes: 1024,
        reclaimable_bytes: 128,
        protected_bytes: 0,
        object_count: 4,
        revision_count: 1,
      },
    ];
  });

  it("labels non-additive accounting and honest coverage state", () => {
    const markup = renderToStaticMarkup(<AdminArtworkStorage />);
    expect(markup).toContain("Local artwork storage");
    expect(markup).toContain("Incomplete");
    expect(markup).toContain("Coverage limited");
    expect(markup).toContain("Shared (non-additive)");
    expect(markup).toContain("Movies");
    expect(markup).toContain("Free artwork storage");
  });

  it("formats bounded byte values and absent capacity", () => {
    expect(formatArtworkBytes(1024)).toBe("1.00 KiB");
    expect(formatArtworkBytes(undefined)).toBe("Not reported");
  });

  it("renders a fresh zero-library snapshot whose JSON libraries value is null", () => {
    mockState.libraries = null;
    const markup = renderToStaticMarkup(<AdminArtworkStorage />);
    expect(markup).toContain("Shared (non-additive)");
    expect(markup).not.toContain("Library #");
  });

  it("offers an explicit rebuild for an unavailable local store", () => {
    mockState.storeHealth = "unavailable";
    const markup = renderToStaticMarkup(<AdminArtworkStorage />);
    expect(markup).toContain("Rebuild artwork store");
  });
});
