// @vitest-environment jsdom
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { setAccessToken, setRefreshToken, setProfileId } from "@/api/client";
import { getAdminItemImages, uploadAdminItemImage } from "./adminImages";
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
it("uploads the image and its slot as a multipart form", async () => {
  const mock = vi.fn().mockResolvedValue(
    jsonResponse({
      content_id: "item-1",
      stored_path: "local/movies/item-1/poster",
      thumbhash: "",
    }),
  );
  vi.stubGlobal("fetch", mock);
  const file = new File(["png"], "poster.png", { type: "image/png" });
  const out = await uploadAdminItemImage("item-1", "backdrop", file);
  expect(out.stored_path).toBe("local/movies/item-1/poster");
  const [url, init] = mock.mock.calls[0] as [string, RequestInit];
  expect(String(url)).toContain("/api/v2/admin/items/item-1/images/upload");
  expect(init.method).toBe("POST");
  expect(init.body).toBeInstanceOf(FormData);
  const form = init.body as FormData;
  expect(form.get("type")).toBe("backdrop");
  expect(form.get("image")).toBeInstanceOf(Blob);
  // The browser sets the multipart boundary itself.
  expect((init.headers as Record<string, string>)["Content-Type"]).toBeUndefined();
});
