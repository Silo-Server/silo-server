import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import {
  createEbookReaderConfigSession,
  fetchEbookReaderConfig,
  saveEbookReaderConfig,
} from "./ebookConfigApi";

function response(config: object, tag: string) {
  return new Response(JSON.stringify({ content_id: "book", config }), {
    status: 200,
    headers: { "Content-Type": "application/json", ETag: tag },
  });
}
describe("guarded ebook configuration", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("synthetic-config-token");
    setProfileId("reader-one");
  });
  afterEach(() => vi.unstubAllGlobals());
  it("uses each returned validator for the next queued save under the original profile", async () => {
    const session = createEbookReaderConfigSession();
    let release: (() => void) | undefined;
    const first = new Promise<void>((resolve) => {
      release = resolve;
    });
    let writes = 0;
    const fetchMock = vi.fn<typeof fetch>(async (_url, options) => {
      if (options?.method === "GET") return response({}, '"initial"');
      writes++;
      if (writes === 1) await first;
      return response({ theme: writes === 1 ? "dark" : "light" }, `"revision-${writes}"`);
    });
    vi.stubGlobal("fetch", fetchMock);
    await fetchEbookReaderConfig("book", session);
    const saving = saveEbookReaderConfig("book", { theme: "dark" }, session);
    const newer = saveEbookReaderConfig("book", { theme: "light" }, session, true);
    await vi.waitFor(() => expect(writes).toBe(1));
    setProfileId("reader-two");
    release!();
    await Promise.all([saving, newer]);
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get("If-Match")).toBe('"initial"');
    expect(new Headers(fetchMock.mock.calls[2]?.[1]?.headers).get("If-Match")).toBe('"revision-1"');
    expect(new Headers(fetchMock.mock.calls[2]?.[1]?.headers).get("X-Profile-Id")).toBe(
      "reader-one",
    );
    expect(fetchMock.mock.calls[2]?.[1]?.keepalive).toBe(true);
  });
  it("fences queued writes after a conflict instead of silently overwriting", async () => {
    const session = createEbookReaderConfigSession();
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(response({}, '"initial"'))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
            title: "Precondition failed",
            status: 412,
            detail: "changed",
          }),
          { status: 412, headers: { "Content-Type": "application/problem+json" } },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);
    await fetchEbookReaderConfig("book", session);
    await expect(saveEbookReaderConfig("book", { theme: "dark" }, session)).rejects.toBeDefined();
    await expect(saveEbookReaderConfig("book", { theme: "light" }, session)).rejects.toBeDefined();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(session.etag).toBe('"initial"');
  });
  it("never dispatches a queued write after an account change", async () => {
    const session = createEbookReaderConfigSession();
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(response({}, '"initial"'));
    vi.stubGlobal("fetch", fetchMock);
    await fetchEbookReaderConfig("book", session);
    setAccessToken("other-account");
    await expect(saveEbookReaderConfig("book", { theme: "dark" }, session)).rejects.toMatchObject({
      name: "StaleApiRequestContextError",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
