import { Lock } from "lucide-react";

import { DISCOVER_LOCKED, DISCOVER_STILL_EDITABLE } from "@/lib/collections/copy";
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

/**
 * The list a Discover (starter pack) list follows, read-only: the editor
 * can't rebuild its source, so a save never sends it.
 */
export function LockedSourceSummary({ config }: { config: Record<string, unknown> }) {
  return (
    <div className="bg-muted/40 grid gap-3 rounded-xl px-4 py-3.5 text-[13.5px]">
      <p className="flex items-center gap-2 font-semibold">
        <Lock aria-hidden className="size-4 shrink-0" />
        {DISCOVER_LOCKED}
      </p>
      <dl className="grid grid-cols-[72px_minmax(0,1fr)] gap-x-3 gap-y-1.5">
        <dt className="text-muted-foreground">Source</dt>
        <dd>{SYNCED_SOURCE_LABEL.tmdb_discover}</dd>
        <dt className="text-muted-foreground">Finds</dt>
        <dd className="break-words">{discoverFinds(config)}</dd>
      </dl>
      <p className="text-muted-foreground text-[13px]">{DISCOVER_STILL_EDITABLE}</p>
    </div>
  );
}
