import { Lock } from "lucide-react";

import {
  DISCOVER_LOCKED,
  DISCOVER_STILL_EDITABLE,
  TRAKT_LOCKED,
  TRAKT_STILL_EDITABLE,
} from "@/lib/collections/copy";
import { SYNCED_SOURCE_LABEL } from "@/lib/collections/types";

const DISCOVER_ORDER: Record<string, string> = {
  "popularity.desc": "most popular first",
  "vote_average.desc": "highest rated first",
  "vote_count.desc": "most voted first",
  "revenue.desc": "highest grossing first",
  "primary_release_date.desc": "newest first",
  "release_date.desc": "newest first",
  "first_air_date.desc": "newest first",
};

const MEDIA: Record<string, string> = { movie: "Movies", tv: "TV shows" };

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

/** "Movies, most popular first": what a Discover list finds. */
function discoverFinds(config: Record<string, unknown>): string {
  const discover = (config.discover ?? {}) as Record<string, unknown>;
  const media = MEDIA[text(config.media_type)] ?? "Titles";
  const order = DISCOVER_ORDER[text(discover.sort_by)];
  return order ? `${media}, ${order}` : media;
}

/** "Recommended movies", or the Trakt list's link. */
function traktFinds(config: Record<string, unknown>, sourceUrl: string): string {
  const link = text(config.list_url) || text(config.url);
  if (config.mode === "trakt_list") return link || sourceUrl;
  const preset = text(config.preset) || "trending";
  const media = config.media_type === "tv" ? "TV shows" : "movies";
  return `${preset.charAt(0).toUpperCase()}${preset.slice(1)} ${media}`;
}

/**
 * The list a Discover (starter pack) or legacy Trakt list follows, read-only:
 * the editor can't rebuild either source, so a save never sends it.
 */
export function LockedSourceSummary({
  source,
  config,
  sourceUrl,
}: {
  source: "tmdb_discover" | "trakt";
  config: Record<string, unknown>;
  sourceUrl: string;
}) {
  const discover = source === "tmdb_discover";
  return (
    <div className="bg-muted/40 grid gap-3 rounded-xl px-4 py-3.5 text-[13.5px]">
      <p className="flex items-center gap-2 font-semibold">
        <Lock aria-hidden className="size-4 shrink-0" />
        {discover ? DISCOVER_LOCKED : TRAKT_LOCKED}
      </p>
      <dl className="grid grid-cols-[72px_minmax(0,1fr)] gap-x-3 gap-y-1.5">
        <dt className="text-muted-foreground">Source</dt>
        <dd>{SYNCED_SOURCE_LABEL[source]}</dd>
        <dt className="text-muted-foreground">Finds</dt>
        <dd className="break-words">
          {discover ? discoverFinds(config) : traktFinds(config, sourceUrl)}
        </dd>
      </dl>
      <p className="text-muted-foreground text-[13px]">
        {discover ? DISCOVER_STILL_EDITABLE : TRAKT_STILL_EDITABLE}
      </p>
    </div>
  );
}
