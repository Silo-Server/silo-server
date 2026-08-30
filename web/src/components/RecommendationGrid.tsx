import ViewTransitionLink from "@/components/ViewTransitionLink";
import MediaCarousel from "@/components/MediaCarousel";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useUICustomization } from "@/hooks/useUICustomization";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";
import CardPlayOverlay from "@/components/CardPlayOverlay";

const MAX_MORE_LIKE_THIS_ITEMS = 12;

interface RecommendationGridProps {
  items: Array<{ media_item_id: string }>;
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

export default function RecommendationGrid({ items, maxItems = 12 }: RecommendationGridProps) {
  const { cardPresentation } = useUICustomization();
  const itemLimit = Math.max(0, Math.min(maxItems, MAX_MORE_LIKE_THIS_ITEMS));
  const posterWidthClasses = carouselCardWidthClasses(cardPresentation.poster_size);

  return (
    <MediaCarousel title="More Like This" edgePadding={false}>
      {items.slice(0, itemLimit).map((si) => (
        <div key={si.media_item_id} className={posterWidthClasses}>
          <RecommendationItemCard
            itemId={si.media_item_id}
            showCaption={cardPresentation.caption !== "artwork"}
          />
        </div>
      ))}
    </MediaCarousel>
  );
}
