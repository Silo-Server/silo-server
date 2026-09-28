import { describe, expect, it } from "vitest";

import itemFixture from "../../../../contracts/api/v2/fixtures/get_catalog_item_ok.json";
import { catalogItemDetailFromV2 } from "@/api/v2/catalog";

// catalogItemDetailFromV2 builds ItemDetail field by field, so a field the
// server sends but the mapper does not copy is dropped silently: types compile,
// the API returns the value, and the UI renders nothing. That is exactly how
// the advisory badge failed its first end-to-end run, so it is pinned here.
describe("catalogItemDetailFromV2 advisory fields", () => {
  it("carries the advisory age and its source through the mapper", () => {
    const detail = catalogItemDetailFromV2({
      ...itemFixture,
      advisory_age: 10,
      advisory_source: "commonsense",
    } as Parameters<typeof catalogItemDetailFromV2>[0]);

    expect(detail.advisory_age).toBe(10);
    expect(detail.advisory_source).toBe("commonsense");
  });

  it("normalizes an absent advisory rather than leaving it undefined", () => {
    const detail = catalogItemDetailFromV2(
      itemFixture as Parameters<typeof catalogItemDetailFromV2>[0],
    );

    expect(detail.advisory_age).toBeNull();
    expect(detail.advisory_source).toBe("");
  });

  it("leaves the certification untouched", () => {
    const detail = catalogItemDetailFromV2({
      ...itemFixture,
      content_rating: "PG",
      advisory_age: 10,
      advisory_source: "commonsense",
    } as Parameters<typeof catalogItemDetailFromV2>[0]);

    expect(detail.content_rating).toBe("PG");
  });
});
