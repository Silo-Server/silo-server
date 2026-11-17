// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  activeCatalogQueryMatchesLibrary,
  activeSectionQueryMatchesLibrary,
} from "./queryInvalidation";

describe("activeCatalogQueryMatchesLibrary", () => {
  it("matches unscoped and same-library catalog queries", () => {
    expect(activeCatalogQueryMatchesLibrary(["catalog", "list", { library_id: 3 }], 3)).toBe(true);
    expect(activeCatalogQueryMatchesLibrary(["catalog", "list", {}], 3)).toBe(true);
  });
});

describe("activeSectionQueryMatchesLibrary", () => {
  it("matches same-library and non-library-scoped section queries", () => {
    expect(activeSectionQueryMatchesLibrary(["sections", "library", 3, "layout"], 3)).toBe(true);
    expect(activeSectionQueryMatchesLibrary(["sections", "admin", "home", undefined], 3)).toBe(
      true,
    );
  });
});
