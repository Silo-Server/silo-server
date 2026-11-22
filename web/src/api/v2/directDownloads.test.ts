import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { DirectDownloadError, isStaleFileError, prepareDirectDownload } from "./directDownloads";

const LINK_URL = "/api/v2/direct-download?file_id=42&dl=link-token";
const LINK_PATH = "/api/v2/direct-download/links";

let createObjectURL: ReturnType<typeof vi.spyOn>;

/** The minted link the server hands back for the preflight. */
function mintResponse(): Response {
  return new Response(
    JSON.stringify({
      url: LINK_URL,
      proxy_url: "/api/v2/direct-download-proxy?file_id=42&dl=link-token",
      expires_at: "2026-01-01T00:30:00Z",
    }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}

/**
 * Stubs fetch for the two-step preparation: the link mint answers first, and
 * every other request is the caller's probe.
 */
function stubFetch(probe: (init?: RequestInit) => Response | Promise<Response>) {
  const fetch = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    String(input).includes(LINK_PATH) ? Promise.resolve(mintResponse()) : probe(init),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

beforeEach(() => {
  setAccessToken("original-account-token");
  setProfileId("profile-one");
  setProfileToken("pin-one");
  // The transfer is the browser's own navigation; preparation must never mint
  // an object URL because there is nothing here to revoke.
  createObjectURL = vi
    .spyOn(URL, "createObjectURL")
    .mockImplementation(() => "blob:never") as ReturnType<typeof vi.spyOn>;
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  setAccessToken(null);
  setProfileId(null);
  setProfileToken(null);
});

describe("direct download preflight", () => {
  it("mints a link, ranged-probes it, and returns the save link", async () => {
    const fetch = stubFetch(
      () =>
        new Response(null, {
          status: 206,
          headers: { "Content-Disposition": `attachment; filename="Movie.mp4"` },
        }),
    );

    const download = await prepareDirectDownload(42, () => true, new AbortController().signal);

    // Two requests: the profile-bound link, then a ranged preflight. The
    // transfer is the link's own navigation, not an awaited fetch.
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls[0]?.[0]).toBe(LINK_PATH);
    expect(fetch.mock.calls[1]?.[0]).toBe(LINK_URL);
    expect(fetch.mock.calls[1]?.[1]).toMatchObject({
      method: "GET",
      cache: "no-store",
      headers: { Range: "bytes=0-0" },
    });
    expect(download.url).toBe(LINK_URL);
    expect(download.filename).toBe("Movie.mp4");
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it("falls back to the caller filename when the server sends no disposition", async () => {
    stubFetch(() => new Response(null, { status: 200 }));

    const download = await prepareDirectDownload(
      42,
      () => true,
      new AbortController().signal,
      "Episode.mkv",
    );
    expect(download.filename).toBe("Episode.mkv");
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it.each(["account", "profile", "pin", "closed"])(
    "refuses a late probe after %s authority changes",
    async (kind) => {
      let resolve!: (r: Response) => void;
      stubFetch(
        () =>
          new Promise<Response>((r) => {
            resolve = r;
          }),
      );
      let current = true;
      const pending = prepareDirectDownload(42, () => current, new AbortController().signal);
      await vi.waitFor(() => expect(resolve).toBeTypeOf("function"));
      if (kind === "account") setAccessToken("replacement");
      if (kind === "profile") setProfileId("profile-two");
      if (kind === "pin") setProfileToken("pin-two");
      if (kind === "closed") current = false;
      resolve(new Response(null, { status: 200 }));
      await expect(pending).rejects.toThrow();
      expect(createObjectURL).not.toHaveBeenCalled();
    },
  );

  it("aborts an in-flight probe when its controller fires", async () => {
    stubFetch(
      (init) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    const controller = new AbortController();
    const pending = prepareDirectDownload(42, () => true, controller.signal);
    controller.abort();
    await expect(pending).rejects.toThrow();
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it("never fetches when the signal is already aborted", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const controller = new AbortController();
    controller.abort();
    await expect(prepareDirectDownload(42, () => true, controller.signal)).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it("reports a refused probe with a concrete, status-specific reason", async () => {
    stubFetch(() => new Response(null, { status: 403 }));
    const pending = prepareDirectDownload(42, () => true, new AbortController().signal);
    await expect(pending).rejects.toBeInstanceOf(DirectDownloadError);
    await expect(pending).rejects.toMatchObject({
      status: 403,
      message: "Downloads are not allowed for this account.",
    });
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it("does not attempt a transfer after a failed probe", async () => {
    const fetch = stubFetch(() => Promise.reject(new TypeError("network")));
    await expect(
      prepareDirectDownload(42, () => true, new AbortController().signal),
    ).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it("carries the problem code and a refresh prompt for a stale file_id", async () => {
    stubFetch(
      () =>
        new Response(
          JSON.stringify({
            type: "https://siloserver.org/docs/api/v2/problems/file_unavailable",
            title: "File unavailable",
            status: 404,
            detail: "This file is no longer available",
          }),
          { status: 404, headers: { "Content-Type": "application/problem+json" } },
        ),
    );
    const pending = prepareDirectDownload(42, () => true, new AbortController().signal);
    await expect(pending).rejects.toMatchObject({
      status: 404,
      code: "file_unavailable",
      message: "This version is no longer available. Refresh the list to see current versions.",
    });
  });

  it("keeps the denial text for a file-level access refusal", async () => {
    stubFetch(
      () =>
        new Response(
          JSON.stringify({
            type: "https://siloserver.org/docs/api/v2/problems/file_access_denied",
            title: "File access denied",
            status: 403,
          }),
          { status: 403, headers: { "Content-Type": "application/problem+json" } },
        ),
    );
    await expect(
      prepareDirectDownload(42, () => true, new AbortController().signal),
    ).rejects.toMatchObject({
      status: 403,
      code: "file_access_denied",
      message: "You do not have access to this file.",
    });
  });

  it("classifies a stale refusal as needing a version refresh and a denial as not", () => {
    expect(isStaleFileError(new DirectDownloadError(404, "x", "file_unavailable"))).toBe(true);
    // A bare 404 on the direct URL has the same corrective action.
    expect(isStaleFileError(new DirectDownloadError(404, "x"))).toBe(true);
    expect(isStaleFileError(new DirectDownloadError(403, "x", "file_access_denied"))).toBe(false);
    expect(isStaleFileError(new Error("network"))).toBe(false);
  });
});
