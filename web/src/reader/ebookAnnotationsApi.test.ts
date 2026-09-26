import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import {
  createEbookAnnotationSession,
  createEbookReaderAnnotation,
  deleteEbookReaderAnnotation,
  fetchEbookReaderAnnotations,
} from "./ebookAnnotationsApi";

const row = {
  id: "one",
  content_id: "book",
  kind: "note" as const,
  location: "here",
  note: "saved",
  selected_text: "",
  style: "",
  color: "",
  metadata: {},
  created_at: "2026-01-01T00:00:00.000Z",
  updated_at: "2026-01-01T00:00:00.000Z",
  etag: '"revision-one"',
};
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
describe("v2 reader annotations", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("synthetic-annotation-token");
    setProfileId("reader-one");
  });
  afterEach(() => vi.unstubAllGlobals());
  it("drains bounded pages under one authority and preserves each validator", async () => {
    const session = createEbookAnnotationSession();
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        response({ items: [row], page: { has_more: true, next_cursor: "opaque" } }),
      )
      .mockResolvedValueOnce(
        response({ items: [{ ...row, id: "two", etag: '"two"' }], page: { has_more: false } }),
      );
    vi.stubGlobal("fetch", fetchMock);
    const rows = await fetchEbookReaderAnnotations("book", session);
    expect(rows.map((row) => row.id)).toEqual(["one", "two"]);
    expect(rows[1]?.etag).toBe('"two"');
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("limit=50");
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain("cursor=opaque");
    expect(new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get("X-Profile-Id")).toBe(
      "reader-one",
    );
  });
  it("retains the same create intent after an uncertain response", async () => {
    const session = createEbookAnnotationSession();
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockRejectedValueOnce(new TypeError("connection lost"))
      .mockResolvedValueOnce(response(row));
    vi.stubGlobal("fetch", fetchMock);
    const input = { kind: "note" as const, location: "here", note: "saved" };
    await expect(createEbookReaderAnnotation("book", input, session)).rejects.toThrow();
    await createEbookReaderAnnotation("book", input, session);
    const first = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body));
    const second = JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body));
    expect(first.id).toBeTruthy();
    expect(second).toEqual(first);
    expect(session.creates.size).toBe(0);
  });
  it("retains stale mutation validators and refuses a switched profile", async () => {
    const session = createEbookAnnotationSession();
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      response(
        {
          status: 412,
          title: "Changed",
          type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
        },
        412,
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    await expect(deleteEbookReaderAnnotation("book", row, session)).rejects.toBeDefined();
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("If-Match")).toBe(row.etag);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    setProfileId("reader-two");
    await expect(deleteEbookReaderAnnotation("book", row, session)).rejects.toMatchObject({
      name: "StaleApiRequestContextError",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
  it("does not publish delayed annotation data after a profile switch", async () => {
    const session = createEbookAnnotationSession();
    let complete: ((value: Response) => void) | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockReturnValue(
        new Promise((resolve) => {
          complete = resolve;
        }),
      ),
    );
    const loading = fetchEbookReaderAnnotations("book", session);
    await vi.waitFor(() => expect(complete).toBeDefined());
    setProfileId("reader-two");
    complete!(response({ items: [row], page: { has_more: false } }));
    await expect(loading).rejects.toMatchObject({ name: "StaleApiRequestContextError" });
  });
  it("rejects a repeated cursor instead of looping indefinitely", async () => {
    const session = createEbookAnnotationSession();
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(async () =>
        response({ items: [row], page: { has_more: true, next_cursor: "same" } }),
      );
    vi.stubGlobal("fetch", fetchMock);
    await expect(fetchEbookReaderAnnotations("book", session)).rejects.toThrow("did not advance");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
