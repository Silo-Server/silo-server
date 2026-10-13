import { describe, expect, it } from "vitest";

import { buildUserCollectionCatalogHref } from "./catalogSearchParams";

describe("Collections helpers", () => {
  it("builds the catalog route for viewing a user collection", () => {
    expect(buildUserCollectionCatalogHref("col-3", "Shared Picks")).toBe(
      "/catalog?source=user_collection&collection_id=col-3&title=Shared+Picks",
    );
  });
});
