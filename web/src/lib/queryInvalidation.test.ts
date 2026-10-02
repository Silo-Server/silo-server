import { describe, expect, it } from "vitest";
import {
  activeCatalogQueryMatchesLibrary,
  activeSectionQueryMatchesLibrary,
} from "./queryInvalidation";

describe("activeCatalogQueryMatchesLibrary", () => {
  it("does not match a catalog list query for a different library event", () => {
    expect(activeCatalogQueryMatchesLibrary(["catalog", "list", { library_id: 1 }], 3)).toBe(false);
  });

  it("matches unscoped and same-library catalog queries", () => {
    expect(activeCatalogQueryMatchesLibrary(["catalog", "list", { library_id: 3 }], 3)).toBe(true);
    expect(activeCatalogQueryMatchesLibrary(["catalog", "list", {}], 3)).toBe(true);
  });
});

describe("activeSectionQueryMatchesLibrary", () => {
  it("does not match a library section query for a different library event", () => {
    expect(activeSectionQueryMatchesLibrary(["sections", "library", 1, "items"], 3)).toBe(false);
  });

  it("matches same-library and non-library-scoped section queries", () => {
    expect(activeSectionQueryMatchesLibrary(["sections", "library", 3, "layout"], 3)).toBe(true);
    expect(activeSectionQueryMatchesLibrary(["sections", "admin", "home", undefined], 3)).toBe(
      true,
    );
  });

  it("does not match home section queries for a library-scoped event", () => {
    expect(activeSectionQueryMatchesLibrary(["sections", "home", "layout"], 3)).toBe(false);
    expect(activeSectionQueryMatchesLibrary(["sections", "home", "items", "recent"], 3)).toBe(
      false,
    );
  });

  it("matches home section queries when the event is not library-scoped", () => {
    expect(activeSectionQueryMatchesLibrary(["sections", "home", "items", "recent"])).toBe(true);
  });
});
