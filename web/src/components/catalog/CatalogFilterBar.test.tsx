import { describe, expect, it } from "vitest";

import { CATALOG_MEDIA_SCOPE_OPTIONS } from "./CatalogFilterBar";

describe("CatalogFilterBar", () => {
  it("offers ebook media scope in the selector", () => {
    expect(CATALOG_MEDIA_SCOPE_OPTIONS).toContainEqual({ value: "ebook", label: "Ebooks" });
  });
});
