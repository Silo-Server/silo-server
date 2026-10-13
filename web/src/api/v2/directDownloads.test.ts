import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { launchDirectDownload } from "./directDownloads";

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
  it.each(["account", "profile", "pin", "closed"])(
    "refuses a late link after %s authority changes",
    async (kind) => {
      let resolve!: (r: Response) => void;
      const fetch = vi.fn<typeof globalThis.fetch>(
        () =>
          new Promise<Response>((r) => {
            resolve = r;
          }),
      );
      vi.stubGlobal("fetch", fetch);
      let current = true;
      const pending = launchDirectDownload(42, () => current);
      await vi.waitFor(() => expect(resolve).toBeDefined());
      if (kind === "account") setAccessToken("replacement");
      if (kind === "profile") setProfileId("profile-two");
      if (kind === "pin") setProfileToken("pin-two");
      if (kind === "closed") current = false;
      resolve(linkResponse());
      await expect(pending).rejects.toThrow();
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(click).not.toHaveBeenCalled();
    },
  );
  it.each(["account", "profile", "pin", "closed"])(
    "refuses a late probe after %s authority changes",
    async (kind) => {
      let resolve!: (r: Response) => void;
      vi.stubGlobal(
        "fetch",
        vi
          .fn<typeof globalThis.fetch>()
          .mockResolvedValueOnce(linkResponse())
          .mockImplementationOnce(
            () =>
              new Promise<Response>((r) => {
                resolve = r;
              }),
          ),
      );
      let current = true;
      const pending = launchDirectDownload(42, () => current);
      await vi.waitFor(() => expect(resolve).toBeDefined());
      if (kind === "account") setAccessToken("replacement");
      if (kind === "profile") setProfileId("profile-two");
      if (kind === "pin") setProfileToken("pin-two");
      if (kind === "closed") current = false;
      resolve(new Response(null, { status: 200 }));
      await expect(pending).rejects.toThrow();
      expect(click).not.toHaveBeenCalled();
    },
  );
  it("does not probe or navigate when the server refuses the link", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      new Response(
        JSON.stringify({
          type: "https://siloserver.org/problems/not-found",
          title: "Not Found",
          status: 404,
          detail: "File not found.",
        }),
        { status: 404, headers: { "Content-Type": "application/problem+json" } },
      ),
    );
    vi.stubGlobal("fetch", fetch);
    await expect(launchDirectDownload(42, () => true)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
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
