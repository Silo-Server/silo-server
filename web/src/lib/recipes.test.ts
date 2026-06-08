import { describe, it, expect, vi, beforeEach } from "vitest";
import { fetchRecipeCatalog, fetchCandidates, previewSection } from "./recipes";

beforeEach(() => {
  vi.spyOn(globalThis, "fetch").mockReset();
});

describe("recipes API client", () => {
  it("fetchRecipeCatalog returns categories", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      status: 200,
      text: async () =>
        JSON.stringify({ categories: { library_staples: [{ type: "recently_added" }] } }),
    } as Response);

    const res = await fetchRecipeCatalog();
    expect(res.categories.library_staples?.[0]!.type).toBe("recently_added");
  });

  it("fetchCandidates returns candidate list", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      status: 200,
      text: async () =>
        JSON.stringify({
          candidates: [{ value: "action", display_name: "Action" }],
        }),
    } as Response);

    const candidates = await fetchCandidates("genre");
    expect(candidates[0]!.value).toBe("action");
  });

  it("previewSection POSTs body and returns items", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ items: [{ content_id: "x" }], total_count: 1 }),
    } as Response);

    const res = await previewSection({
      section_type: "recently_added",
      config: {},
      item_limit: 10,
    });
    expect(res.total_count).toBe(1);
  });
});
