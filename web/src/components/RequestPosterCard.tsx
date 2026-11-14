import type { ReactNode } from "react";
import { Bookmark, BookmarkCheck, Loader2, Plus } from "lucide-react";
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
import { useT } from "@/i18n";

// The badge takes the top-right corner and truncates when the card is narrow.
const BADGE_CLASS = "ms-auto max-w-full shrink-0";
const DETAIL_CLASS = "text-muted-foreground mt-1 truncate text-[0.75rem] font-medium";

type DiscoverProps = {
  variant: "discover";
  item: RequestMediaResult;
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
      onToggleWatchlist={props.onToggleWatchlist}
      isWatchlistPending={props.isWatchlistPending}
      fluid={props.fluid}
    />
  );
}

function DiscoverCard({
  item,
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
      fluid={fluid}
      dim={!requestable}
      badge={
        state ? (
          <RequestStatusBadge state={state} overlay className={BADGE_CLASS} />
        ) : !requestable ? (
          <RequestReasonBadge reason={item.request.reason} overlay className={BADGE_CLASS} />
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
  const t = useT();
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
              aria-label={t("web.requests.cancel.actionFor", { title: request.title })}
              className="text-muted-foreground -ms-2 mt-1.5"
            >
              {t("web.requests.cancel.action")}
            </Button>
          ) : null}
        </>
      ) : null}
    </ExternalTitleCard>
  );
}

/**
 * The library poster card for a title outside the library: the same artwork
 * frame and caption, with a request badge where library cards carry their
 * overlay badges. Requesting happens on the title's detail page.
 */
function ExternalTitleCard({
  title,
  mediaType,
  year,
  posterPath,
  href,
  fluid,
  dim,
  badge,
  cornerAction,
  children,
}: {
  title: string;
  mediaType: RequestMediaType;
  year?: number;
  posterPath?: string;
  href: string;
  fluid?: boolean;
  dim?: boolean;
  badge?: ReactNode;
  /** A hover action in the poster's bottom-right corner, where library cards keep their menu. */
  cornerAction?: ReactNode;
  /** Request details below the caption; pass null for none. */
  children?: ReactNode;
}) {
  const t = useT();
  const { cardPresentation } = useUICustomization();
  const meta = [formatMediaType(mediaType), year].filter(Boolean).join(" · ");
  const label = t("web.requests.card.label", { title, meta });

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
            mediaType={mediaType}
            lazy
            dim={dim}
          />
        </ViewTransitionLink>
        {badge ? (
          <div className="pointer-events-none absolute inset-x-2 top-2 z-10 flex items-center">
            {badge}
          </div>
        ) : null}
        {cornerAction ? (
          // eslint-disable-next-line silo/no-physical-direction -- on the poster artwork, which doesn't mirror; matches CardOverlays and CardWatchStatus
          <div className="absolute right-1.5 bottom-1.5 z-20 sm:right-2.5 sm:bottom-2.5">
            {cornerAction}
          </div>
        ) : null}
      </div>
      <div className={MEDIA_CARD_CAPTION_CLASS}>
        <ViewTransitionLink to={href} className={MEDIA_CARD_TITLE_CLASS}>
          {title}
        </ViewTransitionLink>
        <ViewTransitionLink to={href} className={MEDIA_CARD_META_CLASS}>
          {meta}
        </ViewTransitionLink>
        {children}
      </div>
    </div>
  );
}

/** The centred hover Request action on a watchlist title's poster card. */
export function RequestAction({
  title,
  pending,
  onRequest,
}: {
  title: string;
  pending: boolean;
  onRequest: () => void;
}) {
  const t = useT();
  return (
    <button
      type="button"
      disabled={pending}
      aria-label={
        pending
          ? t("web.requests.card.sendingFor", { title })
          : t("web.requests.card.requestTitle", { title })
      }
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
          {t("web.requests.card.sending")}
        </>
      ) : (
        <>
          <Plus className="size-3.5 stroke-[2.5]" aria-hidden />
          {t("web.requests.request")}
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
  const t = useT();
  const Icon = inWatchlist ? BookmarkCheck : Bookmark;
  return (
    <button
      type="button"
      disabled={pending}
      aria-pressed={inWatchlist}
      aria-label={
        inWatchlist
          ? t("web.requests.card.removeFromWatchlist", { title })
          : t("web.requests.card.addToWatchlistFor", { title })
      }
      title={
        inWatchlist ? t("web.requests.card.onWatchlist") : t("web.requests.card.addToWatchlist")
      }
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
