import { ChevronLeft, ChevronRight } from "lucide-react";
import type { DiscoverBrandCard, DiscoverBrowseKind } from "@/api/types";
import BrandCard from "@/components/BrandCard";
import { Skeleton } from "@/components/ui/skeleton";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";

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
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla();

  const slides = isLoading
    ? Array.from({ length: 8 }).map((_, idx) => (
        <Skeleton key={idx} className="h-28 w-52 flex-none rounded-xl sm:h-32 sm:w-64" />
      ))
    : (cards ?? []).map((card) => <BrandCard key={card.slug} kind={kind} card={card} />);

  return (
    <section className="group/carousel relative isolate space-y-3">
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

      {isError && !isLoading ? (
        <div className="text-muted-foreground flex h-28 items-center px-4 text-xs sm:px-6 lg:px-10 xl:px-12">
          Could not load {title.toLowerCase()}.
        </div>
      ) : (
        <div className="relative">
          {canScrollPrev && (
            <div className="from-background/80 pointer-events-none absolute top-0 bottom-0 left-0 z-[5] w-10 bg-gradient-to-r to-transparent" />
          )}
          {canScrollPrev && (
            <button
              type="button"
              onClick={scrollPrev}
              className="from-background/80 absolute top-0 bottom-0 left-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-[--duration-fast] group-hover/carousel:opacity-100 focus-visible:opacity-100"
              aria-label="Scroll left"
            >
              <ChevronLeft className="text-foreground h-6 w-6" />
            </button>
          )}

          <div
            ref={emblaRef}
            className="embla__viewport overflow-hidden pr-4 sm:pr-6 lg:pr-10 xl:pr-12"
            tabIndex={0}
            aria-label={`${title} carousel`}
            onKeyDown={(e) => {
              if (e.key === "ArrowLeft") {
                scrollPrev();
              } else if (e.key === "ArrowRight") {
                scrollNext();
              }
            }}
          >
            <ul
              role="list"
              className="embla__container flex cursor-grab list-none gap-4 py-2 pl-4 sm:pl-6 lg:pl-10 xl:pl-12"
            >
              {slides.map((slide, index) => (
                <li key={index} className="embla__slide shrink-0">
                  {slide}
                </li>
              ))}
            </ul>
          </div>

          {canScrollNext && (
            <button
              type="button"
              onClick={scrollNext}
              className="from-background/80 absolute top-0 right-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-[--duration-fast] group-hover/carousel:opacity-100 focus-visible:opacity-100"
              aria-label="Scroll right"
            >
              <ChevronRight className="text-foreground h-6 w-6" />
            </button>
          )}
        </div>
      )}
    </section>
  );
}
