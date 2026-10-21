import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { launchDirectDownload } from "./directDownloads";
import { pendingResponse } from "./pendingFetch.test-support";

const LINK_URL = "/api/v2/direct-download?file_id=42&dl=synthetic-link";

function linkResponse(): Response {
  return new Response(
    JSON.stringify({
      url: LINK_URL,
      proxy_url: "/api/v2/direct-download-proxy?file_id=42&dl=synthetic-link",
      expires_at: "2026-01-01T00:05:00Z",
    }),
    { headers: { "Content-Type": "application/json" } },
  );
}

let click: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  setAccessToken("original-account-token");
  setProfileId("profile-one");
  setProfileToken("pin-one");
  click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  setAccessToken(null);
  setProfileId(null);
  setProfileToken(null);
});

describe("direct download navigation", () => {
  it("mints a profile-bound link, probes it, then navigates to it", async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(linkResponse())
      .mockResolvedValueOnce(new Response(null, { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    await launchDirectDownload(42, () => true);
    expect(fetch).toHaveBeenCalledTimes(2);

    const [mintUrl, mintInit] = fetch.mock.calls[0]!;
    expect(String(mintUrl)).toContain("/api/v2/direct-download/links");
    expect(mintInit?.method).toBe("POST");
    expect(JSON.parse(String(mintInit?.body))).toEqual({ file_id: "42" });
    const headers = new Headers(mintInit?.headers);
    expect(headers.get("X-Profile-Id")).toBe("profile-one");
    expect(headers.get("X-Profile-Token")).toBe("pin-one");

    expect(fetch.mock.calls[1]).toEqual([LINK_URL, { method: "HEAD", cache: "no-store" }]);
    expect(click).toHaveBeenCalledTimes(1);
    const href = (click.mock.instances[0] as HTMLAnchorElement).getAttribute("href");
    expect(href).toBe(LINK_URL);
    expect(href).not.toContain("original-account-token");
  });
  it("refuses to start without a selected profile", async () => {
    setProfileId(null);
    const fetch = vi.fn<typeof globalThis.fetch>();
    vi.stubGlobal("fetch", fetch);
    await expect(launchDirectDownload(42, () => true)).rejects.toMatchObject({
      name: "StaleApiRequestContextError",
    });
    expect(fetch).not.toHaveBeenCalled();
  });
  // The link check runs after the mint and the probe check after the HEAD
  // probe; each stage refuses every kind of authority change.
  it.each(
    (["link", "probe"] as const).flatMap((stage) =>
      (["account", "profile", "pin", "closed"] as const).map((kind) => [stage, kind] as const),
    ),
  )("refuses a late %s after %s authority changes", async (stage, kind) => {
    const late = pendingResponse();
    const fetch = vi.fn<typeof globalThis.fetch>();
    if (stage === "link") fetch.mockReturnValue(late.promise);
    else fetch.mockResolvedValueOnce(linkResponse()).mockReturnValueOnce(late.promise);
    vi.stubGlobal("fetch", fetch);
    let current = true;
    const pending = launchDirectDownload(42, () => current);
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(stage === "link" ? 1 : 2));
    if (kind === "account") setAccessToken("replacement");
    if (kind === "profile") setProfileId("profile-two");
    if (kind === "pin") setProfileToken("pin-two");
    if (kind === "closed") current = false;
    late.finish(stage === "link" ? linkResponse() : new Response(null, { status: 200 }));
    await expect(pending).rejects.toThrow();
    if (stage === "link") expect(fetch).toHaveBeenCalledTimes(1);
    expect(click).not.toHaveBeenCalled();
  });
  it.each([403, 500])("does not navigate or replay refused/uncertain status %s", async (status) => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(linkResponse())
      .mockResolvedValueOnce(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await expect(launchDirectDownload(42, () => true)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(click).not.toHaveBeenCalled();
  });
  it("does not turn a failed probe into a transfer", async () => {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(linkResponse())
      .mockRejectedValueOnce(new TypeError("network"));
    vi.stubGlobal("fetch", fetch);
    await expect(launchDirectDownload(42, () => true)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(click).not.toHaveBeenCalled();
  });
});
