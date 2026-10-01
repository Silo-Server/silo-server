import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { getItemMarkers, setItemMarkers } from "./markers";
import { markerUpdateToV2 } from "@/player/marker-wire";

const markers = {
  file_id: "42",
  intro: { start_seconds: 0, end_seconds: 60, source: "manual", confidence: 0 },
  credits: {},
  recap: {},
  preview: {},
  marker_segments: [
    { kind: "intro", start_seconds: 0, end_seconds: 60 },
    { kind: "intro", start_seconds: 120, end_seconds: 150 },
  ],
};

describe("v2 marker consumers", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("profile-1");
  });
  afterEach(() => vi.unstubAllGlobals());

  it("adapts seconds and unknown provenance without losing zero values", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(markers));
    vi.stubGlobal("fetch", fetchMock);
    const result = await getItemMarkers("item/one");
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/markers/items/item%2Fone");
    expect(result.file_id).toBe(42);
    expect(result.intro).toEqual({
      start: 0,
      end: 60,
      source: "manual",
      confidence: 0,
      provider: null,
      algorithm: null,
      detected_at: null,
    });
    expect(result.credits.start).toBeNull();
    expect(result).toHaveProperty("marker_segments", markers.marker_segments);
  });

  it("sends only changed segments and preserves explicit clears", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(markers));
    vi.stubGlobal("fetch", fetchMock);
    const result = await setItemMarkers("one", { intro: { start: 0, end: 60 }, credits: null });
    expect(result).toHaveProperty("marker_segments", markers.marker_segments);
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("PUT");
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      intro: { start_seconds: 0, end_seconds: 60 },
      credits: null,
    });
    expect(markerUpdateToV2({ recap: {} })).toEqual({ recap: {} });
  });

  it("forwards read cancellation to the transport", async () => {
    const controller = new AbortController();
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(
        (_url, options) =>
          new Promise((_resolve, reject) => {
            options?.signal?.addEventListener(
              "abort",
              () => reject(new DOMException("Aborted", "AbortError")),
              { once: true },
            );
          }),
      ),
    );
    const pending = getItemMarkers("one", controller.signal);
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });
});
