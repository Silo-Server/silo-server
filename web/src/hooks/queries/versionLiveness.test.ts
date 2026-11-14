import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { FileVersion } from "@/api/types";

const mocks = vi.hoisted(() => ({
  v2: vi.fn(),
  apiV1: vi.fn(),
}));

vi.mock("@/api/v2/request", () => ({
  v2: mocks.v2,
}));

vi.mock("@/api/client", () => ({
  api: mocks.apiV1,
  apiV1: mocks.apiV1,
}));

import {
  VERSION_LIVENESS_BATCH_SIZE,
  applyVersionAvailability,
  chunkVirtualFileIds,
  fetchVersionLiveness,
  mergeVersionLiveness,
  useVersionLiveness,
} from "./versionLiveness";

function makeVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    resolution: overrides.resolution ?? "1080p",
    codec_video: overrides.codec_video ?? "h264",
    codec_audio: overrides.codec_audio ?? "aac",
    hdr: overrides.hdr ?? false,
    container: overrides.container ?? "mkv",
    file_size: overrides.file_size ?? 0,
    duration: overrides.duration ?? 0,
    bitrate: overrides.bitrate ?? 0,
    file_path: overrides.file_path,
    available: overrides.available,
    failed: overrides.failed,
    source_mismatch: overrides.source_mismatch,
  };
}

function createQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
}

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client: queryClient }, children);
  };
}

describe("fetchVersionLiveness", () => {
  beforeEach(() => {
    mocks.v2.mockReset();
    mocks.apiV1.mockReset();
  });

  it("POSTs the file_ids to the v2 check operation and never the retired v1 route", async () => {
    mocks.v2.mockResolvedValue({ results: [] });
    await fetchVersionLiveness([1, 2, 3]);

    expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/catalog/versions/check", {
      body: { file_ids: ["1", "2", "3"] },
    });
    expect(mocks.apiV1).not.toHaveBeenCalled();
  });

  it("maps the v2 string ids and source mismatch back to numeric rows", async () => {
    mocks.v2.mockResolvedValue({
      results: [
        {
          file_id: 40,
          available: false,
          failed: true,
          source_mismatch: { served_bytes: 42, declared_bytes: 64 },
        },
      ],
    });

    expect(await fetchVersionLiveness([40])).toEqual({
      results: [
        {
          file_id: 40,
          available: false,
          failed: true,
          source_mismatch: { served_bytes: 42, declared_bytes: 64 },
        },
      ],
    });
  });
});

describe("chunkVirtualFileIds", () => {
  it("only includes virtual versions and sorts them ascending", () => {
    const versions = [
      makeVersion({ file_id: 3, container: "virtual" }),
      makeVersion({ file_id: 1, file_path: "virtual://movie/tt1?result=abc" }),
      makeVersion({ file_id: 2, container: "mkv" }),
    ];
    expect(chunkVirtualFileIds(versions)).toEqual([[1, 3]]);
  });

  it("chunks batches larger than the cap", () => {
    const versions = Array.from({ length: VERSION_LIVENESS_BATCH_SIZE + 5 }, (_, i) =>
      makeVersion({ file_id: i + 1, container: "virtual" }),
    );
    const chunks = chunkVirtualFileIds(versions);
    expect(chunks).toHaveLength(2);
    expect(chunks[0]).toHaveLength(VERSION_LIVENESS_BATCH_SIZE);
    expect(chunks[1]).toEqual([
      VERSION_LIVENESS_BATCH_SIZE + 1,
      VERSION_LIVENESS_BATCH_SIZE + 2,
      VERSION_LIVENESS_BATCH_SIZE + 3,
      VERSION_LIVENESS_BATCH_SIZE + 4,
      VERSION_LIVENESS_BATCH_SIZE + 5,
    ]);
  });

  it("returns no chunks when there are no virtual versions", () => {
    expect(chunkVirtualFileIds([makeVersion({ file_id: 1 })])).toEqual([]);
  });
});

describe("mergeVersionLiveness", () => {
  it("marks a version unavailable from item metadata", () => {
    const versions = [makeVersion({ file_id: 1, available: false })];
    expect(mergeVersionLiveness(versions, undefined).get(1)?.available).toBe(false);
  });

  it("marks a version unavailable from a check result", () => {
    const versions = [makeVersion({ file_id: 1 })];
    const merged = mergeVersionLiveness(versions, {
      results: [{ file_id: 1, available: false }],
    });
    expect(merged.get(1)?.available).toBe(false);
  });

  it("keeps a version available when the check reports it so", () => {
    const versions = [makeVersion({ file_id: 1 })];
    const merged = mergeVersionLiveness(versions, {
      results: [{ file_id: 1, available: true }],
    });
    expect(merged.get(1)?.available).toBe(true);
  });

  it("recovers a version the metadata marked unavailable when the check reports it available", () => {
    const versions = [makeVersion({ file_id: 1, available: false })];
    const merged = mergeVersionLiveness(versions, {
      results: [{ file_id: 1, available: true }],
    });
    expect(merged.get(1)?.available).toBe(true);
  });

  it("keeps a version unavailable when the check confirms the metadata", () => {
    const versions = [makeVersion({ file_id: 1, available: false })];
    const merged = mergeVersionLiveness(versions, {
      results: [{ file_id: 1, available: false }],
    });
    expect(merged.get(1)?.available).toBe(false);
  });

  it("marks a version unavailable when the check reports it so despite metadata saying available", () => {
    const versions = [makeVersion({ file_id: 1, available: true })];
    const merged = mergeVersionLiveness(versions, {
      results: [{ file_id: 1, available: false }],
    });
    expect(merged.get(1)?.available).toBe(false);
  });

  it("leaves virtual versions without a result unknown (absent from the map)", () => {
    const versions = [makeVersion({ file_id: 1, container: "virtual" })];
    expect(mergeVersionLiveness(versions, undefined).has(1)).toBe(false);
  });

  it("does not mark non-virtual versions unavailable", () => {
    const versions = [makeVersion({ file_id: 1 })];
    expect(mergeVersionLiveness(versions, undefined).has(1)).toBe(false);
  });
});

describe("useVersionLiveness", () => {
  beforeEach(() => {
    mocks.v2.mockReset();
    mocks.apiV1.mockReset();
  });

  it("does not fire while disabled", () => {
    const versions = [makeVersion({ file_id: 1, container: "virtual" })];
    renderHook(() => useVersionLiveness(versions, false), {
      wrapper: createWrapper(createQueryClient()),
    });

    expect(mocks.v2).not.toHaveBeenCalled();
  });

  it("fires one batched request per chunk with the sorted virtual file_ids", async () => {
    const versions = [
      makeVersion({ file_id: 2, container: "virtual" }),
      makeVersion({ file_id: 1, container: "virtual" }),
      makeVersion({ file_id: 3, container: "mkv" }),
    ];
    mocks.v2.mockResolvedValue({
      results: [
        { file_id: "1", available: true },
        { file_id: "2", available: false },
      ],
    });

    const { result } = renderHook(() => useVersionLiveness(versions, true), {
      wrapper: createWrapper(createQueryClient()),
    });

    await waitFor(() => expect(result.current.get(2)?.available).toBe(false));

    expect(mocks.v2).toHaveBeenCalledTimes(1);
    expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/catalog/versions/check", {
      body: { file_ids: ["1", "2"] },
    });
    expect(mocks.apiV1).not.toHaveBeenCalled();
    expect(result.current.get(1)?.available).toBe(true);
    expect(result.current.has(3)).toBe(false);
  });

  it("merges item metadata with check results (check result wins as fresher)", async () => {
    const versions = [
      makeVersion({ file_id: 1, container: "virtual", available: false }),
      makeVersion({ file_id: 2, container: "virtual" }),
    ];
    mocks.v2.mockResolvedValue({
      results: [
        { file_id: "1", available: true },
        { file_id: "2", available: false },
      ],
    });

    const { result } = renderHook(() => useVersionLiveness(versions, true), {
      wrapper: createWrapper(createQueryClient()),
    });

    await waitFor(() => expect(result.current.get(2)?.available).toBe(false));

    // The check ran after the item snapshot, so its result wins: the version
    // the metadata marked unavailable is recovered.
    expect(result.current.get(1)?.available).toBe(true);
  });

  it("does not re-fire for an unchanged selection", async () => {
    // The watch page rebuilds its version rows on every payload refresh. A
    // fresh array with the same virtual ids must keep the cached chunk keys,
    // or the check re-probes the same rows for every re-render.
    const rows = [makeVersion({ file_id: 1, container: "virtual" })];
    mocks.v2.mockResolvedValue({ results: [{ file_id: "1", available: true }] });

    const { result, rerender } = renderHook(
      ({ versions }: { versions: FileVersion[] }) => useVersionLiveness(versions, true),
      {
        wrapper: createWrapper(createQueryClient()),
        initialProps: { versions: rows },
      },
    );

    await waitFor(() => expect(result.current.get(1)?.available).toBe(true));
    expect(mocks.v2).toHaveBeenCalledTimes(1);

    rerender({ versions: [makeVersion({ file_id: 1, container: "virtual" })] });
    rerender({ versions: [makeVersion({ file_id: 1, container: "virtual", resolution: "4k" })] });

    await waitFor(() => expect(result.current.get(1)?.available).toBe(true));
    expect(mocks.v2).toHaveBeenCalledTimes(1);
  });

  it("caches results across re-enables (no second request while fresh)", async () => {
    const versions = [makeVersion({ file_id: 1, container: "virtual" })];
    mocks.v2.mockResolvedValue({ results: [{ file_id: "1", available: false }] });

    const queryClient = createQueryClient();
    const { result, rerender } = renderHook(
      ({ enabled }: { enabled: boolean }) => useVersionLiveness(versions, enabled),
      {
        wrapper: createWrapper(queryClient),
        initialProps: { enabled: true },
      },
    );

    await waitFor(() => expect(result.current.get(1)?.available).toBe(false));
    expect(mocks.v2).toHaveBeenCalledTimes(1);

    rerender({ enabled: false });
    rerender({ enabled: true });

    await waitFor(() => expect(result.current.get(1)?.available).toBe(false));
    expect(mocks.v2).toHaveBeenCalledTimes(1);
  });
});

describe("applyVersionAvailability", () => {
  it("stamps a reported verdict and clears a stale one", () => {
    const versions = [makeVersion({ file_id: 1 }), makeVersion({ file_id: 2, available: false })];

    const next = applyVersionAvailability(
      versions,
      new Map([
        [1, { available: false }],
        [2, { available: true }],
      ]),
    );

    expect(next[0]?.available).toBe(false);
    expect(next[1]?.available).toBe(true);
  });

  it("leaves rows the check did not report, and their identity, untouched", () => {
    const versions = [makeVersion({ file_id: 1 })];

    const next = applyVersionAvailability(versions, new Map());

    expect(next).toBe(versions);
    expect(next[0]?.available).toBeUndefined();
  });

  it("keeps the list identity when nothing changed", () => {
    const versions = [makeVersion({ file_id: 1, available: true })];

    expect(applyVersionAvailability(versions, new Map([[1, { available: true }]]))).toBe(versions);
  });
});

describe("mergeVersionLiveness source mismatch", () => {
  it("carries a mismatch verdict from the check result", () => {
    const versions = [makeVersion({ file_id: 1, container: "virtual" })];
    const merged = mergeVersionLiveness(versions, {
      results: [
        {
          file_id: 1,
          available: true,
          source_mismatch: { served_bytes: 7_000, declared_bytes: 8_000 },
        },
      ],
    });
    expect(merged.get(1)).toEqual({
      available: true,
      failed: undefined,
      sourceMismatch: { servedBytes: 7_000, declaredBytes: 8_000 },
    });
  });

  it("leaves an unprobed row without a mismatch verdict", () => {
    const versions = [makeVersion({ file_id: 1, container: "virtual" })];
    const merged = mergeVersionLiveness(versions, {
      results: [{ file_id: 1, available: true }],
    });
    expect(merged.get(1)?.sourceMismatch).toBeUndefined();
  });
});

describe("applyVersionAvailability source mismatch", () => {
  it("stamps the mismatch onto the row without marking it failed", () => {
    const versions = [makeVersion({ file_id: 1 })];
    const next = applyVersionAvailability(
      versions,
      new Map([
        [1, { available: true, sourceMismatch: { servedBytes: 7_000, declaredBytes: 8_000 } }],
      ]),
    );
    expect(next[0]?.available).toBe(true);
    expect(next[0]?.failed).toBeUndefined();
    expect(next[0]?.source_mismatch).toEqual({ served_bytes: 7_000, declared_bytes: 8_000 });
  });

  it("clears a stale mismatch when the check reports none", () => {
    const versions = [
      makeVersion({
        file_id: 1,
        available: true,
        source_mismatch: { served_bytes: 7_000, declared_bytes: 8_000 },
      }),
    ];
    const next = applyVersionAvailability(versions, new Map([[1, { available: true }]]));
    expect(next[0]?.source_mismatch).toBeUndefined();
  });

  it("leaves an unprobed row and its identity untouched", () => {
    const versions = [makeVersion({ file_id: 1, container: "virtual" })];
    expect(applyVersionAvailability(versions, new Map())).toBe(versions);
  });
});

describe("mergeVersionLiveness failed verdicts", () => {
  it("carries a failed flag from the check result", () => {
    const versions = [makeVersion({ file_id: 1 })];
    const merged = mergeVersionLiveness(versions, {
      results: [{ file_id: 1, available: false, failed: true }],
    });
    expect(merged.get(1)).toEqual({ available: false, failed: true });
  });

  it("an available check clears a stale failed flag", () => {
    const versions = [makeVersion({ file_id: 1, available: false, failed: true })];
    const merged = mergeVersionLiveness(versions, {
      results: [{ file_id: 1, available: true }],
    });
    expect(merged.get(1)).toEqual({ available: true, failed: undefined });
  });
});

describe("applyVersionAvailability failed verdicts", () => {
  it("stamps failed onto the row", () => {
    const versions = [makeVersion({ file_id: 1 })];
    const next = applyVersionAvailability(
      versions,
      new Map([[1, { available: false, failed: true }]]),
    );
    expect(next[0]?.available).toBe(false);
    expect(next[0]?.failed).toBe(true);
  });

  it("clears failed when the check reports the row available", () => {
    const versions = [makeVersion({ file_id: 1, available: false, failed: true })];
    const next = applyVersionAvailability(versions, new Map([[1, { available: true }]]));
    expect(next[0]?.available).toBe(true);
    expect(next[0]?.failed).toBe(false);
  });

  it("keeps a prior failed flag on an unavailable verdict without one", () => {
    const versions = [makeVersion({ file_id: 1, available: false, failed: true })];
    const next = applyVersionAvailability(versions, new Map([[1, { available: false }]]));
    expect(next[0]?.failed).toBe(true);
  });
});
