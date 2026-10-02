import { afterEach, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({ v2: vi.fn() }));
vi.mock("@/api/v2/request", () => ({ v2: mocks.v2 }));
import { testSubtitleProvider } from "./subtitles";
afterEach(() => mocks.v2.mockReset());
it("sends a draft test once without authentication replay", async () => {
  mocks.v2.mockRejectedValue(new Error("lost response"));
  const config = { api_key: "synthetic-draft" };
  await expect(testSubtitleProvider("subdl", config)).rejects.toThrow("lost response");
  expect(mocks.v2).toHaveBeenCalledTimes(1);
  expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/admin/subtitle-providers/{provider}/test", {
    path: { provider: "subdl" },
    body: config,
    retryAuthentication: false,
  });
});
