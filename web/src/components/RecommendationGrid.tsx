import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useUICustomization } from "@/hooks/useUICustomization";
import { cardGridClasses } from "@/lib/uiCustomization";
import CardPlayOverlay from "@/components/CardPlayOverlay";

interface RecommendationGridProps {
  items: Array<{ media_item_id: string }>;
  maxItems?: number;
}

interface RecommendationItemCardProps {
  itemId: string;
}

function RecommendationItemCard({ itemId }: RecommendationItemCardProps) {
  const { data: item } = useCatalogItemDetail(itemId);
  const { cardPresentation } = useUICustomization();
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
      {cardPresentation.caption !== "artwork" ? (
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
  return (
    <div className={cardGridClasses(cardPresentation.poster_size)}>
      {items.slice(0, maxItems).map((si) => (
        <RecommendationItemCard key={si.media_item_id} itemId={si.media_item_id} />
      ))}
    </div>
  );
}
