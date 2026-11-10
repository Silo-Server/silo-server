import { Shuffle } from "lucide-react";

import { HeroBackdropArtwork } from "@/components/HeroBackdropArtwork";
import {
  CollectionByline,
  CollectionPageActions,
} from "@/components/collections/CollectionPageActions";
import { useAmbientColor } from "@/hooks/useAmbientColor";
import { useT } from "@/i18n";
import type { CollectionPageAccess } from "@/hooks/useCollectionPageAccess";
import type { CollectionPageHeader } from "@/hooks/useCollectionPageHeader";
import { cn } from "@/lib/utils";
import DetailOverview from "@/pages/ItemDetail/components/DetailOverview";

/** The homepage hero's slow pan and dimming, applied to the collection's backdrop. */
const BACKDROP_IMAGE_STYLE = {
  animation: "var(--animate-ken-burns-a)",
  filter:
    "brightness(var(--hero-backdrop-brightness, 0.78)) saturate(var(--hero-backdrop-saturate, 0.95))",
};

interface CollectionPageHeroProps {
  title: string;
  access: CollectionPageAccess;
  header: CollectionPageHeader;
  libraryId?: number;
  /** Omitted when there is nothing to shuffle. */
  shuffle?: { onShuffle: () => void; starting: boolean };
}

/**
 * A collection page's header, in the homepage hero's style: the backdrop runs
 * edge to edge behind the title and the filter bar below it, and fades into
 * the page. Without a backdrop it is the same header over the plain page.
 */
export function CollectionPageHero({
  title,
  access,
  header,
  libraryId,
  shuffle,
}: CollectionPageHeroProps) {
  const t = useT();
  const backdrop =
    header.status === "ready" && (header.backdropUrl || header.backdropThumbhash) ? header : null;
  useAmbientColor(backdrop?.backdropThumbhash);

  return (
    <header
      className={cn(
        "relative flex flex-col gap-5 sm:flex-row sm:items-end sm:justify-between sm:gap-8",
        backdrop && "min-h-[clamp(13rem,30vh,22rem)] justify-end",
      )}
    >
      {backdrop ? (
        // Behind the header and the filter bar under it, out to the edges of
        // the content column. The offsets undo the layout's and the page's
        // top padding (1rem + 1rem, 1rem + 1.5rem from sm, 2rem + 1.5rem from
        // lg) so the art starts at the top of the page, as the homepage
        // hero does; the strong bottom fade melts it into the page under the filters.
        <div
          aria-hidden="true"
          className="pointer-events-none absolute -top-8 right-[calc(var(--page-gutter,0px)*-1)] -bottom-24 left-[calc(var(--page-gutter,0px)*-1)] -z-10 overflow-hidden sm:-top-10 lg:-top-14"
        >
          <HeroBackdropArtwork
            backdropUrl={backdrop.backdropUrl}
            backdropThumbhash={backdrop.backdropThumbhash}
            className="absolute inset-0"
            imageStyle={BACKDROP_IMAGE_STYLE}
          />
          {/* The glow goes under the bottom fade, which ends in the solid page
              color, so the art's bottom edge leaves no seam. */}
          <div className="ambient-glow" />
          <div className="hero-gradient-strong" />
          {/* On a phone the title and description stack up over the middle of
              the art, above where the bottom fade starts, so dim it all. */}
          <div className="bg-background/45 absolute inset-0 sm:hidden" />
          <div className="hero-gradient-left" />
        </div>
      ) : null}

      <div className="max-w-3xl min-w-0">
        <p className="hero-eyebrow mb-3 hidden sm:mb-4 sm:block">
          {/* eslint-disable-next-line formatjs/no-literal-string-in-jsx -- new on main; its catalog key is a follow-up */}
          <span className="hero-eyebrow-strong">Collection</span>
        </p>
        <h1
          className="font-display text-4xl font-extrabold tracking-[-0.04em] text-balance sm:text-5xl lg:text-6xl"
          style={backdrop ? { textShadow: "var(--hero-text-shadow, none)" } : undefined}
        >
          {title}
        </h1>
        <div className="mt-3 space-y-3 empty:hidden">
          <CollectionByline access={access} />
          {header.status === "ready" && header.description ? (
            // A description keeps the line breaks it was written with.
            <div className="whitespace-pre-line">
              <DetailOverview overview={header.description} clamp />
            </div>
          ) : null}
        </div>
      </div>

      {/* Wraps on a narrow phone, where Shuffle, Add to my Home and More actions
          don't fit on one row. */}
      <div className="flex shrink-0 flex-wrap items-center gap-3">
        {shuffle ? (
          <button
            type="button"
            disabled={shuffle.starting}
            onClick={shuffle.onShuffle}
            className="pill pill-primary focus-visible:ring-ring/50 flex-1 outline-none focus-visible:ring-[3px] disabled:pointer-events-none disabled:opacity-60 sm:flex-none [&_svg]:size-4"
          >
            <Shuffle aria-hidden="true" />
            {t("web.catalog.page.shuffle")}
          </button>
        ) : null}
        <CollectionPageActions access={access} libraryId={libraryId} />
      </div>
    </header>
  );
}
