import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";

interface RecommendationGridProps {
  items: Array<{ media_item_id: string }>;
  maxItems?: number;
}

interface RecommendationItemCardProps {
  itemId: string;
}

function RecommendationItemCard({ itemId }: RecommendationItemCardProps) {
  const { data: item } = useCatalogItemDetail(itemId);
  if (!item) {
    return <div className="bg-surface aspect-[2/3] animate-pulse rounded-lg" />;
  }
  return (
    <ViewTransitionLink to={`/item/${encodeURIComponent(itemId)}`} className="group">
      <div className="aspect-[2/3] overflow-hidden rounded-lg">
        {item.poster_url ? (
          <img
            src={item.poster_url}
            alt={item.title}
            className="h-full w-full object-cover transition-transform group-hover:scale-105"
          />
        ) : (
          <div className="bg-surface text-muted-foreground flex h-full items-center justify-center text-xs">
            {item.title}
          </div>
        )}
      </div>
      <p className="mt-1.5 truncate text-sm font-medium">{item.title}</p>
    </ViewTransitionLink>
  );
}

export default function RecommendationGrid({ items, maxItems = 12 }: RecommendationGridProps) {
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
      {items.slice(0, maxItems).map((si) => (
        <RecommendationItemCard key={si.media_item_id} itemId={si.media_item_id} />
      ))}
    </div>
  );
}
