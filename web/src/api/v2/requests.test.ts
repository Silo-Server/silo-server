import { afterEach, describe, expect, it, vi } from "vitest";
import listMyRequestsOk from "../../../../contracts/api/v2/fixtures/list_my_requests_ok.json";
import { v2 } from "./request";
import { listMyMediaRequestsV2 } from "./requests";
import type { components } from "./schema";

vi.mock("./request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./request")>()),
  v2: vi.fn(),
}));

type Schemas = components["schemas"];

afterEach(() => vi.resetAllMocks());

// The adapters rebuild each body, so a field they leave out never reaches a
// page. Download progress must survive every one of them.
describe("request v2 adapters keep download progress", () => {
  const download: Schemas["RequestDownload"] = {
    phase: "downloading",
    percent: 43,
    bytes_total: 4294967296,
    bytes_left: 2448131358,
    estimated_completion_at: "2026-01-02T03:16:05.678Z",
    downloads: 1,
    updated_at: "2026-01-02T03:04:05.678Z",
  };

  it("on a request and on each of its servers", async () => {
    vi.mocked(v2).mockResolvedValue(listMyRequestsOk as never);

    const [waiting, downloading] = await listMyMediaRequestsV2();

    expect(downloading!.download).toEqual(download);
    expect(downloading!.targets![0]!.download).toEqual(download);
    expect(waiting!.download).toBeUndefined();
    expect(waiting!.targets![0]).not.toHaveProperty("download");
  });
});
