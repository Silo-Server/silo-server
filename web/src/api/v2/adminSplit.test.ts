import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { getAdminItemFiles, splitAdminItem } from "./adminSplit";
beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("test-account");
  setProfileId("p-owner");
});
afterEach(() => vi.unstubAllGlobals());
it("walks file pages without losing opaque identifiers", async () => {
  const mock = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(
      jsonResponse({
        items: [{ id: "9007199254740993", library_id: "7" }],
        page: { has_more: true, next_cursor: "next" },
      }),
    )
    .mockResolvedValueOnce(
      jsonResponse({ items: [{ id: "43", library_id: "7" }], page: { has_more: false } }),
    );
  vi.stubGlobal("fetch", mock);
  expect((await getAdminItemFiles("source")).files.map((f) => f.id)).toEqual([
    "9007199254740993",
    "43",
  ]);
  expect(String(mock.mock.calls[1]?.[0])).toContain("cursor=next");
});
it("rejects a repeated file cursor", async () => {
  const mock = vi.fn<typeof fetch>(async () =>
    jsonResponse({ items: [], page: { has_more: true, next_cursor: "repeat" } }),
  );
  vi.stubGlobal("fetch", mock);
  await expect(getAdminItemFiles("source")).rejects.toThrow("Incomplete item file list");
  expect(mock).toHaveBeenCalledTimes(2);
});
it("does not replay split after401", async () => {
  const mock = vi.fn<typeof fetch>(async () => jsonResponse({ message: "Expired" }, 401));
  vi.stubGlobal("fetch", mock);
  await expect(
    splitAdminItem("source", { file_ids: ["42"], target: { content_id: "target" }, dry_run: true }),
  ).rejects.toThrow();
  expect(mock).toHaveBeenCalledTimes(1);
  expect(JSON.parse(String(mock.mock.calls[0]?.[1]?.body)).file_ids).toEqual(["42"]);
});
it("rejects a file walk whose authority changed", async () => {
  const mock = vi.fn<typeof fetch>(async () => {
    setProfileId("p-other");
    return jsonResponse({ items: [], page: { has_more: true, next_cursor: "next" } });
  });
  vi.stubGlobal("fetch", mock);
  await expect(getAdminItemFiles("source")).rejects.toThrow();
  expect(mock).toHaveBeenCalledTimes(1);
});
