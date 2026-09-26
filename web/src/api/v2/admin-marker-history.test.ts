import { afterEach, beforeEach, expect, it, vi } from "vitest";
import fixture from "../../../../contracts/api/v2/fixtures/admin_marker_history_all.json";
import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { getAllMarkerHistory, getItemMarkerHistory } from "./markers";
beforeEach(() => {
  installPolicyStorageMocks();
  setProfileId("p-owner");
});
afterEach(() => vi.unstubAllGlobals());
it("preserves opaque audit identifiers and adapts absent snapshots", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(fixture));
  vi.stubGlobal("fetch", fetchMock);
  const rows = await getAllMarkerHistory(50);
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/admin/markers/history?limit=50");
  expect(rows[0]?.id).toBe("9007199254740993");
  expect(rows[0]?.api_key_id).toBe("9007199254740995");
  expect(rows[0]?.before?.start).toBe(1.5);
  expect(rows[0]?.after).toBeNull();
});
it("reads item history with encoded identity and forwards cancellation", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse({ history: [] }));
  vi.stubGlobal("fetch", fetchMock);
  const controller = new AbortController();
  expect(await getItemMarkerHistory("item/a", 25, controller.signal)).toEqual([]);
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
    "/api/v2/admin/markers/items/item%2Fa/history?limit=25",
  );
  expect(fetchMock.mock.calls[0]?.[1]?.signal).toBe(controller.signal);
});
