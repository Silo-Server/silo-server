/**
 * Artwork requested at the width each screen draws it.
 *
 * The server picks every image's size when it builds a response: v2 reads that
 * return artwork take `poster_width`, `backdrop_width`, `logo_width`,
 * `still_width` and `photo_width`, the width in device pixels the client draws
 * that field at, and answer the narrowest cached rung at least that wide, else
 * the original (docs/images-api.md, "Per-slot widths"). Each rung's URL carries
 * its own content revision and signature, so the page cannot choose between
 * rungs with `srcset`; it chooses by asking for the width.
 *
 * This module is that choice for the whole app. A fetch names the surface it
 * feeds, and `artworkQuery` turns the surface's drawn CSS widths at the current
 * viewport, poster size, and device pixel ratio into the width parameters.
 * Until `images/capabilities` has listed `slot_width_params` the parameters are
 * left out and the server keeps its defaults: the server rejects unknown query
 * parameters, so an older node must not see them.
 *
 * Widths are read when a request is built and are not part of any query key.
 * A resize keeps cached artwork until its query next refetches. The server
 * binds widths into pagination cursors, so a paged read sends its first
 * page's widths for every page after it.
 */
import type { components } from "@/api/v2/schema";
import { v2, V2ProblemError } from "@/api/v2/request";
import {
  carouselCardWidthPx,
  DEFAULT_CARD_PRESENTATION,
  gridCardWidthPx,
  type PosterSize,
} from "@/lib/uiCustomization";
import { storage } from "@/utils/storage";

type ImageCapabilities = components["schemas"]["ImageCapabilities"];

/** The part of the images capability width selection reads. */
export type ArtworkLadders = Pick<ImageCapabilities, "slot_width_params" | "widths">;

export type ArtworkSlot = "poster" | "backdrop" | "logo" | "still" | "photo";

/** CSS pixel widths a surface draws each slot at. */
export type DrawnWidths = Partial<Record<ArtworkSlot, number>>;

export interface ArtworkWidthQuery {
  poster_width?: number;
  backdrop_width?: number;
  logo_width?: number;
  still_width?: number;
  photo_width?: number;
}

/** The image type whose ladder each slot resolves on, as `widths` keys it. */
const SLOT_LADDER: Record<ArtworkSlot, string> = {
  poster: "poster",
  backdrop: "backdrop",
  logo: "logo",
  still: "still",
  photo: "profile",
};

/**
 * The highest pixel ratio widths scale by. Browser zoom raises
 * devicePixelRatio past any screen's density; nothing past 3x looks sharper.
 */
export const MAX_ARTWORK_DPR = 3;

/**
 * How much narrower than the drawn width a rung may be. The ladder is coarse
 * (a still goes 500 then 1280), so a slot a few pixels past a rung would
 * otherwise download the next one at two to four times the bytes for no
 * visible gain.
 */
export const RUNG_SLACK = 0.05;

/**
 * Past the widest rung the server answers the original, up to 3840px and often
 * several times the top rung's bytes. The widest rung stands in while it
 * covers at least this share of the drawn width, the share the Jellyfin
 * surface accepts for MaxWidth.
 */
export const TOP_RUNG_COVERAGE = 0.8;

/** The widest width any slot parameter accepts. */
const MAX_SLOT_WIDTH = 7680;

/**
 * The width parameters for `drawn` at `devicePixelRatio`: none without
 * capabilities, and only the slots the server lists in `slot_width_params`.
 */
export function artworkWidthQuery(
  drawn: DrawnWidths,
  ladders: ArtworkLadders | null | undefined,
  devicePixelRatio: number,
): ArtworkWidthQuery {
  const params = ladders?.slot_width_params;
  if (!params) return {};
  const ratio = Math.min(Math.max(devicePixelRatio || 1, 1), MAX_ARTWORK_DPR);
  const query: ArtworkWidthQuery = {};
  for (const slot of Object.keys(drawn) as ArtworkSlot[]) {
    const css = drawn[slot];
    if (!css || css <= 0 || !params[slot]) continue;
    const device = css * ratio;
    const top = ladders.widths[SLOT_LADDER[slot]]?.large ?? 0;
    const topRungCovers = top > 0 && device > top && top >= device * TOP_RUNG_COVERAGE;
    const width = topRungCovers ? top : Math.ceil(device * (1 - RUNG_SLACK));
    query[`${slot}_width`] = Math.min(Math.max(width, 1), MAX_SLOT_WIDTH);
  }
  return query;
}

/**
 * What a fetch feeds:
 * - `row`: a carousel of poster cards, and wide (16:9) Continue Watching cards.
 * - `hero`: a featured section's full-bleed backdrop slides.
 * - `homeLayout`: the home layout with its first rows, hero included; see
 *   `rowDrawsBackdrops`.
 * - `grid`: a library or catalog poster grid.
 * - `detail`: an item, season or episode detail page: hero backdrop, title
 *   logo, poster, and cast photos.
 * - `seasons`: a series' season poster carousel.
 * - `episodes`: a season's episode cards.
 * - `upNext`: the player's Up Next still.
 * - `onDeck`: the player's On Deck cards below Up Next.
 */
export type ArtworkSurface =
  | "row"
  | "hero"
  | "homeLayout"
  | "grid"
  | "detail"
  | "seasons"
  | "episodes"
  | "upNext"
  | "onDeck";

export interface ArtworkViewport {
  width: number;
  height: number;
}

/** Tailwind's sm, md, lg, xl and 2xl breakpoints. */
const SM = 640;
const MD = 768;
const LG = 1024;
const XL = 1280;
const XXL = 1536;

/** Layout's `--page-gutter` on each side of the main column. */
function pageGutter(viewportWidth: number): number {
  if (viewportWidth >= XL) return 48;
  if (viewportWidth >= LG) return 40;
  if (viewportWidth >= SM) return 24;
  return 16;
}

/**
 * Width of a slide that `object-cover`s a 16:9 image over `width` x `height`:
 * a hero taller than 16:9 scales the image to its height and crops the sides.
 */
function coverWidth(width: number, height: number): number {
  return Math.max(width, (height * 16) / 9);
}

/**
 * HERO_BANNER_SIZE_TALL (Library Recommended), the taller of the two hero
 * sizes, so one width serves both heroes.
 */
function heroBackdropWidth({ width, height }: ArtworkViewport): number {
  const heroHeight = Math.min(Math.max(height * (width >= LG ? 0.72 : 0.6), 420), 760);
  return coverWidth(width, heroHeight);
}

/** DetailHero: the backdrop covers at least 72dvh on lg, 60dvh below. */
function detailBackdropWidth({ width, height }: ArtworkViewport): number {
  return coverWidth(width, height * (width >= LG ? 0.72 : 0.6));
}

/** SeasonEpisodeGrid's card at this viewport; EpisodeCarousel cards are 240px. */
function episodeCardWidth(viewportWidth: number): number {
  // Detail pages collapse the sidebar to its 64px rail and cap the column at
  // page-shell-wide's 1520px.
  const rail = viewportWidth >= LG ? 64 : 0;
  const column = Math.min(viewportWidth - rail, 1520) - 2 * pageGutter(viewportWidth);
  const columns = viewportWidth >= LG ? 5 : viewportWidth >= MD ? 4 : viewportWidth >= SM ? 3 : 2;
  return Math.max(240, (column - 16 * (columns - 1)) / columns);
}

/** On Deck cards per row from each breakpoint up; two below sm. */
const ON_DECK_COLUMNS: ReadonlyArray<[minWidth: number, columns: number]> = [
  [XXL, 7],
  [XL, 6],
  [LG, 5],
  [MD, 4],
  [SM, 3],
];

/** PlayingNextScreen's On Deck card: a share of the full-width row, less its padding. */
function onDeckCardWidth(viewportWidth: number): number {
  const padding = viewportWidth >= MD ? 32 : viewportWidth >= SM ? 24 : 16;
  const gap = viewportWidth >= SM ? 12 : 8;
  const columns = ON_DECK_COLUMNS.find(([minWidth]) => viewportWidth >= minWidth)?.[1] ?? 2;
  return (viewportWidth - 2 * padding + gap) / columns - gap;
}

/** The CSS width each slot of `surface` is drawn at. */
export function surfaceWidths(
  surface: ArtworkSurface,
  viewport: ArtworkViewport,
  posterSize: PosterSize,
): DrawnWidths {
  const vw = viewport.width;
  switch (surface) {
    case "row":
      return {
        poster: carouselCardWidthPx(posterSize, vw),
        backdrop: carouselCardWidthPx("wide", vw),
      };
    case "hero":
      return { backdrop: heroBackdropWidth(viewport) };
    case "homeLayout":
      return {
        poster: carouselCardWidthPx(posterSize, vw),
        backdrop: heroBackdropWidth(viewport),
      };
    case "grid": {
      // Layout's main column: the 260px sidebar on lg and up, then the
      // gutters, less the A-Z bar a title-sorted library reserves (pr-6,
      // sm:pr-9). Grids without the bar are a few pixels wider than this.
      const column = vw - (vw >= LG ? 260 : 0) - 2 * pageGutter(vw) - (vw >= SM ? 36 : 24);
      return { poster: gridCardWidthPx(posterSize, vw, column) };
    }
    case "detail":
      // DetailHero's portrait poster and title logo box, CastCarousel's card.
      return {
        backdrop: detailBackdropWidth(viewport),
        logo: Math.min(vw >= LG ? 480 : 420, vw - 2 * pageGutter(vw)),
        poster: vw >= SM ? 220 : 170,
        photo: 110,
      };
    case "seasons":
      return { poster: vw >= SM ? 170 : 160 };
    case "episodes":
      return { still: episodeCardWidth(vw) };
    case "upNext":
      // PlayingNextScreen: full width, 16:9, at most min(38vh, 400px) tall.
      return {
        still: Math.min(vw - 2 * pageGutter(vw), (Math.min(viewport.height * 0.38, 400) * 16) / 9),
      };
    case "onDeck": {
      // A 16:9 card that covers itself with the backdrop, else the poster.
      const card = onDeckCardWidth(vw);
      return { backdrop: card, poster: card };
    }
  }
}

/**
 * Whether a home or library row draws its items' backdrops as cards: Continue
 * Watching and Next Up use wide cards. The home layout asks for hero-sized
 * backdrops, so these rows load their own items at card size instead.
 */
export function rowDrawsBackdrops(sectionType: string): boolean {
  return sectionType === "continue_watching" || sectionType === "next_up";
}

const STORAGE_KEY = storage.KEYS.IMAGE_CAPABILITIES;

// undefined until first read from storage; null when the server lists no
// slot widths.
let ladders: ArtworkLadders | null | undefined;
let posterSize: PosterSize = DEFAULT_CARD_PRESENTATION.poster_size;

function readStoredLadders(): ArtworkLadders | null {
  try {
    const value: unknown = JSON.parse(storage.get(STORAGE_KEY) ?? "null");
    if (
      value &&
      typeof value === "object" &&
      "slot_width_params" in value &&
      "widths" in value &&
      typeof value.slot_width_params === "object" &&
      typeof value.widths === "object"
    ) {
      return value as ArtworkLadders;
    }
  } catch {
    // Unreadable: treat as unknown.
  }
  return null;
}

function currentLadders(): ArtworkLadders | null {
  if (ladders === undefined) ladders = readStoredLadders();
  return ladders;
}

/**
 * Records the images capability. It is kept across reloads so the first
 * screen of the next visit is sized without waiting on this read.
 */
export function setImageCapabilities(capabilities: ImageCapabilities | null): void {
  ladders =
    capabilities?.state === "available" &&
    capabilities.allowed &&
    Object.keys(capabilities.slot_width_params ?? {}).length > 0
      ? { slot_width_params: capabilities.slot_width_params, widths: capabilities.widths }
      : null;
  if (ladders) storage.set(STORAGE_KEY, JSON.stringify(ladders));
  else storage.remove(STORAGE_KEY);
}

/**
 * Reads the images capability. A server that answers with a problem (one
 * that predates the document, or a refusal) gets no widths; a network failure
 * keeps the last document.
 */
export async function loadImageCapabilities(signal?: AbortSignal): Promise<void> {
  try {
    setImageCapabilities(await v2("GET /api/v2/images/capabilities", { signal }));
  } catch (error) {
    if (error instanceof V2ProblemError) setImageCapabilities(null);
  }
}

/** The profile's poster size, which sets card widths in rows and grids. */
export function setArtworkPosterSize(size: PosterSize): void {
  posterSize = size;
}

/** The width parameters for a request feeding `surface`. */
export function artworkQuery(surface: ArtworkSurface): ArtworkWidthQuery {
  if (typeof window === "undefined") return {};
  return artworkWidthQuery(
    surfaceWidths(surface, { width: window.innerWidth, height: window.innerHeight }, posterSize),
    currentLadders(),
    window.devicePixelRatio,
  );
}
