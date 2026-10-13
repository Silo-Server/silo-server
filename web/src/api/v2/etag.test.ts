import { beforeEach, describe, expect, it, vi } from "vitest";

import { requiredETag, withETag } from "./etag";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));

function answer(body: unknown, etag?: string) {
  request.mockImplementationOnce(async (_operation, options) => {
    options.onResponse(new Response(null, { headers: etag ? { ETag: etag } : {} }));
    return body;
  });
}

beforeEach(() => request.mockReset());

describe("requiredETag", () => {
  it("returns a usable validator unchanged", () => {
    expect(requiredETag('W/"7"')).toBe('W/"7"');
  });
  it.each([undefined, null, "", "*"])("refuses %j", (etag) => {
    expect(() => requiredETag(etag)).toThrow("version is unavailable");
  });
});

describe("withETag", () => {
  it("returns the body with the validator the response carried", async () => {
    answer({ ordered_ids: ["a"] }, '"order-3"');
    await expect(withETag("GET /api/v2/collections/order")).resolves.toEqual({
      body: { ordered_ids: ["a"] },
      etag: '"order-3"',
    });
    expect(request).toHaveBeenCalledWith(
      "GET /api/v2/collections/order",
      expect.objectContaining({ onResponse: expect.any(Function) }),
    );
  });

  it("throws when the response has no ETag", async () => {
    answer({ id: "c1" });
    await expect(withETag("GET /api/v2/collections/{id}", { path: { id: "c1" } })).rejects.toThrow(
      "version is unavailable",
    );
  });

  it("passes the request options through and still calls the caller's onResponse", async () => {
    const seen = vi.fn();
    answer({ ordered_ids: [] }, '"x"');
    await withETag("GET /api/v2/admin/collection-groups/{group_id}/collections/order", {
      path: { group_id: "g1" },
      query: { library_id: "4" },
      onResponse: seen,
    });
    expect(request).toHaveBeenCalledWith(
      "GET /api/v2/admin/collection-groups/{group_id}/collections/order",
      expect.objectContaining({ path: { group_id: "g1" }, query: { library_id: "4" } }),
    );
    expect(seen).toHaveBeenCalledOnce();
  });
});
