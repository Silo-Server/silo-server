// @vitest-environment node

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * Core browse and detail screens must follow the browser text-size setting
 * (#1796). That needs a root font size relative to the browser default and
 * rem-based text: a `text-[Npx]` label stays the same size when the rest of
 * the page grows.
 */
const SOURCE_ROOT = fileURLToPath(new URL(".", import.meta.url));

const CORE_TEXT_FILES = [
  "components/AppSidebar.tsx",
  "components/CastCarousel.tsx",
  "components/ContinueWatchingCard.tsx",
  "components/CrewList.tsx",
  "components/EpisodeRow.tsx",
  "components/GlobalSearch.tsx",
  "components/ItemCard.tsx",
  "components/MediaCardArtwork.tsx",
  "components/MediaCarousel.tsx",
  "components/MediaLocations.tsx",
  "components/RequestPosterCard.tsx",
  "components/RequestToAddSection.tsx",
  "components/SeasonAccordion.tsx",
  "components/SectionItemCard.tsx",
  "components/catalog/CatalogFilterBar.tsx",
  "components/collections/CollectionPosterCard.tsx",
  "components/ratings/RatingEntry.tsx",
  "components/ui/kbd.tsx",
  "components/watchlist/WatchlistTabs.tsx",
  "components/watchlist/WatchlistTitleCard.tsx",
  "lib/overlays/presets.ts",
  "pages/ItemDetail/DetailHero.tsx",
  "pages/ItemDetail/ExternalTitleContent.tsx",
  "pages/ItemDetail/SeasonCarousel.tsx",
  "pages/ItemDetail/components/ActionBar.tsx",
  "pages/ItemDetail/components/AudioTracksPopover.tsx",
  "pages/ItemDetail/components/DetailBreadcrumb.tsx",
  "pages/ItemDetail/components/DetailOverview.tsx",
  "pages/ItemDetail/components/EpisodeCarousel.tsx",
  "pages/ItemDetail/components/ExtrasSection.tsx",
  "pages/ItemDetail/components/HeroCrewLine.tsx",
  "pages/ItemDetail/components/QualityBadges.tsx",
  "pages/ItemDetail/components/SubtitlesPopover.tsx",
  "pages/ItemDetail/components/TrailersSection.tsx",
  "pages/ItemDetail/components/VersionDropdown.tsx",
];

describe("browser text-size scaling", () => {
  it("sizes the root font relative to the browser default", () => {
    const css = readFileSync(join(SOURCE_ROOT, "app.css"), "utf8");
    const rootSizes = [
      ...css.matchAll(/^\s*html(?:\[[^\]]*\])?\s*\{[^}]*?font-size:\s*([^;]+);/gm),
    ];
    expect(rootSizes.map(([, size]) => size)).toEqual(["100%", "112.5%", "125%"]);
  });

  it("keeps core browse and detail text in rem", () => {
    const offenders: string[] = [];
    for (const file of CORE_TEXT_FILES) {
      const source = readFileSync(join(SOURCE_ROOT, file), "utf8");
      for (const [match] of source.matchAll(/\btext-\[[0-9.]+px\]/g)) {
        offenders.push(`${file}: ${match}`);
      }
    }
    expect(offenders).toEqual([]);
  });
});
