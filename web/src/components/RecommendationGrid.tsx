import type { ReactNode } from "react";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import MediaCarousel from "@/components/MediaCarousel";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useUICustomization } from "@/hooks/useUICustomization";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";
import CardPlayOverlay from "@/components/CardPlayOverlay";

const MAX_MORE_LIKE_THIS_ITEMS = 12;

interface RecommendationGridProps {
  items: Array<{ content_id: string }>;
  maxItems?: number;
}

interface RecommendationItemCardProps {
  itemId: string;
  showCaption: boolean;
}

function RecommendationItemCard({ itemId, showCaption }: RecommendationItemCardProps) {
  const { data: item } = useCatalogItemDetail(itemId);
  if (!item) {
    return <div className="bg-surface aspect-[2/3] animate-pulse rounded-lg" />;
  }
  return (
    <div className="group/card">
      <div className="group/media relative">
        <ViewTransitionLink to={`/item/${encodeURIComponent(itemId)}`} className="group block">
          <div className="aspect-[2/3] overflow-hidden rounded-lg">
            {item.poster_url ? (
              <img
                src={item.poster_url}
                alt={item.title}
                loading="lazy"
                decoding="async"
                className="h-full w-full object-cover transition-transform group-hover:scale-105"
              />
            ) : (
              <div className="bg-surface text-muted-foreground flex h-full items-center justify-center text-xs">
                {item.title}
              </div>
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
          className="mt-1.5 block truncate text-sm font-medium hover:underline"
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
  const { cardPresentation } = useUICustomization();
  const itemLimit = Math.max(0, Math.min(maxItems, MAX_MORE_LIKE_THIS_ITEMS));
  const posterWidthClasses = carouselCardWidthClasses(cardPresentation.poster_size);
  const showCaption = cardPresentation.caption !== "artwork";

  return (
    <MediaCarousel title="More Like This" edgePadding={false}>
      {items.slice(0, itemLimit).map((item) => (
        <div key={itemKey(item)} className={posterWidthClasses}>
          {renderItem(item, showCaption)}
        </div>
      ))}
    </MediaCarousel>
  );
}

export default function RecommendationGrid({ items, maxItems = 12 }: RecommendationGridProps) {
  return (
    <MoreLikeThisRow
      items={items}
      maxItems={maxItems}
      itemKey={(item) => item.content_id}
      renderItem={(item, showCaption) => (
        <RecommendationItemCard itemId={item.content_id} showCaption={showCaption} />
      )}
    />
  );
}
