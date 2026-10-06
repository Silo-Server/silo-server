import type { ReactNode } from "react";
import { Bookmark, BookmarkCheck, Library, Loader2, Plus } from "lucide-react";
import type { MediaRequest, RequestMediaResult, RequestMediaType } from "@/api/types";
import { cn } from "@/lib/utils";
import {
  formatMediaType,
  formatSeasonList,
  formatSeasonProgress,
  requestDetailHref,
  requestDisplayState,
  tmdbImageURL,
  type RequestDisplayState,
} from "@/lib/mediaRequests";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";
import { useUICustomization } from "@/hooks/useUICustomization";
import MediaCardArtwork, {
  MEDIA_CARD_CAPTION_CLASS,
  MEDIA_CARD_CENTER_ACTION_CLASS,
  MEDIA_CARD_META_CLASS,
  MEDIA_CARD_TITLE_CLASS,
} from "@/components/MediaCardArtwork";
import { RequestReasonBadge, RequestStatusBadge } from "@/components/RequestStatusBadge";
import { Button } from "@/components/ui/button";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import {
  mediaItemMenuIconClassName,
  mediaItemMenuTriggerClassName,
} from "@/components/mediaItemMenuTrigger";

// The badge takes the top-right corner and truncates before it reaches the
// Library chip on the left.
const BADGE_CLASS = "ml-auto max-w-full shrink-0";
const DETAIL_CLASS = "text-muted-foreground mt-1 truncate text-[0.75rem] font-medium";

type DiscoverProps = {
  variant: "discover";
  item: RequestMediaResult;
  /** Called when the hover Request action is clicked. Omit to suppress the action. */
  onRequest?: () => void;
  /** Shows the pending state on the hover Request action. Ignored when onRequest is omitted. */
  isSubmitting?: boolean;
  /**
   * Called when the hover watchlist action is clicked; it reads
   * item.in_watchlist. Omit to suppress the action.
   */
  onToggleWatchlist?: () => void;
  /** Disables the hover watchlist action while a change is in flight. */
  isWatchlistPending?: boolean;
  /** When true, fills the parent (use inside grids). Default: the viewer's carousel card width. */
  fluid?: boolean;
};

type MineProps = {
  variant: "mine";
  request: MediaRequest;
  fluid?: boolean;
  /** Shows a Cancel request action. Pass it only for the viewer's own cancellable request. */
  onCancel?: () => void;
  /** Disables the Cancel request action while a cancellation is in flight. */
  isCancelling?: boolean;
};

export type RequestPosterCardProps = DiscoverProps | MineProps;

/**
 * A title known from TMDB, drawn on the same poster card as library titles:
 * a TMDB search or discovery result ("discover"), or one of the viewer's
 * requests ("mine").
 */
export default function RequestPosterCard(props: RequestPosterCardProps) {
  if (props.variant === "mine") {
    return (
      <MineCard
        request={props.request}
        fluid={props.fluid}
        onCancel={props.onCancel}
        isCancelling={props.isCancelling}
      />
    );
  }
  return (
    <DiscoverCard
      item={props.item}
      isSubmitting={props.isSubmitting}
      onRequest={props.onRequest}
      onToggleWatchlist={props.onToggleWatchlist}
      isWatchlistPending={props.isWatchlistPending}
      fluid={props.fluid}
    />
  );
}

function DiscoverCard({
  item,
  isSubmitting,
  onRequest,
  onToggleWatchlist,
  isWatchlistPending,
  fluid,
}: Omit<DiscoverProps, "variant">) {
  const requestable = item.request.requestable;
  const availableInLibrary = item.availability === "available" && !item.request.status;
  const state: RequestDisplayState | undefined = item.request.status
    ? requestDisplayState(item.request.status, undefined, item.request.state)
    : availableInLibrary
      ? "available"
      : undefined;

  return (
    <ExternalTitleCard
      title={item.title}
      mediaType={item.media_type}
      year={item.year}
      posterPath={item.poster_path}
      href={requestDetailHref(item.media_type, item.tmdb_id)}
      libraryContentId={item.library_content_id}
      fluid={fluid}
      dim={!requestable}
      badge={
        state ? (
          <RequestStatusBadge state={state} overlay className={BADGE_CLASS} />
        ) : !requestable ? (
          <RequestReasonBadge reason={item.request.reason} overlay className={BADGE_CLASS} />
        ) : null
      }
      action={
        requestable && onRequest ? (
          <RequestAction
            title={`${item.title} (${[formatMediaType(item.media_type), item.year].filter(Boolean).join(" · ")})`}
            pending={Boolean(isSubmitting)}
            onRequest={onRequest}
          />
        ) : null
      }
      cornerAction={
        onToggleWatchlist ? (
          <WatchlistAction
            title={item.title}
            inWatchlist={Boolean(item.in_watchlist)}
            pending={Boolean(isWatchlistPending)}
            onToggle={onToggleWatchlist}
          />
        ) : null
      }
    />
  );
}

function MineCard({ request, fluid, onCancel, isCancelling }: Omit<MineProps, "variant">) {
  const state = requestDisplayState(request.status, request.outcome, request.state);
  const isClosed =
    request.outcome === "failed" ||
    request.outcome === "declined" ||
    request.outcome === "cancelled";
  const seasons = request.seasons?.length ? formatSeasonList(request.seasons) : "";
  const progress =
    state === "partially_available" && request.season_progress?.length
      ? formatSeasonProgress(request.season_progress)
      : "";
  const hasDetails = Boolean(
    seasons || progress || request.last_error || request.outcome_reason || onCancel,
  );

  return (
    <ExternalTitleCard
      title={request.title}
      mediaType={request.media_type}
      year={request.year}
      posterPath={request.poster_path}
      href={requestDetailHref(request.media_type, request.tmdb_id)}
      libraryContentId={request.library_content_id}
      fluid={fluid}
      dim={isClosed}
      badge={state ? <RequestStatusBadge state={state} overlay className={BADGE_CLASS} /> : null}
    >
      {hasDetails ? (
        <>
          {seasons ? <p className={DETAIL_CLASS}>{seasons}</p> : null}
          {progress ? <p className={DETAIL_CLASS}>{progress}</p> : null}
          {request.last_error ? (
            <p
              className="text-destructive mt-1 line-clamp-2 text-[0.75rem] leading-snug font-medium"
              title={request.last_error}
            >
              {request.last_error}
            </p>
          ) : request.outcome_reason ? (
            <p
              className="text-muted-foreground mt-1 line-clamp-2 text-[0.75rem] leading-snug"
              title={request.outcome_reason}
            >
              {request.outcome_reason}
            </p>
          ) : null}
          {onCancel ? (
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={onCancel}
              disabled={isCancelling}
              aria-label={`Cancel request for ${request.title}`}
              className="text-muted-foreground mt-1.5 -ml-2"
            >
              Cancel request
            </Button>
          ) : null}
        </>
      ) : null}
    </ExternalTitleCard>
  );
}

/**
 * The library poster card for a title outside the library: the same artwork
 * frame, hover reveal, and caption, with a request badge where library cards
 * carry their overlay badges and a Request action where they carry Play.
 */
function ExternalTitleCard({
  title,
  mediaType,
  year,
  posterPath,
  href,
  libraryContentId,
  fluid,
  dim,
  badge,
  action,
  cornerAction,
  children,
}: {
  title: string;
  mediaType: RequestMediaType;
  year?: number;
  posterPath?: string;
  href: string;
  /** Adds a Library chip linking to the title's library item. */
  libraryContentId?: string;
  fluid?: boolean;
  dim?: boolean;
  badge?: ReactNode;
  /** The hover action in the card's centre slot. */
  action?: ReactNode;
  /** A hover action in the poster's bottom-right corner, where library cards keep their menu. */
  cornerAction?: ReactNode;
  /** Request details below the caption. Shown whatever the caption setting; pass null for none. */
  children?: ReactNode;
}) {
  const { cardPresentation } = useUICustomization();
  const showCaption = cardPresentation.caption !== "artwork";
  // Unlike a library card, a TMDB title always names its type and year under
  // a caption: a movie and a series can share a title, and the viewer is
  // choosing which one to request.
  const showMetadata = showCaption;
  const meta = [formatMediaType(mediaType), year].filter(Boolean).join(" · ");
  const label = `${title} (${meta})`;

  return (
    <div
      className={cn(
        "media-card group/card",
        fluid ? "w-full" : carouselCardWidthClasses(cardPresentation.poster_size),
      )}
    >
      <div className="group/media relative">
        <ViewTransitionLink
          to={href}
          aria-label={label}
          className="block overflow-hidden rounded-xl"
        >
          <MediaCardArtwork
            src={tmdbImageURL(posterPath)}
            alt={title}
            fallbackLabel={title}
            lazy
            dim={dim}
            fallbackOnError
          />
        </ViewTransitionLink>
        {libraryContentId || badge ? (
          <div className="pointer-events-none absolute inset-x-2 top-2 z-10 flex flex-wrap items-center gap-1.5">
            {libraryContentId ? <LibraryChip contentID={libraryContentId} title={title} /> : null}
            {badge}
          </div>
        ) : null}
        {action}
        {cornerAction ? (
          <div className="absolute right-1.5 bottom-1.5 z-20 sm:right-2.5 sm:bottom-2.5">
            {cornerAction}
          </div>
        ) : null}
      </div>
      {showCaption || children ? (
        <div className={MEDIA_CARD_CAPTION_CLASS}>
          {showCaption ? (
            <ViewTransitionLink to={href} className={MEDIA_CARD_TITLE_CLASS}>
              {title}
            </ViewTransitionLink>
          ) : null}
          {showMetadata ? (
            <ViewTransitionLink to={href} className={MEDIA_CARD_META_CLASS}>
              {meta}
            </ViewTransitionLink>
          ) : null}
          {children}
        </div>
      ) : null}
    </div>
  );
}

/** The centred hover Request action on a TMDB title's poster card. */
export function RequestAction({
  title,
  pending,
  onRequest,
}: {
  title: string;
  pending: boolean;
  onRequest: () => void;
}) {
  return (
    <button
      type="button"
      disabled={pending}
      aria-label={pending ? `Sending request for ${title}` : `Request ${title}`}
      onClick={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onRequest();
      }}
      className={cn(
        MEDIA_CARD_CENTER_ACTION_CLASS,
        "h-9 gap-1.5 px-3.5 text-[0.75rem] font-semibold whitespace-nowrap hover:scale-105",
        // Keep the pending state in view after the pointer leaves the card.
        pending && "pointer-events-auto opacity-100",
      )}
    >
      {pending ? (
        <>
          <Loader2 className="size-3.5 animate-spin" aria-hidden />
          Sending
        </>
      ) : (
        <>
          <Plus className="size-3.5 stroke-[2.5]" aria-hidden />
          Request
        </>
      )}
    </button>
  );
}

/** The hover watchlist toggle in a TMDB title's poster corner. */
export function WatchlistAction({
  title,
  inWatchlist,
  pending,
  onToggle,
}: {
  title: string;
  inWatchlist: boolean;
  pending: boolean;
  onToggle: () => void;
}) {
  const Icon = inWatchlist ? BookmarkCheck : Bookmark;
  return (
    <button
      type="button"
      disabled={pending}
      aria-pressed={inWatchlist}
      aria-label={
        inWatchlist ? `Remove ${title} from your watchlist` : `Add ${title} to your watchlist`
      }
      title={inWatchlist ? "On Watchlist" : "Add to Watchlist"}
      onClick={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onToggle();
      }}
      className={cn(
        mediaItemMenuTriggerClassName("poster"),
        inWatchlist && "text-primary",
        // Keep the pending state in view after the pointer leaves the card.
        pending && "pointer-events-auto opacity-100",
      )}
    >
      <Icon
        className={cn(mediaItemMenuIconClassName("poster"), inWatchlist && "fill-primary/20")}
        aria-hidden
      />
    </button>
  );
}

function LibraryChip({ contentID, title }: { contentID: string; title: string }) {
  return (
    <ViewTransitionLink
      to={`/item/${encodeURIComponent(contentID)}`}
      aria-label={`Open ${title} in library`}
      className="glass-chip text-foreground focus-visible:ring-ring pointer-events-auto inline-flex shrink-0 items-center gap-1 rounded-full border border-white/15 px-2.5 py-1 text-[0.625rem] leading-none font-semibold tracking-[0.14em] uppercase transition-colors hover:border-white/40 focus-visible:ring-2 focus-visible:outline-none"
    >
      <Library className="size-3 shrink-0" strokeWidth={2.4} aria-hidden />
      Library
    </ViewTransitionLink>
  );
}
