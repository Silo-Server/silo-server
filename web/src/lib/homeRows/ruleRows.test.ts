import { describe, expect, it } from "vitest";
import type { RecipeCatalogResponse } from "@/lib/recipes";
import { ruleRowKinds } from "./ruleRows";

const catalog = {
  categories: {
    custom: [{ type: "custom_filter", admin_only: true, presets: [] }],
    editorial: [
      { type: "admin_curated_list", admin_only: true, presets: [] },
      { type: "hidden_gems", admin_only: false, presets: [] },
    ],
  },
} as unknown as RecipeCatalogResponse;

describe("rule row kinds", () => {
  it("counts the catalog's admin-only kinds and the filter kinds, as the server's gate does", () => {
    const kinds = ruleRowKinds(catalog);
    expect(kinds.has("custom_filter")).toBe(true);
    expect(kinds.has("admin_curated_list")).toBe(true);
    // genre is hidden from the catalog but is still a filter kind.
    expect(kinds.has("genre")).toBe(true);
    expect(kinds.has("hidden_gems")).toBe(false);
  });

  it("still knows the filter kinds before the catalog loads", () => {
    const kinds = ruleRowKinds(undefined);
    expect(kinds.has("custom_filter")).toBe(true);
    expect(kinds.has("trending_on_server")).toBe(false);
  });
});
