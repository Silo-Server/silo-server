import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { launchDirectDownload } from "./directDownloads";

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
  it("probes then navigates with the same captured account URL without buffering", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    await launchDirectDownload(42, () => true);
    expect(fetch).toHaveBeenCalledTimes(1);
    const call = fetch.mock.calls[0];
    if (!call) throw new Error("Expected one HEAD call");
    expect(call[0]).toBe("/api/v2/direct-download?file_id=42&token=original-account-token");
    expect(call[1]).toEqual({ method: "HEAD", cache: "no-store" });
    expect(click).toHaveBeenCalledTimes(1);
    expect((click.mock.instances[0] as HTMLAnchorElement).getAttribute("href")).toBe(call[0]);
  });
  it.each(["account", "profile", "pin", "closed"])(
    "refuses a late probe after %s authority changes",
    async (kind) => {
      let resolve!: (r: Response) => void;
      vi.stubGlobal(
        "fetch",
        vi.fn(
          () =>
            new Promise<Response>((r) => {
              resolve = r;
            }),
        ),
      );
      let current = true;
      const pending = launchDirectDownload(42, () => current);
      if (kind === "account") setAccessToken("replacement");
      if (kind === "profile") setProfileId("profile-two");
      if (kind === "pin") setProfileToken("pin-two");
      if (kind === "closed") current = false;
      resolve(new Response(null, { status: 200 }));
      await expect(pending).rejects.toThrow();
      expect(click).not.toHaveBeenCalled();
    },
  );
  it.each([403, 500])("does not navigate or replay refused/uncertain status %s", async (status) => {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await expect(launchDirectDownload(42, () => true)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(click).not.toHaveBeenCalled();
  });
  it("does not turn a failed probe into a transfer", async () => {
    const fetch = vi.fn().mockRejectedValue(new TypeError("network"));
    vi.stubGlobal("fetch", fetch);
    await expect(launchDirectDownload(42, () => true)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(click).not.toHaveBeenCalled();
  });
});
