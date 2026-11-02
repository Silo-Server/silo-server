import { beforeEach, describe, expect, it, vi } from "vitest";

import syncProgressOk from "../../../../contracts/api/v2/fixtures/sync_progress_ok.json";

const mocks = vi.hoisted(() => ({
  useQuery: vi.fn(),
  useQueries: vi.fn(),
  useMutation: vi.fn(),
  useQueryClient: vi.fn(),
  fetchCatalogItemDetail: vi.fn(),
  v2: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (...args: unknown[]) => mocks.useQuery(...args),
  useQueries: (...args: unknown[]) => mocks.useQueries(...args),
  useMutation: (...args: unknown[]) => mocks.useMutation(...args),
  useQueryClient: () => mocks.useQueryClient(),
}));

vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: (...args: unknown[]) => mocks.v2(...args) };
});

vi.mock("./catalogRead", () => ({
  fetchCatalogItemDetail: (...args: unknown[]) => mocks.fetchCatalogItemDetail(...args),
}));

import { useReportMediaProgress } from "./progress";

describe("useReportMediaProgress", () => {
  type ReportOptions = {
    mutationFn: (vars: {
      contentId: string;
      positionSeconds: number;
      durationSeconds: number;
      forceOverwrite?: boolean;
    }) => Promise<unknown>;
    onSuccess?: (data: unknown, vars: { contentId: string }) => void;
  };

  beforeEach(() => {
    mocks.useMutation.mockReset();
    mocks.useMutation.mockImplementation((options: unknown) => options);
    mocks.useQueryClient.mockReset();
    mocks.useQueryClient.mockReturnValue({ invalidateQueries: vi.fn() });
    mocks.v2.mockReset();
    mocks.v2.mockResolvedValue(syncProgressOk);
  });

  it("sends one syncProgress item with millisecond positions and returns the results summary", async () => {
    const options = useReportMediaProgress() as unknown as ReportOptions;

    const result = await options.mutationFn({
      contentId: "movie-8f2c1a",
      positionSeconds: 1325.5,
      durationSeconds: 10200,
    });

    expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/sync/progress", {
      body: {
        items: [
          {
            media_item_id: "movie-8f2c1a",
            position_ms: 1325500,
            duration_ms: 10200000,
            force_overwrite: true,
          },
        ],
      },
    });
    expect(result).toEqual(syncProgressOk);
    expect(syncProgressOk.items[0]?.status).toBe("success");
  });

  it("rejects an item failure inside a successful HTTP batch", async () => {
    mocks.v2.mockResolvedValue({
      items: [
        {
          index: 0,
          media_item_id: "hidden",
          status: "failure",
          failure: {
            type: "https://siloserver.org/docs/api/v2/problems/not_found",
            title: "Not found",
            status: 404,
            detail: "Catalog item not found.",
          },
        },
      ],
      summary: { total: 1, succeeded: 0, failed: 1 },
    });
    const options = useReportMediaProgress() as unknown as ReportOptions;
    await expect(
      options.mutationFn({ contentId: "hidden", positionSeconds: 1, durationSeconds: 10 }),
    ).rejects.toThrow("Catalog item not found.");
  });
});
