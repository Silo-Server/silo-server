import type { DiscoverBrandCard, DiscoverBrowseKind } from "@/api/types";
import BrandCard from "@/components/BrandCard";
import { Skeleton } from "@/components/ui/skeleton";

interface BrandCarouselProps {
  kind: DiscoverBrowseKind;
  title: string;
  cards: DiscoverBrandCard[] | undefined;
  isLoading: boolean;
  isError: boolean;
  onRetry?: () => void;
}

export default function BrandCarousel({
  kind,
  title,
  cards,
  isLoading,
  isError,
  onRetry,
}: BrandCarouselProps) {
  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between px-4 sm:px-6 lg:px-10 xl:px-12">
        <h2 className="text-muted-foreground text-sm font-semibold tracking-normal">{title}</h2>
        {isError && onRetry ? (
          <button
            type="button"
            onClick={onRetry}
            className="text-muted-foreground hover:text-foreground text-xs underline-offset-2 hover:underline"
          >
            Retry
          </button>
        ) : null}
      </div>
      <div className="overflow-x-auto px-4 pb-2 sm:px-6 lg:px-10 xl:px-12">
        <div className="flex min-h-20 gap-3">
          {isLoading ? (
            Array.from({ length: 8 }).map((_, idx) => (
              <Skeleton key={idx} className="h-20 w-[140px] flex-none rounded-lg" />
            ))
          ) : isError ? (
            <div className="text-muted-foreground flex h-20 items-center text-xs">
              Could not load {title.toLowerCase()}.
            </div>
          ) : (
            (cards ?? []).map((card) => <BrandCard key={card.slug} kind={kind} card={card} />)
          )}
        </div>
      </div>
    </section>
  );
}
