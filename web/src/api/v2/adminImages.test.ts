// @vitest-environment jsdom
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { setAccessToken, setRefreshToken, setProfileId } from "@/api/client";
import { getAdminItemImages, applyAdminItemImage } from "./adminImages";
beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("test-account");
  setRefreshToken("test-refresh");
  setProfileId("p-owner");
});
afterEach(() => vi.unstubAllGlobals());
const choice = {
  provider_id: "p",
  url: "display",
  original_url: "source",
  type: "poster",
  language: "en",
  width: 100,
  height: 100,
  rating: 1,
};
it("collects every image page and retains current selection", async () => {
  const mock = vi
    .fn()
    .mockResolvedValueOnce(
      jsonResponse({
        items: [choice],
        page: { has_more: true, next_cursor: "next" },
        current: { poster_url: "current" },
      }),
    )
    .mockResolvedValueOnce(
      jsonResponse({
        items: [{ ...choice, original_url: "second" }],
        page: { has_more: false },
        current: { poster_url: "current" },
      }),
    );
  vi.stubGlobal("fetch", mock);
  const out = await getAdminItemImages("item-1");
  expect(out.images).toHaveLength(2);
  expect(out.current.poster_url).toBe("current");
  expect(String(mock.mock.calls[1]?.[0])).toContain("cursor=next");
});
it("refuses repeated continuation without returning a partial list", async () => {
  const mock = vi.fn().mockImplementation(() =>
    Promise.resolve(
      jsonResponse({
        items: [choice],
        page: { has_more: true, next_cursor: "same" },
        current: {},
      }),
    ),
  );
  vi.stubGlobal("fetch", mock);
  await expect(getAdminItemImages("item-1")).rejects.toThrow("Image choices changed");
  expect(mock).toHaveBeenCalledTimes(2);
});
it("refuses image results when authority changes during decoding", async () => {
  const response = jsonResponse({ items: [choice], page: { has_more: false }, current: {} });
  const text = response.text.bind(response);
  vi.spyOn(response, "text").mockImplementation(async () => {
    const body = await text();
    setProfileId("p-other");
    return body;
  });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  await expect(getAdminItemImages("item-1")).rejects.toThrow();
});
it("does not refresh or replay an image publication after401", async () => {
  const mock = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
        status: 401,
        title: "Unauthorized",
      }),
      { status: 401, headers: { "Content-Type": "application/problem+json" } },
    ),
  );
  vi.stubGlobal("fetch", mock);
  await expect(
    applyAdminItemImage("item-1", { original_url: "source", type: "poster", provider_id: "p" }),
  ).rejects.toThrow();
  expect(mock).toHaveBeenCalledTimes(1);
});
