import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  API_BLOB_MAX_BYTES,
  captureProfileRequestContext,
  setAccessToken,
  setProfileId,
} from "@/api/client";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import { readEbookBlob } from "./ebookFile";

describe("v2 ebook byte transport", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("synthetic-file-token");
    setProfileId("reader");
  });
  afterEach(() => vi.unstubAllGlobals());
  it("loads binary bytes with encoded identity and captured profile", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response("epub-content", {
        headers: { "Content-Type": "application/epub+zip", "Content-Length": "12" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const blob = await readEbookBlob("book/one", 42, captureProfileRequestContext());
    expect(blob.type).toBe("application/epub+zip");
    expect(await blob.text()).toBe("epub-content");
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/ebooks/book%2Fone/files/42/read");
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("X-Profile-Id")).toBe("reader");
  });
  it("decodes problem errors instead of passing them to the document parser", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(
          JSON.stringify({
            type: "https://siloserver.org/docs/api/v2/problems/not_found",
            title: "Not found",
            status: 404,
          }),
          { status: 404, headers: { "Content-Type": "application/problem+json" } },
        ),
      ),
    );
    await expect(readEbookBlob("book", 42, captureProfileRequestContext())).rejects.toMatchObject({
      name: "V2ProblemError",
    });
  });
  it("rejects an oversized advertised blob before buffering", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response("small fixture", {
          headers: {
            "Content-Type": "application/epub+zip",
            "Content-Length": String(API_BLOB_MAX_BYTES + 1),
          },
        }),
      ),
    );
    await expect(readEbookBlob("book", 42, captureProfileRequestContext())).rejects.toThrow(
      "too large",
    );
  });
  it("rejects stale account authority before sending", async () => {
    const captured = captureProfileRequestContext();
    setAccessToken("other-account");
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    await expect(readEbookBlob("book", 42, captured)).rejects.toMatchObject({
      name: "StaleApiRequestContextError",
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
