import { describe, it, expect, vi, beforeEach } from "vitest";
import listSectionRecipeCandidatesOk from "../../../contracts/api/v2/fixtures/list_section_recipe_candidates_ok.json";
import listSectionRecipesOk from "../../../contracts/api/v2/fixtures/list_section_recipes_ok.json";
import { fetchRecipeCatalog, fetchCandidates, previewSection } from "./recipes";

beforeEach(() => {
  vi.spyOn(globalThis, "fetch").mockReset();
});

describe("recipes API client", () => {
  it("fetchRecipeCatalog returns categories", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify(listSectionRecipesOk), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    const res = await fetchRecipeCatalog();
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe("/api/v2/sections/recipes");
    expect(res.categories.library_staples?.[0]!.type).toBe("recently_added");
    expect(res.categories.mood?.[0]!.presets[0]!.default_params).toEqual({
      genres: ["comedy"],
      mood: "cozy",
    });
  });

  it("fetchCandidates returns candidate list", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify(listSectionRecipeCandidatesOk), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    const candidates = await fetchCandidates("custom_filter");
    expect(vi.mocked(globalThis.fetch).mock.calls[0]?.[0]).toBe(
      "/api/v2/sections/recipes/custom_filter/candidates",
    );
    expect(candidates[0]!.value).toBe("action");
    expect(candidates[0]!.subtitle).toBe("12 titles");
    expect(candidates[1]!.subtitle).toBeUndefined();
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
