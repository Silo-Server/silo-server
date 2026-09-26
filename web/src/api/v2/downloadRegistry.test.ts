import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import { deleteDownloadEntry, fetchDownloadCapability } from "./downloadRegistry";

describe("download registry browser boundary", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("synthetic-download-token");
    setProfileId("reader-one");
  });
  afterEach(() => vi.unstubAllGlobals());
  it("uses the v2 capability and retains proxy delivery", async () => {
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        new Response(
          JSON.stringify({ enabled: true, proxy_delivery: true, ordered_status: true }),
          { headers: { "Content-Type": "application/json" } },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchDownloadCapability()).toMatchObject({
      proxy_delivery: true,
      ordered_status: true,
    });
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/capabilities/downloads");
  });
  it("retains the browser device and captured profile authority on deletion", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    await deleteDownloadEntry("entry");
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/downloads/entry");
    const headers = new Headers(fetchMock.mock.calls[0]?.[1]?.headers);
    expect(headers.get("X-Profile-Id")).toBe("reader-one");
    expect(headers.get("X-Silo-Device-Id")).toBeTruthy();
  });
  it("does not publish a delayed capability after profile switch", async () => {
    let complete: ((response: Response) => void) | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockReturnValue(
        new Promise((resolve) => {
          complete = resolve;
        }),
      ),
    );
    const pending = fetchDownloadCapability();
    await vi.waitFor(() => expect(complete).toBeDefined());
    setProfileId("reader-two");
    complete!(
      new Response(JSON.stringify({ enabled: true }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    await expect(pending).rejects.toMatchObject({ name: "StaleApiRequestContextError" });
  });
});
