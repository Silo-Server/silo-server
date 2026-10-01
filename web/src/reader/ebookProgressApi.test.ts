import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import {
  captureEbookProgressIntent,
  fetchEbookReaderProgress,
  saveEbookReaderProgress,
} from "./ebookProgressApi";

describe("ordered ebook progress", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("synthetic-reader-token");
    setProfileId("reader-one");
    setProfileToken("synthetic-pin");
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("reads the v2 envelope and adapts the file identifier", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () =>
      jsonResponse({
        progress: {
          file_id: "42",
          location: "here",
          progress: 0.2,
          content_id: "book",
          updated_at: "2026-01-01T00:00:00.000Z",
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchEbookReaderProgress("book/one")).toMatchObject({
      file_id: 42,
      location: "here",
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/ebooks/book%2Fone/progress");
    fetchMock.mockResolvedValueOnce(jsonResponse({}));
    expect(await fetchEbookReaderProgress("book")).toBeNull();
  });

  it("preserves the event time and original profile through delayed send and retry", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:00:00.000Z"));
    const intent = captureEbookProgressIntent({ file_id: 42, location: "here", progress: 0.2 })!;
    vi.setSystemTime(new Date("2026-01-01T01:00:00.000Z"));
    setProfileId("reader-two");
    setProfileToken("different-pin");
    const fetchMock = vi.fn<typeof fetch>(async () =>
      jsonResponse({ progress: { ...intent.body, content_id: "book" } }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await saveEbookReaderProgress("book", intent);
    await saveEbookReaderProgress("book", intent, true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    for (const [, options] of fetchMock.mock.calls) {
      expect(JSON.parse(String(options?.body))).toEqual({
        file_id: "42",
        location: "here",
        progress: 0.2,
        updated_at: "2026-01-01T00:00:00.000Z",
      });
      expect(new Headers(options?.headers).get("X-Profile-Id")).toBe("reader-one");
      expect(new Headers(options?.headers).get("X-Profile-Token")).toBe("synthetic-pin");
    }
    expect(fetchMock.mock.calls[1]?.[1]?.keepalive).toBe(true);
  });

  it("refuses queued progress after account authority changes", async () => {
    const intent = captureEbookProgressIntent({ file_id: 42, location: "here", progress: 0.2 })!;
    setAccessToken("another-account-token");
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    await expect(saveEbookReaderProgress("book", intent)).rejects.toMatchObject({
      name: "StaleApiRequestContextError",
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("does not round an unrepresentable file identifier", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        jsonResponse({
          progress: {
            file_id: "9007199254740993",
            location: "here",
            progress: 0.2,
            content_id: "book",
            updated_at: "2026-01-01T00:00:00.000Z",
          },
        }),
      ),
    );
    await expect(fetchEbookReaderProgress("book")).rejects.toThrow("file identifier");
  });
});
