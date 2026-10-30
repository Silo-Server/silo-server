import { describe, expect, it } from "vitest";

import recentlyAddedFixture from "../../../../contracts/api/v2/fixtures/list_recently_added_ok.json";
import { catalogItemFromV2 } from "@/api/v2/catalog";
import {
  getOverlayDef,
  overlayDataFromBrowseItem,
  overlayDataFromSectionItem,
} from "@/lib/overlays";

const cardItem = recentlyAddedFixture.items[0] as Parameters<typeof catalogItemFromV2>[0];
const badge = getOverlayDef("unwatched_episodes")!;

describe("unwatched-episodes badge", () => {
  it("names the series on a series card and nothing on other cards", () => {
    const series = catalogItemFromV2({
      ...cardItem,
      content_id: "series:severance",
      type: "series",
    });
    const movie = catalogItemFromV2({ ...cardItem, content_id: "movie:heat-1995", type: "movie" });

    expect(overlayDataFromSectionItem(series).unwatched_episodes_series_id).toBe(
      "series:severance",
    );
    expect(overlayDataFromBrowseItem(series).unwatched_episodes_series_id).toBe("series:severance");
    expect(overlayDataFromSectionItem(movie).unwatched_episodes_series_id).toBeUndefined();
  });

  it("shows the count and hides once nothing is left or the count is unknown", () => {
    expect(badge.getValue({ unwatched_episodes: 10 })).toBe("10");
    expect(badge.getValue({ unwatched_episodes: 1 })).toBe("1");
    expect(badge.getValue({ unwatched_episodes: 0 })).toBeNull();
    expect(badge.getValue({ unwatched_episodes: null })).toBeNull();
    expect(badge.getValue({})).toBeNull();
  });

  it("is off by default and offered only by servers that accept it", () => {
    expect(badge.defaultEnabled).toBe(false);
    expect(badge.introducedInManifest).toBe(25);
  });
});
