import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import MediaCardArtwork, {
  MEDIA_CARD_CAPTION_CLASS,
  MEDIA_CARD_META_CLASS,
  MEDIA_CARD_TITLE_CLASS,
} from "@/components/MediaCardArtwork";
import CardOverlays from "@/components/overlays/CardOverlays";
import { RequestDownloadBar } from "@/components/overlays/RequestDownloadBar";
import { RequestAction, WatchlistAction } from "@/components/RequestPosterCard";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useUICustomization } from "@/hooks/useUICustomization";
import {
  formatMediaType,
  requestDetailHref,
  requestSearchHref,
  tmdbImageURL,
} from "@/lib/mediaRequests";
import {
  overlayDataFromWatchlistTitle,
  requestDownloadBarPercent,
  type CardOverlayPrefs,
} from "@/lib/overlays";
import { cn } from "@/lib/utils";
import type { WatchlistTitleStatus } from "@/lib/watchlistTitles";

/**
 * A watchlist title the library doesn't have yet. The poster carries the
 * viewer's card overlays, with the request status as one of them; the caption
 * always says the full status in words, so the card reads the same with
 * overlays off. It opens the Discover title page and never offers Play.
 */
export default function WatchlistTitleCard({
  title,
  status,
  overlayPrefs,
  onRequest,
  isRequesting = false,
  onRemove,
  isRemoving = false,
}: {
  title: WatchlistTitle;
  status: WatchlistTitleStatus;
  /** The viewer's overlay preferences; null while loading or with overlays off. */
  overlayPrefs: CardOverlayPrefs | null;
  /** Requests the title from the hover action; shown only on a requestable card. */
  onRequest?: () => void;
  isRequesting?: boolean;
  /** Removes the title from the watchlist from the hover corner action. */
  onRemove?: () => void;
  isRemoving?: boolean;
}) {
  const { cardPresentation } = useUICustomization();
  const showCaption = cardPresentation.caption !== "artwork";
  const href = requestDetailHref(title.media_type, title.tmdb_id);
  const meta = [formatMediaType(title.media_type), title.year].filter(Boolean).join(" · ");
  const label = `${title.title} (${meta})`;
  const overlayData = overlayDataFromWatchlistTitle(title, status);
  const downloadPercent = overlayPrefs
    ? requestDownloadBarPercent(overlayData, overlayPrefs)
    : null;

  return (
    <div className="media-card group/card w-full" data-testid="watchlist-title-card">
      <div className="group/media relative">
        <ViewTransitionLink
          to={href}
          aria-label={label}
          className="block overflow-hidden rounded-xl"
        >
          <MediaCardArtwork
            src={tmdbImageURL(title.poster_path)}
            alt={title.title}
            fallbackLabel={title.title}
            lazy
            dim={status.attention}
            fallbackOnError
          >
            {downloadPercent !== null ? <RequestDownloadBar percent={downloadPercent} /> : null}
            {overlayPrefs ? (
              <CardOverlays
                data={overlayData}
                prefs={overlayPrefs}
                hasProgressBar={downloadPercent !== null}
              />
            ) : null}
          </MediaCardArtwork>
        </ViewTransitionLink>
        {status.requestable && onRequest ? (
          <RequestAction title={label} pending={isRequesting} onRequest={onRequest} />
        ) : null}
        {onRemove ? (
          <div className="absolute right-1.5 bottom-1.5 z-20 sm:right-2.5 sm:bottom-2.5">
            <WatchlistAction
              title={title.title}
              inWatchlist
              pending={isRemoving}
              onToggle={onRemove}
            />
          </div>
        ) : null}
      </div>
      <div className={MEDIA_CARD_CAPTION_CLASS}>
        {showCaption ? (
          <>
            <ViewTransitionLink to={href} className={MEDIA_CARD_TITLE_CLASS}>
              {title.title}
            </ViewTransitionLink>
            <ViewTransitionLink to={href} className={MEDIA_CARD_META_CLASS}>
              {meta}
            </ViewTransitionLink>
          </>
        ) : null}
        {/* The status stays under every caption setting: with overlays off,
            it is the only place the card says where the title stands. */}
        <p
          className={cn(
            "mt-1.5 truncate text-[12px]",
            status.attention ? "text-amber-300" : "text-muted-foreground",
          )}
          data-testid="watchlist-title-caption"
        >
          {status.caption}
          {status.attention ? (
            <>
              {" · "}
              <ViewTransitionLink
                to={requestSearchHref(title.title, title.media_type)}
                className="font-medium underline underline-offset-2 hover:text-amber-200"
              >
                Find it
              </ViewTransitionLink>
            </>
          ) : null}
        </p>
      </div>
    </div>
  );
}
