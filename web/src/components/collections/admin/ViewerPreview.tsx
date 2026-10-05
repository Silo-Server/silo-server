import { Fragment } from "react";

import type { LibraryCollection } from "@/api/types";
import { PosterArt } from "@/components/calm/PosterTile";
import {
  VIEWER_PREVIEW_LABEL,
  VIEWER_PREVIEW_MINE,
  VIEWER_PREVIEW_NOTE,
} from "@/lib/collections/copy";
import { pinnedBand, shownCollections, type Shelf } from "@/lib/collections/shelves";
import { cn } from "@/lib/utils";

import { PinGlyph } from "./CollectionListItem";

/**
 * A small Collections-tab card: the collection's poster (its collage unless
 * one was set) and name, with the pin when it leads its shelf.
 */
function MiniCollectionCard({
  collection,
  pinned,
}: {
  collection: LibraryCollection;
  pinned: boolean;
}) {
  return (
    <li className="grid w-[62px] content-start gap-1.5">
      <PosterArt
        posterUrl={collection.poster_url}
        thumbhash={collection.poster_thumbhash}
        className="ring-border/60 aspect-[2/3] w-full rounded-md ring-1"
      />
      <span className="line-clamp-2 text-[11px] leading-tight font-medium">
        {pinned ? (
          <PinGlyph className="text-muted-foreground mr-0.5 size-2.5 align-[-1px]" />
        ) : null}
        {collection.title}
      </span>
    </li>
  );
}

/** Each viewer's own collections: two empty cards, since every viewer sees different ones. */
function MineCards() {
  return (
    <ul className="m-0 flex list-none gap-2 p-0">
      {[0, 1].map((index) => (
        <li key={index} className="grid w-[62px] content-start gap-1.5">
          <span className="border-border bg-muted/30 aspect-[2/3] w-full rounded-md border" />
          <span className="text-muted-foreground text-[11px] leading-tight">
            {index === 0 ? VIEWER_PREVIEW_MINE : ""}
          </span>
        </li>
      ))}
    </ul>
  );
}

/**
 * What viewers see on the library's Collections tab, beside the shelves:
 * each shelf's visible collections in the order it shows them, the pin on
 * those that lead a Your order shelf, a divider before No heading, and
 * nothing that's hidden. Empty shelves don't show.
 */
export function ViewerPreview({
  libraryName,
  shelves,
  isVisible,
  className,
}: {
  libraryName: string;
  shelves: readonly Shelf[];
  isVisible: (collection: LibraryCollection) => boolean;
  className?: string;
}) {
  return (
    <aside
      aria-label={VIEWER_PREVIEW_LABEL}
      className={cn("surface-panel grid content-start gap-4 rounded-[22px] p-4", className)}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <b className="text-[14px] font-semibold">{libraryName} › Collections</b>
        <span className="text-muted-foreground inline-flex items-center gap-1.5 text-[12px]">
          <span aria-hidden className="bg-success size-1.5 rounded-full" />
          {VIEWER_PREVIEW_LABEL}
        </span>
      </div>
      {shelves.map((shelf) => {
        if (shelf.kind === "user_collections")
          return (
            <div key={shelf.id} className="grid gap-2">
              <h3 className="m-0 text-[12.5px] font-semibold">{shelf.name}</h3>
              <MineCards />
            </div>
          );
        const shown = shownCollections(shelf).filter(isVisible);
        if (shown.length === 0) return null;
        const band = new Set(pinnedBand(shelf).map((entry) => entry.id));
        const loose = shelf.kind === "ungrouped";
        return (
          <Fragment key={shelf.id}>
            {loose ? <hr aria-hidden className="border-border m-0" /> : null}
            <div className="grid gap-2">
              <h3
                className={cn(
                  "m-0 text-[12.5px]",
                  loose ? "text-muted-foreground font-normal italic" : "font-semibold",
                )}
              >
                {shelf.name}
              </h3>
              <ul className="m-0 flex list-none flex-wrap gap-2 p-0">
                {shown.map((collection) => (
                  <MiniCollectionCard
                    key={collection.id}
                    collection={collection}
                    pinned={band.has(collection.id)}
                  />
                ))}
              </ul>
            </div>
          </Fragment>
        );
      })}
      <p className="text-muted-foreground m-0 text-[12px] leading-normal">{VIEWER_PREVIEW_NOTE}</p>
    </aside>
  );
}
