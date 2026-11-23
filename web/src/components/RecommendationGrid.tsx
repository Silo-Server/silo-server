import { useCallback, useEffect, useState, type ReactNode } from "react";
import DefaultArtwork from "@/components/DefaultArtwork";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import MediaCarousel from "@/components/MediaCarousel";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { isTerminalNotFoundError } from "@/hooks/queries/mediaSurfaceRefresh";
import { useUICustomization } from "@/hooks/useUICustomization";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";
import CardPlayOverlay from "@/components/CardPlayOverlay";
import { useT } from "@/i18n";

const MAX_MORE_LIKE_THIS_ITEMS = 12;

interface RecommendationGridProps {
  items: Array<{ content_id: string }>;
  maxItems?: number;
}

interface RecommendationItemCardProps {
  itemId: string;
  showCaption: boolean;
  // Sized by the row's wrapper in the default grid; direct card users pass
  // their own width classes.
  className?: string;
  onTerminalError: (itemId: string) => void;
}

function RecommendationItemCard({
  itemId,
  showCaption,
  className,
  onTerminalError,
}: RecommendationItemCardProps) {
  const { data: item, error } = useCatalogItemDetail(itemId);
  // Keyed by URL so a re-signed poster is tried again.
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  // A 404 is terminal: the recommendation points at an item the catalog no
  // longer has. Report it so the grid unmounts this card, which lets the
  // query deactivate instead of polling a dead endpoint forever.
  const terminal = isTerminalNotFoundError(error);

  useEffect(() => {
    if (terminal) onTerminalError(itemId);
  }, [terminal, itemId, onTerminalError]);

  if (terminal) {
    return null;
  }
  if (!item) {
    return (
      <div className={className}>
        <div className="bg-surface aspect-[2/3] animate-pulse rounded-lg" />
      </div>
    );
  }
  return (
    <div className={className ?? "group/card"}>
      <div className="group/media relative">
        <ViewTransitionLink
          to={`/item/${encodeURIComponent(itemId)}`}
          aria-label={item.title}
          className="group block"
        >
          <div className="relative aspect-[2/3] overflow-hidden rounded-lg">
            {item.poster_url && failedUrl !== item.poster_url ? (
              <img
                src={item.poster_url}
                alt={item.title}
                loading="lazy"
                decoding="async"
                onError={() => setFailedUrl(item.poster_url ?? null)}
                className="h-full w-full object-cover transition-transform group-hover:scale-105"
              />
            ) : (
              <DefaultArtwork mediaType={item.type} />
            )}
          </div>
        </ViewTransitionLink>
        {item.play_content_id ? (
          <CardPlayOverlay
            contentId={item.play_content_id}
            title={item.title}
            type={item.type === "movie" ? "movie" : "episode"}
          />
        ) : null}
      </div>
      {showCaption ? (
        <ViewTransitionLink
          to={`/item/${encodeURIComponent(itemId)}`}
          dir="auto"
          className="bidi-text mt-1.5 block truncate text-sm font-medium hover:underline"
        >
          {item.title}
        </ViewTransitionLink>
      ) : null}
    </div>
  );
}

interface MoreLikeThisRowProps<T> {
  items: T[];
  itemKey: (item: T) => string;
  renderItem: (item: T, showCaption: boolean) => ReactNode;
  maxItems?: number;
}

/**
 * The "More Like This" rail on detail pages: poster-width slides inside the
 * page shell, sized by the viewer's card settings. Library items and titles
 * known only from TMDB supply their own cards.
 */
export function MoreLikeThisRow<T>({
  items,
  itemKey,
  renderItem,
  maxItems = MAX_MORE_LIKE_THIS_ITEMS,
}: MoreLikeThisRowProps<T>) {
  const t = useT();
  const { cardPresentation } = useUICustomization();
  const itemLimit = Math.max(0, Math.min(maxItems, MAX_MORE_LIKE_THIS_ITEMS));
  const posterWidthClasses = carouselCardWidthClasses(cardPresentation.poster_size);
  // Card captions are always on: the caption preference was retired, and every
  // card shows its title and details.
  const showCaption = true;

  return (
    <MediaCarousel title={t("web.recommendations.moreLikeThis")} edgePadding={false}>
      {items.slice(0, itemLimit).map((item) => (
        <div key={itemKey(item)} className={posterWidthClasses}>
          {renderItem(item, showCaption)}
        </div>
      ))}
    </MediaCarousel>
  );
}

export default function RecommendationGrid({ items, maxItems = 12 }: RecommendationGridProps) {
  const [failedItemIds, setFailedItemIds] = useState<Set<string>>(() => new Set());
  const handleTerminalError = useCallback((itemId: string) => {
    setFailedItemIds((prev) => {
      if (prev.has(itemId)) return prev;
      const next = new Set(prev);
      next.add(itemId);
      return next;
    });
  }, []);

  // Drop terminally-failed ids before applying the limit so a removed card is
  // replaced by the next healthy recommendation rather than leaving a gap.
  const visibleItems = items.filter((si) => !failedItemIds.has(si.content_id));

  return (
    <MoreLikeThisRow
      items={visibleItems}
      maxItems={maxItems}
      itemKey={(item) => item.content_id}
      renderItem={(item, showCaption) => (
        <RecommendationItemCard
          itemId={item.content_id}
          showCaption={showCaption}
          onTerminalError={handleTerminalError}
        />
      )}
    />
  );
}
