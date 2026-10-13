import { beforeEach, describe, expect, it, vi } from "vitest";
import { v2 } from "@/api/v2/request";
import { listWatchlistTitlesV2 } from "./watchlistTitles";

vi.mock("@/api/v2/request", () => ({ v2: vi.fn() }));

const page = (ids: number[], nextCursor?: string) => ({
  items: ids.map((id) => ({ media_type: "movie", tmdb_id: id })),
  page: { has_more: Boolean(nextCursor), next_cursor: nextCursor },
});

describe("listWatchlistTitlesV2", () => {
  beforeEach(() => vi.mocked(v2).mockReset());

  it("reads every page, however many there are", async () => {
    const pages = 30;
    for (let i = 0; i < pages; i++) {
      vi.mocked(v2).mockResolvedValueOnce(
        page([i], i < pages - 1 ? `c${i + 1}` : undefined) as never,
      );
    }
    const titles = await listWatchlistTitlesV2();
    expect(titles).toHaveLength(pages);
    expect(vi.mocked(v2)).toHaveBeenCalledTimes(pages);
  });

  it("fails instead of looping on a repeated cursor", async () => {
    vi.mocked(v2)
      .mockResolvedValueOnce(page([1], "same") as never)
      .mockResolvedValueOnce(page([2], "same") as never);
    await expect(listWatchlistTitlesV2()).rejects.toThrow(/repeated a page cursor/);
  });
});
