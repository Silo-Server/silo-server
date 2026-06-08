import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: mocks.api,
}));

vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ awaitAdminJob: vi.fn() }),
}));

vi.mock("@/pages/ItemDetail/watchedState", () => ({
  getCachedWatchedInvalidationKeys: vi.fn(() => []),
}));

vi.mock("./mediaSurfaceRefresh", () => ({
  invalidateMediaSurfaceQueries: vi.fn(),
}));

vi.mock("@/pages/homeSurfaceRefresh", () => ({
  bumpHomeRefreshSignal: vi.fn(),
}));

import { fetchWatchDetail, redetectEpisodeIntro } from "./items";

describe("item query helpers", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.api.mockResolvedValue({});
  });

  it("encodes item IDs in watch detail endpoints", async () => {
    await fetchWatchDetail("ebook 1/isbn:978", 42, 12);

    expect(mocks.api).toHaveBeenCalledWith(
      "/watch/ebook%201%2Fisbn%3A978?fileId=42&library_id=12",
      undefined,
    );
  });

  it("encodes item IDs in admin item endpoints", async () => {
    await redetectEpisodeIntro("episode 1/id:abc");

    expect(mocks.api).toHaveBeenCalledWith(
      "/admin/items/episode%201%2Fid%3Aabc/redetect-intro",
      {
        method: "POST",
      },
    );
  });
});
