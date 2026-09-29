import { describe, expect, it } from "vitest";

import itemFixture from "../../../../contracts/api/v2/fixtures/get_catalog_item_ok.json";
import recentlyAddedFixture from "../../../../contracts/api/v2/fixtures/list_recently_added_ok.json";
import { catalogItemDetailFromV2, catalogItemFromV2 } from "@/api/v2/catalog";
import { getOverlayDef, overlayDataFromSectionItem } from "@/lib/overlays";

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

// Cards go through a second field-by-field mapper, catalogItemFromV2, and then
// the overlay extractor. Dropping the advisory at either step leaves the card
// badge blank with no type error, so the whole path is pinned here.
describe("advisory age card overlay", () => {
  const cardItem = recentlyAddedFixture.items[0] as Parameters<typeof catalogItemFromV2>[0];
  const advisoryBadge = getOverlayDef("advisory_age")!;

  it("renders the advisory age a v2 card carries", () => {
    const card = catalogItemFromV2({
      ...cardItem,
      advisory_age: 13,
      advisory_source: "commonsense",
    });

    expect(advisoryBadge.getValue(overlayDataFromSectionItem(card))).toBe("13+");
  });

  it("shows no badge for a card without an advisory", () => {
    const card = catalogItemFromV2(cardItem);

    expect(advisoryBadge.getValue(overlayDataFromSectionItem(card))).toBeNull();
  });
});
