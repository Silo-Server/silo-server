import ViewTransitionLink from "@/components/ViewTransitionLink";
import MediaCardArtwork, {
  MEDIA_CARD_CAPTION_CLASS,
  MEDIA_CARD_META_CLASS,
  MEDIA_CARD_TITLE_CLASS,
} from "@/components/MediaCardArtwork";
import type { BoxSetSummary } from "@/api/types";
import { useUICustomization } from "@/hooks/useUICustomization";
import { boxSetFilmCount, boxSetYears } from "@/lib/boxSets";
import { buildBoxSetHref } from "@/lib/mediaNavigation";

/**
 * The card of a movie box set: one poster for the movies TMDB lists in one
 * collection. The stacked edge above the poster and the BOX SET pill set it
 * apart from a single movie and from curated collections.
 */
export default function BoxSetCard({
  boxSet,
  posterUrl,
  posterThumbhash,
}: {
  boxSet: BoxSetSummary;
  /** The card's poster; the box set's own, or a member's when it has none. */
  posterUrl?: string;
  posterThumbhash?: string;
}) {
  const { cardPresentation } = useUICustomization();
  const showCaption = cardPresentation.caption !== "artwork";
  const showMetadata = cardPresentation.caption === "title_metadata";
  const href = buildBoxSetHref({ id: boxSet.id, libraryId: boxSet.library_id });
  const progress = boxSet.movie_count > 0 ? boxSet.played_count / boxSet.movie_count : 0;
  const meta =
    boxSet.played_count > 0
      ? `${boxSet.played_count} / ${boxSet.movie_count} watched`
      : boxSetYears(boxSet) || boxSetFilmCount(boxSet.movie_count);

  return (
    <div className="media-card group/card" data-testid="box-set-card">
      <div className="relative pt-1.5">
        {/* Two sleeves peeking above the poster read as a stack of films. */}
        <span
          aria-hidden
          className="bg-surface-raised border-border absolute inset-x-[10%] top-0 h-6 rounded-t-xl border"
        />
        <span
          aria-hidden
          className="bg-surface-hover border-border absolute inset-x-[5%] top-[3px] h-6 rounded-t-xl border"
        />
        <ViewTransitionLink
          to={href}
          aria-label={`${boxSet.title}, box set of ${boxSetFilmCount(boxSet.movie_count)}`}
          className="relative block overflow-hidden rounded-xl"
        >
          <MediaCardArtwork
            src={posterUrl ?? boxSet.poster_url}
            alt={boxSet.title}
            mediaType="movie"
            thumbhash={posterThumbhash}
            scrim="background"
          >
            <span className="glass-subtle text-foreground absolute top-2.5 left-2.5 rounded-full border border-white/15 px-2.5 py-1 text-[0.625rem] font-semibold tracking-[0.14em] uppercase">
              Box set
            </span>
            <span className="bg-background/60 text-foreground absolute right-2 bottom-2.5 rounded-md px-2 py-0.5 text-[0.6875rem] font-bold backdrop-blur-sm">
              {boxSetFilmCount(boxSet.movie_count)}
            </span>
            {progress > 0 ? (
              <div
                role="progressbar"
                aria-label="Films watched"
                aria-valuemin={0}
                aria-valuemax={boxSet.movie_count}
                aria-valuenow={boxSet.played_count}
                className="absolute inset-x-2.5 bottom-1 h-[3px] overflow-hidden rounded-full bg-black/40"
              >
                <div
                  className="bg-primary h-full rounded-full"
                  style={{ width: `${Math.round(Math.min(1, progress) * 100)}%` }}
                />
              </div>
            ) : null}
          </MediaCardArtwork>
        </ViewTransitionLink>
      </div>
      {showCaption ? (
        <div className={MEDIA_CARD_CAPTION_CLASS}>
          <ViewTransitionLink to={href} className={MEDIA_CARD_TITLE_CLASS}>
            {boxSet.title}
          </ViewTransitionLink>
          {showMetadata ? (
            <ViewTransitionLink to={href} className={MEDIA_CARD_META_CLASS}>
              {meta}
            </ViewTransitionLink>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
